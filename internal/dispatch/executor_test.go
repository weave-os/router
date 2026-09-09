package dispatch_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

type fakePlan struct {
	selected     inference.Target
	alternatives []inference.Target
	budget       inference.BudgetSpec
}

func (p fakePlan) Purpose() inference.Purpose { return inference.PurposeHandoverSummary }
func (p fakePlan) DispatchClass() inference.DispatchClass {
	return inference.DispatchClassAuxiliaryInference
}
func (p fakePlan) PolicyID() inference.PolicyID             { return "handover-summary" }
func (p fakePlan) RegistryRevision() string                 { return "reg-1" }
func (p fakePlan) PolicyRevision() inference.PolicyRevision { return "pol-1" }
func (p fakePlan) SelectionStrategy() inference.SelectionStrategy {
	return inference.SelectionStrategyFixedCatalog
}
func (p fakePlan) SelectedTarget() inference.Target { return p.selected }
func (p fakePlan) AlternativeTargets() []inference.Target {
	return append([]inference.Target(nil), p.alternatives...)
}
func (p fakePlan) HardConstraints() []inference.Constraint     { return nil }
func (p fakePlan) SoftPreferences() []inference.SoftPreference { return nil }
func (p fakePlan) Budget() inference.BudgetSpec                { return p.budget }
func (p fakePlan) Provenance() inference.PlanProvenance        { return inference.PlanProvenance{} }

var _ inference.ResolvedPlan = fakePlan{}

// fakeUpstream is a provider client that records the model each Proxy call
// would send and answers from a scripted error queue.
type fakeUpstream struct {
	name   string
	models []string
	errs   []error
}

func (f *fakeUpstream) Proxy(_ context.Context, decision router.Decision, prep providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	f.models = append(f.models, decision.Model)
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *fakeUpstream) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

var (
	primary = inference.Target{CatalogID: "kimi-k2.5", Provider: providers.ProviderFireworks, UpstreamID: "accounts/fireworks/models/kimi-k2p5", BindingIndex: 0}
	backup  = inference.Target{CatalogID: "kimi-k2.5", Provider: providers.ProviderOpenRouter, UpstreamID: "moonshotai/kimi-k2.5", BindingIndex: 1}
)

type recorder struct{ events []inference.AttemptEvent }

func (r *recorder) RecordAttempt(_ context.Context, e inference.AttemptEvent) {
	r.events = append(r.events, e)
}

func newExecutor(t *testing.T, clients map[string]providers.Client, rec *recorder) *dispatch.Executor {
	t.Helper()
	exec, err := dispatch.NewExecutor(dispatch.NewClients(clients),
		dispatch.WithAttemptSink(rec),
		dispatch.WithSleep(func(context.Context, time.Duration) error { return nil }),
	)
	require.NoError(t, err)
	return exec
}

// attemptWith validates the prepared body against the plan target before
// touching the client, then dispatches through Proxy.
func attemptWith(body []byte) dispatch.AttemptFunc {
	return func(ctx context.Context, attempt dispatch.Attempt, client providers.Client) error {
		prep := providers.PreparedRequest{Body: body}
		if err := dispatch.ValidatePreparedTarget(prep, attempt.Target); err != nil {
			return err
		}
		return client.Proxy(ctx, router.Decision{Model: attempt.Target.CatalogID, Provider: attempt.Target.Provider}, prep, nil, nil)
	}
}

func TestRunFailsOverToAlternativeAndRecordsOrderedAttempts(t *testing.T) {
	fw := &fakeUpstream{name: providers.ProviderFireworks, errs: []error{&providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}}}
	or := &fakeUpstream{name: providers.ProviderOpenRouter}
	rec := &recorder{}
	exec := newExecutor(t, map[string]providers.Client{providers.ProviderFireworks: fw, providers.ProviderOpenRouter: or}, rec)

	resets := 0
	result, err := exec.Run(context.Background(),
		inference.InvocationRequest{RequestID: "req-1"},
		fakePlan{selected: primary, alternatives: []inference.Target{backup}},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)), Reset: func() { resets++ }, OperationID: "op-a"},
	)
	require.NoError(t, err)
	assert.Equal(t, backup, result.Outcome.ServedTarget)
	assert.Equal(t, primary, result.Outcome.PolicySelectedTarget)
	assert.True(t, result.Outcome.FallbackUsed)
	assert.Equal(t, 2, result.Outcome.AttemptCount)
	assert.Equal(t, 1, resets)
	assert.Equal(t, dispatch.FailureReasonUpstreamStatus, result.Summary.FallbackReason)
	assert.Equal(t, inference.AccountingOutcomeUsageUnknown, result.Summary.AccountingOutcome)
	assert.Equal(t, []string{"kimi-k2.5"}, fw.models)
	assert.Equal(t, []string{"kimi-k2.5"}, or.models)

	require.Len(t, rec.events, 2)
	assert.Equal(t, 0, rec.events[0].AttemptIndex)
	assert.Equal(t, inference.AttemptOutcomeFailed, rec.events[0].Outcome)
	assert.Equal(t, http.StatusServiceUnavailable, rec.events[0].UpstreamStatusCode)
	assert.Equal(t, 1, rec.events[1].AttemptIndex)
	assert.Equal(t, inference.AttemptOutcomeServed, rec.events[1].Outcome)
	assert.Equal(t, backup, rec.events[1].Target)
	for _, e := range rec.events {
		assert.Equal(t, "op-a", e.OperationID)
		assert.Equal(t, "req-1", e.RequestID)
		assert.Equal(t, inference.PolicyID("handover-summary"), e.PolicyID)
		assert.Equal(t, inference.PolicyRevision("reg-1"), e.RegistryRevision)
		assert.False(t, e.Usage.Known)
	}
}

func TestRunRejectsWireTargetMismatchBeforeIO(t *testing.T) {
	fw := &fakeUpstream{}
	rec := &recorder{}
	exec := newExecutor(t, map[string]providers.Client{providers.ProviderFireworks: fw}, rec)

	_, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"claude-sonnet-4-6"}`))},
	)
	require.ErrorIs(t, err, dispatch.ErrTargetMismatch)
	assert.Empty(t, fw.models, "mismatched wire target must never reach the upstream")
	require.Len(t, rec.events, 1)
	assert.Equal(t, dispatch.FailureReasonTargetMismatch, rec.events[0].FailureReason)
}

func TestRunDoesNotFailOverAfterCommit(t *testing.T) {
	fw := &fakeUpstream{errs: []error{&providers.UpstreamStatusError{Status: http.StatusBadGateway}}}
	or := &fakeUpstream{}
	exec := newExecutor(t, map[string]providers.Client{providers.ProviderFireworks: fw, providers.ProviderOpenRouter: or}, &recorder{})

	result, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary, alternatives: []inference.Target{backup}},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)), Committed: func() bool { return true }},
	)
	require.Error(t, err)
	assert.Empty(t, or.models)
	assert.True(t, result.Outcome.ResponseCommitted)
	assert.Equal(t, dispatch.FailureReasonCommitted, result.Summary.FallbackReason)
}

func TestRunFailsOverOnModelNotFoundButNotOnBadRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		failover bool
	}{
		{"404 walks to next binding", http.StatusNotFound, true},
		{"402 walks to next binding", http.StatusPaymentRequired, true},
		{"400 is final", http.StatusBadRequest, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fw := &fakeUpstream{errs: []error{&providers.UpstreamErrorResponse{Status: tc.status}}}
			or := &fakeUpstream{}
			exec := newExecutor(t, map[string]providers.Client{providers.ProviderFireworks: fw, providers.ProviderOpenRouter: or}, &recorder{})
			_, err := exec.Run(context.Background(), inference.InvocationRequest{},
				fakePlan{selected: primary, alternatives: []inference.Target{backup}},
				dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))},
			)
			if tc.failover {
				require.NoError(t, err)
				assert.Len(t, or.models, 1)
			} else {
				require.Error(t, err)
				assert.Empty(t, or.models)
			}
		})
	}
}

func TestRunRetriesSingleTargetInPlaceWithinBudget(t *testing.T) {
	fw := &fakeUpstream{errs: []error{
		&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests},
		&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests},
	}}
	rec := &recorder{}
	var slept []time.Duration
	exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
		dispatch.WithAttemptSink(rec),
		dispatch.WithSleep(func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }),
	)
	require.NoError(t, err)

	result, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, result.Outcome.AttemptCount)
	assert.False(t, result.Outcome.FallbackUsed)
	assert.Equal(t, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}, slept)
	require.Len(t, rec.events, 3)
	assert.Equal(t, []int{0, 1, 2}, []int{rec.events[0].AttemptIndex, rec.events[1].AttemptIndex, rec.events[2].AttemptIndex})
}

func TestRunMaxAttemptsBoundsSameTargetRetries(t *testing.T) {
	fw := &fakeUpstream{errs: []error{
		&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests},
		&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests},
	}}
	exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
		dispatch.WithSleep(func(context.Context, time.Duration) error { return nil }),
	)
	require.NoError(t, err)

	result, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary, budget: inference.BudgetSpec{MaxAttempts: 1}},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))},
	)
	require.Error(t, err)
	assert.Equal(t, 1, result.Outcome.AttemptCount)
	assert.Equal(t, dispatch.FailureReasonUpstreamStatus, result.Summary.FallbackReason)
}

func TestRunStopsSameTargetRetryWhenWallClockBudgetSpent(t *testing.T) {
	fw := &fakeUpstream{errs: []error{
		&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests},
		&providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests},
	}}
	now := time.Unix(0, 0)
	exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}),
		dispatch.WithClock(func() time.Time { now = now.Add(6 * time.Second); return now }),
		dispatch.WithSleep(func(context.Context, time.Duration) error { return nil }),
	)
	require.NoError(t, err)
	result, err := exec.Run(context.Background(), inference.InvocationRequest{}, fakePlan{selected: primary},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))})
	require.Error(t, err)
	assert.Equal(t, 1, result.Outcome.AttemptCount)
}

func TestRunSkipsUnconfiguredProviderAndHonoursMaxAttempts(t *testing.T) {
	or := &fakeUpstream{}
	rec := &recorder{}
	exec := newExecutor(t, map[string]providers.Client{providers.ProviderOpenRouter: or}, rec)

	result, err := exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary, alternatives: []inference.Target{backup}},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))},
	)
	require.NoError(t, err)
	assert.Equal(t, backup, result.Outcome.ServedTarget)
	require.Len(t, rec.events, 2)
	assert.Equal(t, inference.AttemptOutcomeSkipped, rec.events[0].Outcome)
	assert.Equal(t, dispatch.FailureReasonProviderNotConfigured, rec.events[0].FailureReason)

	_, err = exec.Run(context.Background(), inference.InvocationRequest{},
		fakePlan{selected: primary, alternatives: []inference.Target{backup}, budget: inference.BudgetSpec{MaxAttempts: 1}},
		dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))},
	)
	require.ErrorIs(t, err, dispatch.ErrProviderNotConfigured)
	assert.Len(t, or.models, 1, "MaxAttempts=1 must not reach the alternative")
}

func TestRunAbortsWhenPrepareFails(t *testing.T) {
	fw := &fakeUpstream{}
	rec := &recorder{}
	exec := newExecutor(t, map[string]providers.Client{providers.ProviderFireworks: fw}, rec)
	boom := errors.New("no credentials")
	_, err := exec.Run(context.Background(), inference.InvocationRequest{}, fakePlan{selected: primary},
		dispatch.Transport{
			Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)),
			Prepare: func(context.Context, dispatch.Attempt) (context.Context, error) { return nil, boom },
		})
	require.ErrorIs(t, err, boom)
	assert.Empty(t, fw.models)
	require.Len(t, rec.events, 1)
	assert.Equal(t, inference.AttemptOutcomeAborted, rec.events[0].Outcome)
}

func TestBindSatisfiesInferenceExecutor(t *testing.T) {
	fw := &fakeUpstream{}
	exec := newExecutor(t, map[string]providers.Client{providers.ProviderFireworks: fw}, &recorder{})
	var bound inference.Executor = exec.Bind(dispatch.Transport{Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`))})
	outcome, err := bound.Execute(context.Background(), inference.InvocationRequest{}, fakePlan{selected: primary})
	require.NoError(t, err)
	assert.Equal(t, primary, outcome.ServedTarget)
}

func TestValidateWireModelAcceptsCatalogOrUpstreamID(t *testing.T) {
	require.NoError(t, dispatch.ValidateWireModel(primary.CatalogID, primary))
	require.NoError(t, dispatch.ValidateWireModel(primary.UpstreamID, primary))
	require.ErrorIs(t, dispatch.ValidateWireModel("", primary), dispatch.ErrTargetMismatch)
	require.ErrorIs(t, dispatch.ValidateWireModel("gpt-5", primary), dispatch.ErrTargetMismatch)
	require.NoError(t, dispatch.ValidatePreparedTarget(providers.PreparedRequest{Body: []byte(`{"contents":[]}`)}, primary))
}

func TestClientsRegistryIsDetachedFromSource(t *testing.T) {
	src := map[string]providers.Client{providers.ProviderFireworks: &fakeUpstream{}, providers.ProviderOpenAI: nil}
	clients := dispatch.NewClients(src)
	delete(src, providers.ProviderFireworks)
	assert.True(t, clients.Has(providers.ProviderFireworks))
	assert.True(t, clients.Has(providers.ProviderOpenAI), "nil clients count as registered")
	_, err := clients.Client(providers.ProviderOpenAI)
	require.ErrorIs(t, err, dispatch.ErrProviderNotConfigured, "but can never be dispatched to")
	assert.Equal(t, []string{providers.ProviderFireworks, providers.ProviderOpenAI}, clients.Names())
	_, err = clients.Client(providers.ProviderAnthropic)
	require.ErrorIs(t, err, dispatch.ErrProviderNotConfigured)
}

func TestBufferedTransportValidatesTargetAndDeliversResponse(t *testing.T) {
	fw := &fakeUpstream{}
	exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}))
	require.NoError(t, err)

	var consumed int
	transport := dispatch.Buffered{
		Reason: "test",
		Prepare: func(_ context.Context, attempt dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
			body := []byte(`{"model":"` + attempt.Target.CatalogID + `"}`)
			return providers.PreparedRequest{Body: body}, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)), nil
		},
		Consume: func(_ context.Context, _ dispatch.Attempt, resp *http.Response) error {
			consumed = resp.StatusCode
			return nil
		},
	}.Transport()
	result, err := exec.Run(context.Background(), inference.InvocationRequest{}, fakePlan{selected: primary}, transport)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Outcome.AttemptCount)
	assert.Equal(t, http.StatusOK, consumed)
	assert.Equal(t, []string{"kimi-k2.5"}, fw.models)

	mismatch := dispatch.Buffered{
		Prepare: func(context.Context, dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
			return providers.PreparedRequest{Body: []byte(`{"model":"other"}`)}, httptest.NewRequest(http.MethodPost, "/", nil), nil
		},
	}.Transport()
	_, err = exec.Run(context.Background(), inference.InvocationRequest{}, fakePlan{selected: primary}, mismatch)
	require.ErrorIs(t, err, dispatch.ErrTargetMismatch)
	assert.Len(t, fw.models, 1, "mismatch must not reach upstream")
}

func TestRunTerminalStopsSameTargetRetryAndFailover(t *testing.T) {
	transient := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}
	for _, tc := range []struct {
		name string
		plan fakePlan
	}{
		{"single target", fakePlan{selected: primary}},
		{"multi target", fakePlan{selected: primary, alternatives: []inference.Target{backup}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fw := &fakeUpstream{errs: []error{transient, transient, transient}}
			or := &fakeUpstream{}
			slept := 0
			exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{
				providers.ProviderFireworks: fw, providers.ProviderOpenRouter: or,
			}), dispatch.WithSleep(func(context.Context, time.Duration) error { slept++; return nil }))
			require.NoError(t, err)

			result, err := exec.Run(context.Background(), inference.InvocationRequest{}, tc.plan, dispatch.Transport{
				Attempt:  attemptWith([]byte(`{"model":"kimi-k2.5"}`)),
				Terminal: func(dispatch.Attempt, error) bool { return true },
			})
			require.ErrorIs(t, err, transient)
			assert.Equal(t, 1, result.Outcome.AttemptCount)
			assert.Len(t, fw.models, 1)
			assert.Empty(t, or.models)
			assert.Equal(t, 0, slept)
		})
	}
}

func TestRunBoundRetriesSameTargetButNeverFailsOver(t *testing.T) {
	transient := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}
	slept := 0
	sleep := func(context.Context, time.Duration) error { slept++; return nil }

	t.Run("single target keeps same-target retries", func(t *testing.T) {
		slept = 0
		fw := &fakeUpstream{errs: []error{transient}}
		exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: fw}), dispatch.WithSleep(sleep))
		require.NoError(t, err)

		result, err := exec.Run(context.Background(), inference.InvocationRequest{}, fakePlan{selected: primary}, dispatch.Transport{
			Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)),
			Bound:   func(dispatch.Attempt, error) bool { return true },
		})
		require.NoError(t, err)
		assert.Equal(t, 2, result.Outcome.AttemptCount)
		assert.Len(t, fw.models, 2)
		assert.Equal(t, 1, slept)
	})

	t.Run("multi target never reaches the alternative", func(t *testing.T) {
		slept = 0
		fw := &fakeUpstream{errs: []error{transient, transient, transient}}
		or := &fakeUpstream{}
		exec, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{
			providers.ProviderFireworks: fw, providers.ProviderOpenRouter: or,
		}), dispatch.WithSleep(sleep))
		require.NoError(t, err)

		result, err := exec.Run(context.Background(), inference.InvocationRequest{}, fakePlan{selected: primary, alternatives: []inference.Target{backup}}, dispatch.Transport{
			Attempt: attemptWith([]byte(`{"model":"kimi-k2.5"}`)),
			Bound:   func(dispatch.Attempt, error) bool { return true },
		})
		require.ErrorIs(t, err, transient)
		assert.Equal(t, 1, result.Outcome.AttemptCount)
		assert.Len(t, fw.models, 1)
		assert.Empty(t, or.models, "bound operation must not fail over")
		assert.Equal(t, 0, slept)
	})
}
