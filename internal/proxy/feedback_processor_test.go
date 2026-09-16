package proxy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capabilityFeedbackRouter is a reporter whose capability set is read live,
// as a sidecar-backed router's is.
type capabilityFeedbackRouter struct {
	fakePolicyFeedbackRouter
	capabilities policy.Capabilities
}

func (c *capabilityFeedbackRouter) CurrentCapabilities() policy.Capabilities {
	return c.capabilities
}

// acceptedFeedback saves one attached, rated command against a completed
// response served under the given strategy and returns what was saved.
func acceptedFeedback(t *testing.T, store *fakeFeedbackStore, strategy router.Strategy) proxy.RouterFeedbackEvent {
	t.Helper()
	installationID := uuid.NewString()
	key := []byte(uuid.NewString())[:16]
	store.complete(installationID, key, "default_mid", proxy.FeedbackRequest{
		RequestID:       "req-" + uuid.NewString(),
		TrainingAllowed: true,
		ServedModel:     feedbackServedModel,
		ServedProvider:  providers.ProviderAnthropic,
		Strategy:        string(strategy),
		RouteID:         "route-" + uuid.NewString(),
	})
	saved, err := store.AcceptRouterFeedback(context.Background(), proxy.RouterFeedbackEvent{
		ID:              uuid.NewString(),
		ExternalID:      "org-1",
		Sequence:        -1,
		RolloutID:       "rollout-7",
		TrainingAllowed: true,
		InstallationID:  installationID,
		SessionKey:      key,
		Role:            "default_mid",
		RouterUserID:    "user-1",
		ClientApp:       proxy.ClientAppClaudeCode,
		SessionID:       "sess-1",
		RequestedModel:  feedbackRequestedModel,
		Rating:          "down",
		SuggestedLabel:  "high",
		Feedback:        "too slow",
		Source:          proxy.RouterFeedbackSourceUser,
	})
	require.NoError(t, err)
	require.True(t, saved.Attached())
	return saved
}

func processorSvc(t *testing.T, specs ...policy.StrategySpec) *proxy.Service {
	t.Helper()
	svc := newPinSvc(&fakeRouter{}, newFakePinStore())
	for _, spec := range specs {
		svc = svc.WithPolicyStrategy(spec)
	}
	return svc
}

func TestRouterFeedbackProcessor_DeliversSavedIdentityOnce(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	reporter := &fakePolicyFeedbackRouter{}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)

	payloads := reporter.Payloads()
	require.Len(t, payloads, 1)
	payload := payloads[0]
	assert.Equal(t, saved.ID, payload["feedback_id"], "the command UUID is the receiver's deduplication key")
	assert.Equal(t, string(router.StrategyRL), payload["strategy"])
	assert.Equal(t, saved.RequestID, payload["request_id"])
	assert.Equal(t, saved.RouteID, payload["route_id"])
	assert.Equal(t, feedbackServedModel, payload["served_model"])
	assert.Equal(t, feedbackRequestedModel, payload["requested_model"])
	assert.Equal(t, "down", payload["rating"])
	assert.Equal(t, "too slow", payload["feedback"])
	assert.Equal(t, "high", payload["suggested_label"])
	assert.Equal(t, "org-1", payload["organization_id"])
	assert.Equal(t, saved.InstallationID, payload["installation_id"])
	assert.Equal(t, "rollout-7", payload["rollout_id"])
	assert.Equal(t, true, payload["training_allowed"])
	assert.Equal(t, "default_mid", payload["feedback_role"])
	assert.NotEmpty(t, payload["feedback_key"])
	assert.NotContains(t, payload, "training_conversation_delta", "no guessed transcript slice is sent")

	stored := store.event(saved.ID)
	assert.Equal(t, proxy.RouterFeedbackDelivered, stored.DeliveryStatus)
	assert.Empty(t, stored.LeaseToken, "a settled row holds no lease")

	claimed, err = svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	assert.False(t, claimed, "a delivered command is never claimed again")
	assert.Len(t, reporter.Payloads(), 1)
}

func TestRouterFeedbackProcessor_NeverSubstitutesCurrentStrategy(t *testing.T) {
	store := newFakeFeedbackStore()
	acceptedFeedback(t, store, router.StrategyRL)
	rlReporter := &fakePolicyFeedbackRouter{}
	hmmReporter := &fakePolicyFeedbackRouter{}
	svc := processorSvc(t,
		policy.StrategySpec{Strategy: router.StrategyRL, Router: rlReporter, FeedbackRetrySafe: true},
		policy.StrategySpec{Strategy: router.StrategyHMM, Router: hmmReporter, FeedbackRetrySafe: true},
	)

	claimed, err := svc.ProcessRouterFeedback(router.WithStrategy(context.Background(), router.StrategyHMM), store)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Len(t, rlReporter.Payloads(), 1, "the rated request's saved strategy selects the reporter")
	assert.Empty(t, hmmReporter.Payloads(), "a strategy on the processor's context must never be credited")
}

func TestRouterFeedbackProcessor_RetriesTransportFailureWithBackoff(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	reporter := &fakePolicyFeedbackRouter{err: errors.New("sidecar unreachable")}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err, "a reporter failure is rescheduled, not surfaced as a loop failure")
	require.True(t, claimed)

	stored := store.event(saved.ID)
	assert.Equal(t, proxy.RouterFeedbackPending, stored.DeliveryStatus)
	assert.Contains(t, stored.LastError, "sidecar unreachable")
	assert.Equal(t, 1, stored.Attempts)
	assert.Empty(t, stored.LeaseToken, "a rescheduled row releases its lease")

	claimed, err = svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	assert.False(t, claimed, "a rescheduled command is not due before its backoff")

	reporter.setErr(nil)
	store.clearBackoff()
	claimed, err = svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, proxy.RouterFeedbackDelivered, store.event(saved.ID).DeliveryStatus)
	payloads := reporter.Payloads()
	require.Len(t, payloads, 2, "the failed send and its retry are both attempted")
	assert.Equal(t, saved.ID, payloads[0]["feedback_id"])
	assert.Equal(t, saved.ID, payloads[1]["feedback_id"], "the retry carries the same feedback_id, not a new one")
	assert.Equal(t, 1, reporter.DistinctEffects(), "a deduplicating receiver applies the retried event once")
}

func TestRouterFeedbackProcessor_RecoversExpiredLeaseWithSameEventID(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	reporter := &fakePolicyFeedbackRouter{}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	// A worker claims and reports, then dies before it can record delivery.
	staleToken := uuid.NewString()
	claimedEvent, ok, err := store.ClaimRouterFeedback(context.Background(), staleToken, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, reporter.ReportFeedback(context.Background(), map[string]interface{}{"feedback_id": claimedEvent.ID}))

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	assert.False(t, claimed, "an unexpired lease held elsewhere is not claimable")

	store.expireLeases()
	claimed, err = svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed, "an expired lease becomes claimable by a fresh worker")
	assert.Equal(t, proxy.RouterFeedbackDelivered, store.event(saved.ID).DeliveryStatus)
	assert.Equal(t, map[string]int{saved.ID: 2}, reporter.Deliveries(), "the recovered claim redelivers the same event id")
	assert.Equal(t, 1, reporter.DistinctEffects(), "an idempotent receiver applies the redelivered event once")

	// The dead worker's late completion must not overwrite the new owner's result.
	err = store.FinishRouterFeedback(context.Background(), saved.ID, staleToken, proxy.RouterFeedbackSkipped, "late", time.Time{})
	require.ErrorIs(t, err, proxy.ErrFeedbackLeaseLost)
	assert.Equal(t, proxy.RouterFeedbackDelivered, store.event(saved.ID).DeliveryStatus)
}

func TestRouterFeedbackProcessor_KeepsPendingForUnsafeReporter(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	reporter := &fakePolicyFeedbackRouter{}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter})

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)

	assert.Empty(t, reporter.Payloads(), "an unverified receiver must not be sent a retried mutation")
	stored := store.event(saved.ID)
	assert.Equal(t, proxy.RouterFeedbackPending, stored.DeliveryStatus, "the rating waits for a verified receiver instead of being dropped")
	assert.Contains(t, stored.LastError, "deduplicate")
}

func TestRouterFeedbackProcessor_KeepsPendingForMissingStrategy(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyHMM, Router: &fakePolicyFeedbackRouter{}, FeedbackRetrySafe: true})

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)

	stored := store.event(saved.ID)
	assert.Equal(t, proxy.RouterFeedbackPending, stored.DeliveryStatus, "a reporter this process has not wired may come back")
	assert.Contains(t, stored.LastError, "no strategy registered")
}

func TestRouterFeedbackProcessor_SkipsWithReason(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, store *fakeFeedbackStore) (*proxy.Service, *fakePolicyFeedbackRouter, string)
	}{
		{"unattached command has no target", func(t *testing.T, store *fakeFeedbackStore) (*proxy.Service, *fakePolicyFeedbackRouter, string) {
			saved, err := store.AcceptRouterFeedback(context.Background(), proxy.RouterFeedbackEvent{
				ID: uuid.NewString(), InstallationID: uuid.NewString(), SessionKey: []byte("scope"), Role: "default_mid", Sequence: -1, Rating: "down", Feedback: "too slow",
			})
			require.NoError(t, err)
			require.False(t, saved.Attached())
			reporter := &fakePolicyFeedbackRouter{}
			return processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true}), reporter, saved.ID
		}},
		{"default scorer has no learning consumer", func(t *testing.T, store *fakeFeedbackStore) (*proxy.Service, *fakePolicyFeedbackRouter, string) {
			saved := acceptedFeedback(t, store, router.StrategyCluster)
			reporter := &fakePolicyFeedbackRouter{}
			return processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true}), reporter, saved.ID
		}},
		{"registered strategy without a feedback reporter", func(t *testing.T, store *fakeFeedbackStore) (*proxy.Service, *fakePolicyFeedbackRouter, string) {
			saved := acceptedFeedback(t, store, router.StrategyBandit)
			return processorSvc(t, policy.StrategySpec{Strategy: router.StrategyBandit, Router: &fakeRouter{}, FeedbackRetrySafe: true}), &fakePolicyFeedbackRouter{}, saved.ID
		}},
		{"static capabilities declare feedback disabled", func(t *testing.T, store *fakeFeedbackStore) (*proxy.Service, *fakePolicyFeedbackRouter, string) {
			saved := acceptedFeedback(t, store, router.StrategyHMM)
			reporter := &fakePolicyFeedbackRouter{}
			return processorSvc(t, policy.StrategySpec{
				Strategy:          router.StrategyHMM,
				Router:            reporter,
				FeedbackRetrySafe: true,
				Capabilities:      policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, ReportsOutcomes: true},
			}), reporter, saved.ID
		}},
		{"live capabilities declare feedback disabled", func(t *testing.T, store *fakeFeedbackStore) (*proxy.Service, *fakePolicyFeedbackRouter, string) {
			saved := acceptedFeedback(t, store, router.StrategyHMM)
			reporter := &capabilityFeedbackRouter{capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, ReportsOutcomes: true}}
			return processorSvc(t, policy.StrategySpec{
				Strategy:          router.StrategyHMM,
				Router:            reporter,
				FeedbackRetrySafe: true,
				Capabilities:      policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, ReportsFeedback: true},
			}), &reporter.fakePolicyFeedbackRouter, saved.ID
		}},
		{"installation withdrew training permission", func(t *testing.T, store *fakeFeedbackStore) (*proxy.Service, *fakePolicyFeedbackRouter, string) {
			saved := acceptedFeedback(t, store, router.StrategyRL)
			store.trainingDenied = true
			reporter := &fakePolicyFeedbackRouter{}
			return processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true}), reporter, saved.ID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeFeedbackStore()
			svc, reporter, id := tc.setup(t, store)

			claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
			require.NoError(t, err)
			require.True(t, claimed)

			assert.Empty(t, reporter.Payloads(), "a skipped command reaches no receiver")
			stored := store.event(id)
			assert.Equal(t, proxy.RouterFeedbackSkipped, stored.DeliveryStatus)
			assert.Equal(t, "down", stored.Rating, "a skipped command keeps its local rating")
			if stored.Attached() {
				assert.Equal(t, "down", store.ratingFor(stored.RequestID), "the local request rating survives a skipped delivery")
			}
		})
	}
}

func TestRouterFeedbackProcessor_UndeclaredCapabilitiesStillReport(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	// An older sidecar answers no capability document at all; that is not an
	// explicit refusal of feedback.
	reporter := &capabilityFeedbackRouter{}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Len(t, reporter.Payloads(), 1)
	assert.Equal(t, proxy.RouterFeedbackDelivered, store.event(saved.ID).DeliveryStatus)
}

// A negotiated capability document that declares nothing but its schema is a
// sidecar saying feedback is off, not one that never answered. Its reporter
// drops the payload without sending, so acknowledging it as delivered would be
// a lie; the command is skipped with its reason instead.
func TestRouterFeedbackProcessor_ExplicitlyDisabledCapabilitiesAreSkipped(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	reporter := &capabilityFeedbackRouter{capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1}}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Empty(t, reporter.Payloads())
	stored := store.event(saved.ID)
	assert.Equal(t, proxy.RouterFeedbackSkipped, stored.DeliveryStatus)
	assert.Contains(t, stored.LastError, "disabled")
}

func TestRouterFeedbackProcessor_PermissionCheckFailureRetries(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	store.trainingErr = errors.New("permission lookup unavailable")
	reporter := &fakePolicyFeedbackRouter{}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)

	assert.Empty(t, reporter.Payloads(), "delivery must not proceed on an unknown permission")
	stored := store.event(saved.ID)
	assert.Equal(t, proxy.RouterFeedbackPending, stored.DeliveryStatus, "an unavailable permission check is retried, not treated as forbidden")
	assert.Contains(t, stored.LastError, "permission lookup unavailable")
}

func TestRouterFeedbackProcessor_SendIsBoundedBelowLease(t *testing.T) {
	store := newFakeFeedbackStore()
	acceptedFeedback(t, store, router.StrategyRL)
	deadlines := make(chan time.Time, 1)
	reporter := &deadlineCapturingReporter{deadlines: deadlines}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	before := time.Now()
	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)

	select {
	case deadline := <-deadlines:
		remaining := deadline.Sub(before)
		assert.Greater(t, remaining, time.Duration(0), "each send carries a deadline")
		assert.Less(t, remaining, 30*time.Second, "the send deadline must end before the claim lease can expire")
	default:
		t.Fatal("reporter was not called with a deadline-bearing context")
	}
}

type deadlineCapturingReporter struct {
	fakeRouter
	deadlines chan time.Time
}

func (r *deadlineCapturingReporter) ReportFeedback(ctx context.Context, _ map[string]interface{}) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("missing deadline")
	}
	r.deadlines <- deadline
	return nil
}

func TestRouterFeedbackProcessor_RunStopsOnCancellationAndRequiresQueue(t *testing.T) {
	svc := processorSvc(t)
	require.Error(t, svc.RunRouterFeedbackProcessor(context.Background(), nil))

	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	reporter := &fakePolicyFeedbackRouter{}
	svc = processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.RunRouterFeedbackProcessor(ctx, store) }()

	require.Eventually(t, func() bool {
		return store.event(saved.ID).DeliveryStatus == proxy.RouterFeedbackDelivered
	}, 2*time.Second, 10*time.Millisecond, "the loop must drain due work without waiting for an idle poll")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "cancellation is an orderly stop")
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not stop after cancellation")
	}
}

func TestRouterFeedbackProcessor_ClaimFailureIsReported(t *testing.T) {
	store := newFakeFeedbackStore()
	store.claimErr = errors.New("database unavailable")
	svc := processorSvc(t)

	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.ErrorContains(t, err, "database unavailable", "a store failure is an error, not an empty queue")
	assert.False(t, claimed)
}

func TestRouterFeedbackProcessor_SavedOptOutSurvivesLaterOptIn(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	store.events[saved.ID].TrainingAllowed = false
	reporter := &fakePolicyFeedbackRouter{}
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL, Router: reporter, FeedbackRetrySafe: true})
	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Empty(t, reporter.Payloads())
	require.Equal(t, proxy.RouterFeedbackSkipped, store.event(saved.ID).DeliveryStatus)
	require.Contains(t, store.event(saved.ID).LastError, "not granted")
	require.Equal(t, "down", store.ratingFor(saved.RequestID))
}

func TestRouterFeedbackProcessor_MissingConfiguredRouterStaysPending(t *testing.T) {
	store := newFakeFeedbackStore()
	saved := acceptedFeedback(t, store, router.StrategyRL)
	svc := processorSvc(t, policy.StrategySpec{Strategy: router.StrategyRL})
	claimed, err := svc.ProcessRouterFeedback(context.Background(), store)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, proxy.RouterFeedbackPending, store.event(saved.ID).DeliveryStatus)
	require.Contains(t, store.event(saved.ID).LastError, "unavailable")
}
