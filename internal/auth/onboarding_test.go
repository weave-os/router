package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

type onboardingRecorder struct {
	mu            sync.Mutex
	harnesses     []auth.APIKeyFirstUsedEvent
	subscriptions []auth.SubscriptionConnectedEvent
}

func (r *onboardingRecorder) APIKeyFirstUsed(event auth.APIKeyFirstUsedEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.harnesses = append(r.harnesses, event)
}

func (r *onboardingRecorder) SubscriptionConnected(event auth.SubscriptionConnectedEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscriptions = append(r.subscriptions, event)
}

func (r *onboardingRecorder) harnessEvents() []auth.APIKeyFirstUsedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]auth.APIKeyFirstUsedEvent(nil), r.harnesses...)
}

func TestVerifyAPIKeyReportsFirstUseOnceIncludingCacheHits(t *testing.T) {
	installation := &auth.Installation{ID: "installation", ExternalID: "org-test"}
	first := &auth.APIKey{
		ID: "key-first", InstallationID: installation.ID, ExternalID: "kid-first",
		KeyHash: auth.HashAPIKeySHA256("rk_first"), Scope: auth.ScopeRouting,
		CredentialSubjectID: "subject", Harness: "codex",
	}
	keys := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		first.KeyHash: {apiKey: first, installation: installation},
	}}
	recorder := &onboardingRecorder{}
	cache := newRecordingAPIKeyCache()
	svc := auth.NewService(&fakeInstallationRepository{}, keys, nil, nil, cache, nil, frozenClock()).
		WithOnboardingObserver(recorder)

	_, _, _, _, err := svc.VerifyAPIKey(context.Background(), "rk_first")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(recorder.harnessEvents()) == 1 }, time.Second, time.Millisecond)
	want := []auth.APIKeyFirstUsedEvent{{
		InstallationExternalID: installation.ExternalID, CredentialSubjectID: "subject",
		APIKeyID: first.ID, Harness: "codex", OccurredAt: frozenClock()(),
	}}
	require.Equal(t, want, recorder.harnessEvents())

	for range 3 {
		_, _, _, _, err = svc.VerifyAPIKey(context.Background(), "rk_first")
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return len(keys.markUsedSnapshot()) == 4 }, time.Second, time.Millisecond)
	require.Never(t, func() bool { return len(recorder.harnessEvents()) != 1 }, 50*time.Millisecond, time.Millisecond)
	require.Equal(t, want, recorder.harnessEvents())
}

func TestVerifyAPIKeyDoesNotReportIneligibleConnections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness string
		scope   auth.APIKeyScope
		markErr error
	}{
		{name: "analytics", harness: "codex", scope: auth.ScopeAnalyticsRead},
		{name: "persistence failure", harness: "codex", scope: auth.ScopeRouting, markErr: errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.scope.TokenPrefix() + "_test"
			installation := &auth.Installation{ID: "installation", ExternalID: "org-test"}
			key := &auth.APIKey{ID: "key", InstallationID: installation.ID, KeyHash: auth.HashAPIKeySHA256(raw), Scope: tc.scope, Harness: tc.harness}
			svc, keys := makeService(t, fakeKeyRow{apiKey: key, installation: installation})
			keys.markUsedErr = tc.markErr
			recorder := &onboardingRecorder{}
			svc.WithOnboardingObserver(recorder)
			if tc.scope == auth.ScopeAnalyticsRead {
				_, _, err := svc.VerifyAnalyticsAPIKey(context.Background(), raw)
				require.NoError(t, err)
			} else {
				_, _, _, _, err := svc.VerifyAPIKey(context.Background(), raw)
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool { return len(keys.markUsedSnapshot()) == 1 }, time.Second, time.Millisecond)
			require.Never(t, func() bool { return len(recorder.harnessEvents()) != 0 }, 50*time.Millisecond, time.Millisecond)
		})
	}
}

type onboardingSubscriptionRepo struct {
	auth.SubscriptionAccountRepository
	account *auth.SubscriptionAccount
	kind    auth.SubscriptionUpsertKind
	err     error
	params  auth.CreateSubscriptionAccountParams
}

func (r *onboardingSubscriptionRepo) UpsertSubscriptionAccount(_ context.Context, params auth.CreateSubscriptionAccountParams) (*auth.SubscriptionAccount, auth.SubscriptionUpsertKind, error) {
	r.params = params
	kind := r.kind
	if kind == "" {
		kind = auth.SubscriptionUpsertInserted
		if r.account != nil {
			kind = auth.SubscriptionUpsertUpdated
		}
	}
	if r.account == nil {
		r.account = &auth.SubscriptionAccount{ID: "account", Provider: params.Provider}
	}
	return r.account, kind, r.err
}

func TestAddSubscriptionAccountReportsOnlyNewConnections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      auth.SubscriptionUpsertKind
		err       error
		wantEvent bool
	}{
		{name: "insert", kind: auth.SubscriptionUpsertInserted, wantEvent: true},
		{name: "adoption", kind: auth.SubscriptionUpsertAdopted, wantEvent: true},
		{name: "update", kind: auth.SubscriptionUpsertUpdated},
		{name: "failure", kind: auth.SubscriptionUpsertInserted, err: errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &onboardingRecorder{}
			repo := &onboardingSubscriptionRepo{kind: tc.kind, err: tc.err}
			svc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, frozenClock()).
				WithSubscriptionAccounts(repo).WithOnboardingObserver(recorder)
			params := auth.CreateSubscriptionAccountParams{
				Owner:    auth.SubscriptionOwner{SubscriberID: "subscriber", APIKeyID: "key"},
				Provider: auth.SubscriptionProviderClaude, ExternalAccountID: "external-account",
				RefreshToken: []byte("refresh"), InstallationExternalID: "org-test",
			}
			_, err := svc.AddSubscriptionAccount(context.Background(), params)
			require.ErrorIs(t, err, tc.err)
			require.Empty(t, repo.params.InstallationExternalID, "telemetry attribution stays out of persistence")
			if !tc.wantEvent {
				require.Empty(t, recorder.subscriptions)
				return
			}
			want := []auth.SubscriptionConnectedEvent{{
				InstallationExternalID: "org-test", CredentialSubjectID: "subscriber", APIKeyID: "key",
				AccountID: "account", Provider: auth.SubscriptionProviderClaude, OccurredAt: frozenClock()(),
			}}
			require.Equal(t, want, recorder.subscriptions)

			repo.kind = auth.SubscriptionUpsertUpdated
			params.Owner.APIKeyID = "rotated-key"
			_, err = svc.AddSubscriptionAccount(context.Background(), params)
			require.NoError(t, err)
			require.Equal(t, want, recorder.subscriptions, "re-enrollment through a rotated key is not a new connection")
		})
	}
}

func TestOnboardingObserverIsOptional(t *testing.T) {
	svc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, frozenClock()).
		WithSubscriptionAccounts(&onboardingSubscriptionRepo{})
	_, err := svc.AddSubscriptionAccount(context.Background(), auth.CreateSubscriptionAccountParams{
		Owner: auth.SubscriptionOwner{APIKeyID: "key"}, Provider: auth.SubscriptionProviderCodex,
		ExternalAccountID: "external-account", RefreshToken: []byte("refresh"),
	})
	require.NoError(t, err)
}
