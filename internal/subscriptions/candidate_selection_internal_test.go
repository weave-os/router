package subscriptions

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

func TestCandidateReservationsBalanceConcurrentRefreshes(t *testing.T) {
	now := time.Now()
	runtime := NewRuntime(nil, nil, func() time.Time { return now })
	accounts := []*auth.SubscriptionAccount{
		{ID: "sooner", SubscriberID: "borrower"},
		{ID: "later", SubscriberID: "borrower"},
	}
	signals := map[string]CandidateSignal{
		"sooner": {Observed: true, UsedFraction: .3, ResetAt: now.Add(time.Minute)},
		"later":  {Observed: true, UsedFraction: .1, ResetAt: now.Add(time.Hour)},
	}
	const borrowers = 40
	selected := make(chan string, borrowers)
	releases := make(chan func(), borrowers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range borrowers {
		workers.Go(func() {
			<-start
			account, release := runtime.reserveCandidate(t.Context(), append([]*auth.SubscriptionAccount(nil), accounts...), auth.SubscriptionOwner{SubscriberID: "borrower"}, ProviderClaude, "sooner", signals)
			selected <- account.ID
			releases <- release
		})
	}
	close(start)
	workers.Wait()
	close(selected)
	counts := map[string]int{}
	for id := range selected {
		counts[id]++
	}
	require.Equal(t, 20, counts["sooner"])
	require.Equal(t, 20, counts["later"])
	close(releases)
	for release := range releases {
		release()
		release()
	}
	account, release := runtime.reserveCandidate(t.Context(), accounts, auth.SubscriptionOwner{SubscriberID: "borrower"}, ProviderClaude, "later", signals)
	defer release()
	require.Equal(t, "sooner", account.ID, "release restores expiry priority; double release must not create negative load")
}

func TestCandidateExplorationRespectsBudgetAndHealth(t *testing.T) {
	for _, state := range []CandidateSignal{
		{},
		{Observed: true, ResetAt: time.Now().Add(time.Hour)},
		{Observed: true, Exhausted: true},
		{Observed: true, Billable: true},
	} {
		t.Run(stringState(state), func(t *testing.T) {
			now := time.Now()
			runtime := NewRuntime(nil, nil, func() time.Time { return now })
			accounts := []*auth.SubscriptionAccount{{ID: "active", SubscriberID: "borrower"}, {ID: "idle", SubscriberID: "borrower"}}
			signals := map[string]CandidateSignal{"active": {Observed: true, ResetAt: now.Add(time.Minute)}, "idle": state}
			var output bytes.Buffer
			ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
			counts := map[string]int{}
			for range 100 {
				account, release := runtime.reserveCandidate(ctx, accounts, auth.SubscriptionOwner{SubscriberID: "borrower"}, ProviderClaude, "active", signals)
				counts[account.ID]++
				release()
			}
			if state.Exhausted || state.Billable {
				require.Zero(t, counts["idle"])
			} else {
				require.Equal(t, 1, counts["idle"], "one exploratory attempt per idle seat within five minutes")
				require.Contains(t, output.String(), string(candidateSelectionProbe))
				now = now.Add(candidateExplorationIdle)
				account, release := runtime.reserveCandidate(ctx, accounts, auth.SubscriptionOwner{SubscriberID: "borrower"}, ProviderClaude, "active", signals)
				release()
				require.Equal(t, "idle", account.ID, "an idle seat becomes probeable again")
			}
		})
	}
}

func stringState(signal CandidateSignal) string {
	switch {
	case signal.Exhausted:
		return "spent"
	case signal.Billable:
		return "billable"
	case signal.Observed:
		return "idle_known"
	default:
		return "unknown"
	}
}

func TestCandidateExplorationPreservesOwnershipAndIsolatesScopes(t *testing.T) {
	now := time.Now()
	runtime := NewRuntime(nil, nil, func() time.Time { return now })
	accounts := []*auth.SubscriptionAccount{{ID: "personal", SubscriberID: "borrower"}, {ID: "shared", SubscriberID: "another"}}
	signals := map[string]CandidateSignal{"personal": {Observed: true, ResetAt: now.Add(time.Hour)}}
	for range 20 {
		selected, release := runtime.reserveCandidate(t.Context(), accounts, auth.SubscriptionOwner{SubscriberID: "borrower", InstallationID: "first"}, ProviderClaude, "shared", signals)
		release()
		require.Equal(t, "personal", selected.ID)
	}
	accounts[1].SubscriberID = "borrower"
	selected, release := runtime.reserveCandidate(t.Context(), accounts, auth.SubscriptionOwner{SubscriberID: "borrower", InstallationID: "second"}, ProviderClaude, "shared", signals)
	release()
	require.Equal(t, "personal", selected.ID, "another installation cannot spend this scope's exploration budget")
}

func TestCandidateComparisonTelemetryIsBounded(t *testing.T) {
	now := time.Now()
	runtime := NewRuntime(nil, nil, func() time.Time { return now })
	accounts := make([]*auth.SubscriptionAccount, 20)
	for i := range accounts {
		accounts[i] = &auth.SubscriptionAccount{ID: time.Unix(int64(i), 0).String(), SubscriberID: "borrower"}
	}
	var output bytes.Buffer
	ctx := observability.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil)))
	selected, release := runtime.reserveCandidate(ctx, accounts, auth.SubscriptionOwner{SubscriberID: "borrower"}, ProviderCodex, "", map[string]CandidateSignal{})
	release()
	var event struct {
		Event      auth.SubscriptionCredentialEventKind `json:"subscription_event"`
		Candidates []candidateComparison                `json:"candidates"`
		Count      int                                  `json:"candidate_count"`
		Truncated  bool                                 `json:"candidates_truncated"`
		AccountID  string                               `json:"account_id"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, auth.SubscriptionCredentialEventCandidateEvaluated, event.Event)
	require.Equal(t, selected.ID, event.AccountID)
	require.Equal(t, 20, event.Count)
	require.Len(t, event.Candidates, 16)
	require.True(t, event.Truncated)
}

func TestCandidateExplorationHasGlobalTimeLimitPerScope(t *testing.T) {
	now := time.Now()
	runtime := NewRuntime(nil, nil, func() time.Time { return now })
	accounts := []*auth.SubscriptionAccount{{ID: "active", SubscriberID: "borrower"}, {ID: "idle-a", SubscriberID: "borrower"}, {ID: "idle-b", SubscriberID: "borrower"}}
	signals := map[string]CandidateSignal{"active": {Observed: true, ResetAt: now.Add(time.Minute)}}
	counts := map[string]int{}
	for range 100 {
		selected, release := runtime.reserveCandidate(t.Context(), accounts, auth.SubscriptionOwner{SubscriberID: "borrower"}, ProviderClaude, "active", signals)
		counts[selected.ID]++
		release()
	}
	require.Equal(t, 99, counts["active"], "at most one lower-ranked probe during the thirty-second interval")
	require.Equal(t, 1, counts["idle-a"])
	require.Zero(t, counts["idle-b"])
	now = now.Add(candidateExplorationInterval)
	selected, release := runtime.reserveCandidate(t.Context(), accounts, auth.SubscriptionOwner{SubscriberID: "borrower"}, ProviderClaude, "active", signals)
	defer release()
	require.Equal(t, "idle-b", selected.ID, "the next probe discovers a different idle seat")
}
