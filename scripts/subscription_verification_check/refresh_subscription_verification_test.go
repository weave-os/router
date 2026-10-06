package subscription_verification_check_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/subscriptions"
)

func TestVerificationSQLConcurrentSharedRefreshAndReset(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("local Postgres integration requires ROUTER_TEST_DATABASE_URL")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname())
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	org, owner, borrowerA, borrowerB, key := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name) VALUES($1,$2,'verification')`, org, org.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_subscription_accounts WHERE api_key_id=$1`, key)
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_api_keys WHERE id=$1`, key)
		_, _ = pool.Exec(ctx, `DELETE FROM router.credential_subject_installations WHERE installation_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM router.credential_subjects WHERE id=ANY($1::uuid[])`, []uuid.UUID{owner, borrowerA, borrowerB})
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_installations WHERE id=$1`, org)
	})
	for _, subject := range []uuid.UUID{owner, borrowerA, borrowerB} {
		_, err = pool.Exec(ctx, `INSERT INTO router.credential_subjects(id,projection_complete) VALUES($1,true)`, subject)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `INSERT INTO router.credential_subject_installations(subject_id,installation_id,access_enabled) VALUES($1,$2,true)`, subject, org)
		require.NoError(t, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO router.model_router_api_keys(id,installation_id,external_id,key_prefix,key_hash,key_suffix) VALUES($1,$2,$3,'verify',$3,'test')`, key, org, key.String())
	require.NoError(t, err)
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)))
	enc, err := auth.NewTinkEncryptor(buf.String())
	require.NoError(t, err)
	service := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithEncryptor(enc).WithSubscriptionAccounts(postgres.NewSubscriptionAccountRepo(pool)).WithCodexEnrollmentVerifier(verificationCodexEnrollment{})
	// Two owners concurrently register the same physical provider identity.
	enrollmentErrors := make([]error, 2)
	enrolled := make([]*auth.SubscriptionAccount, 2)
	var enrollmentWG sync.WaitGroup
	physicalID := "synthetic-enrollment-" + uuid.NewString()
	for i, subject := range []uuid.UUID{owner, borrowerB} {
		enrollmentWG.Add(1)
		go func(i int, subject uuid.UUID) {
			defer enrollmentWG.Done()
			enrolled[i], enrollmentErrors[i] = service.AddSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: auth.SubscriptionOwner{InstallationID: org.String(), SubscriberID: subject.String(), APIKeyID: key.String()}, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: physicalID, RefreshToken: []byte("synthetic-enrollment-refresh")})
		}(i, subject)
	}
	enrollmentWG.Wait()
	var successful int
	var enrolledID string
	for i, err := range enrollmentErrors {
		if err == nil {
			successful++
			enrolledID = enrolled[i].ID
		}
	}
	require.Equal(t, 1, successful, "concurrent owner registrations must preserve exactly one physical credential")
	var physicalRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM router.model_router_subscription_accounts WHERE external_account_id=$1`, physicalID).Scan(&physicalRows))
	require.Equal(t, 1, physicalRows)
	_, err = pool.Exec(ctx, `DELETE FROM router.model_router_subscription_accounts WHERE id=$1`, enrolledID)
	require.NoError(t, err)
	account, err := service.AddSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: auth.SubscriptionOwner{InstallationID: org.String(), SubscriberID: owner.String(), APIKeyID: key.String()}, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: "synthetic-shared-refresh-" + owner.String(), RefreshToken: []byte("synthetic-refresh-original")})
	require.NoError(t, err)
	var refreshes atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		t.Logf("refresh token exchange: %s", r.Form.Get("refresh_token"))
		refreshes.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": verificationCodexIDToken(t, strings.SplitN(r.Form.Get("refresh_token"), ":", 2)[1]), "access_token": "synthetic-access-refreshed", "refresh_token": "synthetic-refresh-rotated", "expires_in": 3600, "token_type": "Bearer"})
	}))
	defer tokenServer.Close()
	oauth := subscriptions.NewOAuthClient(tokenServer.Client(), tokenServer.URL, tokenServer.URL, time.Now)
	runtimes := []*subscriptions.Runtime{subscriptions.NewRuntime(service, oauth, time.Now), subscriptions.NewRuntime(service, oauth, time.Now)}
	requesters := []auth.SubscriptionOwner{{InstallationID: org.String(), SubscriberID: borrowerA.String(), APIKeyID: key.String()}, {InstallationID: org.String(), SubscriberID: borrowerB.String(), APIKeyID: key.String()}}
	leases := make([]subscriptions.Lease, 2)
	errs := make([]error, 2)
	present := make([]bool, 2)
	var wg sync.WaitGroup
	for i := range runtimes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			leases[i], present[i], errs[i] = runtimes[i].Lease(ctx, requesters[i], subscriptions.ProviderCodex, "synthetic-session")
		}(i)
	}
	wg.Wait()
	for i := range leases {
		require.NoError(t, errs[i])
		require.True(t, present[i])
		require.Equal(t, account.ID, leases[i].AccountID)
		require.Equal(t, owner.String(), leases[i].OwnerID)
		require.Equal(t, auth.SubscriptionTierShared, leases[i].Tier)
		require.Equal(t, "synthetic-access-refreshed", leases[i].AccessToken)
		leases[i].Release()
	}
	require.Equal(t, int32(1), refreshes.Load(), "different borrowers and replicas must refresh one physical credential once")
	var version int64
	var leaseReleased bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT token_refresh_version,token_refresh_lease_id IS NULL FROM router.model_router_subscription_accounts WHERE id=$1`, account.ID).Scan(&version, &leaseReleased))
	require.Equal(t, int64(1), version)
	require.True(t, leaseReleased)
	require.NoError(t, runtimes[0].Exhaust(ctx, requesters[0], subscriptions.ProviderCodex, account.ID, time.Now().Add(100*time.Millisecond)))
	_, _, err = runtimes[1].Lease(ctx, requesters[1], subscriptions.ProviderCodex, "exhausted")
	require.ErrorIs(t, err, subscriptions.ErrNoAvailableAccount)
	time.Sleep(150 * time.Millisecond)
	resetLease, _, err := runtimes[1].Lease(ctx, requesters[1], subscriptions.ProviderCodex, "reset")
	require.NoError(t, err)
	require.Equal(t, "synthetic-access-refreshed", resetLease.AccessToken)
	_, err = pool.Exec(ctx, `UPDATE router.model_router_installations SET subscription_sharing_enabled=false WHERE id=$1`, org)
	require.NoError(t, err)
	blockedLease, available, leaseErr := runtimes[0].Lease(ctx, requesters[0], subscriptions.ProviderCodex, "sharing-off-new-admission")
	require.NoError(t, leaseErr)
	require.False(t, available)
	require.Empty(t, blockedLease.AccessToken)
	resetLease.Release()
	_, err = pool.Exec(ctx, `UPDATE router.model_router_installations SET subscription_sharing_enabled=true WHERE id=$1`, org)
	require.NoError(t, err)
	require.Equal(t, int32(1), refreshes.Load())
	verifySQLTierLocalAffinity(t, service, runtimes[0], requesters[0], borrowerB, account.ID)
	verifyDispatchBilling(t, pool, runtimes[0], requesters[0], owner, org, key)
	_, err = pool.Exec(ctx, `UPDATE router.credential_subject_installations SET access_enabled=false WHERE subject_id=$1 AND installation_id=$2`, owner, org)
	require.NoError(t, err)
	revoked, available, err := runtimes[0].Lease(ctx, requesters[0], subscriptions.ProviderCodex, "revoked")
	require.NoError(t, err)
	require.False(t, available)
	require.Empty(t, revoked.AccessToken, "cached credentials cannot outlive SQL admission")

}

type verificationBillingRouter struct{}

func (verificationBillingRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "test"}, nil
}

type verificationIncludedClient struct{ *openai.Client }

func (*verificationIncludedClient) IncludedOnlySubscriptions() bool { return true }
func verifyDispatchBilling(t *testing.T, pool *pgxpool.Pool, runtime *subscriptions.Runtime, requester auth.SubscriptionOwner, owner, org, key uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, subject := range []uuid.UUID{owner, uuid.MustParse(requester.SubscriberID)} {
		_, err := pool.Exec(ctx, `INSERT INTO router.subscriber_credit_balance(subscriber_id,balance_usd_micros) VALUES($1,3000000)`, subject)
		require.NoError(t, err)
	}
	book := postgres.NewSubscriberCreditRepo(pool)
	billingService := billing.NewService(postgres.NewBillingRepo(pool)).WithSubscriberPrepaid(book)
	for _, apiFallback := range []bool{false, true} {
		var bearers []string
		var bearersMu sync.Mutex
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bearer := r.Header.Get("Authorization")
			bearersMu.Lock()
			bearers = append(bearers, bearer)
			bearersMu.Unlock()
			if apiFallback && bearer == "Bearer synthetic-access-refreshed" {
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"error":{"code":"usage_limit_reached","message":"synthetic quota"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"billing answer\"}\n\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11000,\"output_tokens\":7000}}}\n\n")
		}))
		client := &verificationIncludedClient{openai.NewClient("synthetic-api-key", server.URL)}
		client.SetCodexBaseURL(server.URL)
		svc := proxy.NewService(verificationBillingRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil).WithManagedSubscriptions(runtime).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).WithBillingService(billingService)
		actionID, requestID := uuid.NewString(), uuid.NewString()
		authorization, err := billingService.AuthorizeSubscriberPrepaid(ctx, billing.PrepaidAuthorizationRequest{Owner: billing.SubscriberOwner(requester.SubscriberID), ActionID: actionID, RouterRequestID: requestID, APIKeyID: key.String(), RequestedModel: "auto", UpperBoundUsdMicros: 1000000})
		require.NoError(t, err)
		requestCtx := billing.WithPrepaidAuthorization(ctx, authorization)
		requestCtx = proxy.WithSubscriptionOwner(requestCtx, requester)
		requestCtx = proxy.WithManagedSubscriptionUsage(requestCtx)
		requestCtx = context.WithValue(requestCtx, proxy.APIKeyIDContextKey{}, key.String())
		requestCtx = context.WithValue(requestCtx, proxy.InstallationIDContextKey{}, org.String())
		requestCtx = context.WithValue(requestCtx, proxy.ExternalIDContextKey{}, org.String())
		requestCtx = context.WithValue(requestCtx, proxy.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderCodex: {}})
		body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIChatCompletion(requestCtx, []byte(body), rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))))
		require.Contains(t, rec.Body.String(), "billing answer")
		_, err = billingService.FinalizeSubscriberPrepaid(ctx, actionID)
		require.NoError(t, err)
		requesterBalance, err := book.Balance(ctx, billing.SubscriberOwner(requester.SubscriberID))
		require.NoError(t, err)
		ownerBalance, err := book.Balance(ctx, billing.SubscriberOwner(owner.String()))
		require.NoError(t, err)
		require.Equal(t, int64(3000000), ownerBalance, "borrowed owner must never pay for requester API")
		if apiFallback {
			bearersMu.Lock()
			require.Equal(t, []string{"Bearer synthetic-access-refreshed", "Bearer synthetic-api-key"}, bearers)
			bearersMu.Unlock()
			require.Less(t, requesterBalance, int64(3000000))
			var settlements int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM router.subscriber_credit_ledger WHERE subscriber_id=$1`, requester.SubscriberID).Scan(&settlements))
			require.Equal(t, 1, settlements)
		} else {
			bearersMu.Lock()
			require.Equal(t, []string{"Bearer synthetic-access-refreshed"}, bearers)
			bearersMu.Unlock()
			require.Equal(t, int64(3000000), requesterBalance)
		}
		server.Close()
	}
}

func verifySQLTierLocalAffinity(t *testing.T, service *auth.Service, runtime *subscriptions.Runtime, requester auth.SubscriptionOwner, secondOwner uuid.UUID, firstAccountID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, runtime.Activate(ctx, requester, subscriptions.ProviderCodex, firstAccountID))
	second, err := service.AddSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: auth.SubscriptionOwner{InstallationID: requester.InstallationID, SubscriberID: secondOwner.String(), APIKeyID: requester.APIKeyID}, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: "synthetic-affinity-shared-" + uuid.NewString(), RefreshToken: []byte("synthetic-refresh-original")})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, service.DeleteSubscriptionAccount(ctx, auth.SubscriptionOwner{InstallationID: requester.InstallationID, SubscriberID: secondOwner.String(), APIKeyID: requester.APIKeyID}, second.ID))
	}()
	first, _, err := runtime.Lease(ctx, requester, subscriptions.ProviderCodex, "synthetic-affinity-session")
	require.NoError(t, err)
	require.Equal(t, firstAccountID, first.AccountID)
	require.Equal(t, auth.SubscriptionTierShared, first.Tier)
	first.Release()
	require.NoError(t, runtime.Exhaust(ctx, requester, subscriptions.ProviderCodex, firstAccountID, time.Now().Add(100*time.Millisecond)))
	rotated, _, err := runtime.Lease(ctx, requester, subscriptions.ProviderCodex, "synthetic-affinity-session")
	require.NoError(t, err)
	require.Equal(t, second.ID, rotated.AccountID)
	require.Equal(t, secondOwner.String(), rotated.OwnerID)
	rotated.Release()
	time.Sleep(150 * time.Millisecond)
	retained, _, err := runtime.Lease(ctx, requester, subscriptions.ProviderCodex, "synthetic-affinity-session")
	require.NoError(t, err)
	require.Equal(t, second.ID, retained.AccountID, "quota-reset earlier shared account cannot break session affinity within same tier")
	retained.Release()
	personal, err := service.AddSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: requester, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: "synthetic-affinity-personal-" + uuid.NewString(), RefreshToken: []byte("synthetic-refresh-original")})
	require.NoError(t, err)
	defer func() { require.NoError(t, service.DeleteSubscriptionAccount(ctx, requester, personal.ID)) }()
	promoted, _, err := runtime.Lease(ctx, requester, subscriptions.ProviderCodex, "synthetic-affinity-session")
	require.NoError(t, err)
	require.Equal(t, personal.ID, promoted.AccountID, "personal capacity always precedes remembered shared capacity")
	require.Equal(t, requester.SubscriberID, promoted.OwnerID)
	require.Equal(t, auth.SubscriptionTierPersonal, promoted.Tier)
	promoted.Release()
}
