package policyclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/policy"
)

func TestEverySidecarStatusPreservesDependencyDiagnosis(t *testing.T) {
	for _, test := range []struct {
		status   int
		reason   policy.FailureReason
		attempts int32
	}{
		{400, policy.FailureEvidence, 1},
		{401, policy.FailureAuth, 1},
		{403, policy.FailureAuth, 1},
		{429, policy.FailureOverload, 3},
		{500, policy.FailureTransport, 3},
		{503, policy.FailureOverload, 3},
	} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":"synthetic classifier failure"}`))
			}))
			defer server.Close()
			_, err := New(server.URL, server.Client(), time.Second).Decide(context.Background(), policy.Query{})
			var status *PolicyStatusError
			require.ErrorAs(t, err, &status)
			assert.Equal(t, test.status, status.Status)
			assert.Equal(t, test.reason, policy.FailureReasonFor(err))
			assert.Equal(t, test.attempts, calls.Load())
		})
	}
}

func TestCircuitOpensProbesAndRecoversWithoutContaminatingBeta(t *testing.T) {
	var calls atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"synthetic overload"}`))
			return
		}
		_, _ = w.Write([]byte(`{"schema_version":"policy_router_v1","model":"permitted"}`))
	}))
	defer server.Close()
	now := time.Now()
	stable := New(server.URL, server.Client(), time.Second, WithResilience(ResilienceConfig{FailureThreshold: 2, Now: func() time.Time { return now }}))
	for i := 0; i < 2; i++ {
		_, err := stable.Decide(context.Background(), policy.Query{})
		assert.Equal(t, policy.FailureOverload, policy.FailureReasonFor(err))
	}
	_, err := stable.Decide(context.Background(), policy.Query{})
	assert.Equal(t, policy.FailureCircuitOpen, policy.FailureReasonFor(err))
	assert.Equal(t, int32(6), calls.Load())
	healthy.Store(true)
	beta := New(server.URL, server.Client(), time.Second)
	got, err := beta.Decide(context.Background(), policy.Query{})
	require.NoError(t, err)
	assert.Equal(t, "permitted", got.Model)
	assert.Equal(t, int32(7), calls.Load())
	now = now.Add(31 * time.Second)
	assert.False(t, stable.resilience.ready(), "elapsed cooldown is not proof of recovery")
	assert.True(t, stable.resilience.probeEligible(), "health checks must be able to admit the half-open probe")
	require.NoError(t, stable.CheckHealth(context.Background()))
	assert.True(t, stable.resilience.ready(), "a successful probe restores readiness")
	for i := 0; i < 2; i++ {
		got, err = stable.Decide(context.Background(), policy.Query{})
		require.NoError(t, err)
		assert.Equal(t, "permitted", got.Model)
	}
	assert.Equal(t, int32(12), calls.Load())
}

func TestV3ResponseRequiresExplicitSchemaBeforeCircuitSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"permitted"}`))
	}))
	defer server.Close()
	client := New(server.URL, server.Client(), time.Second, WithResilience(ResilienceConfig{FailureThreshold: 1}))
	_, err := client.Decide(context.Background(), policy.Query{SchemaVersion: policy.SchemaVersionV3})
	assert.Equal(t, policy.FailureContract, policy.FailureReasonFor(err))
	_, err = client.Decide(context.Background(), policy.Query{SchemaVersion: policy.SchemaVersionV3})
	assert.Equal(t, policy.FailureCircuitOpen, policy.FailureReasonFor(err))
}

func TestInvalidClassifierRanksOpenCircuit(t *testing.T) {
	for _, ranks := range []string{
		`[{"group":"low","probability":-1}]`,
		`[{"group":"low","probability":1.1}]`,
		`[{"group":" ","probability":1}]`,
		`[{"group":"low","probability":0.5},{"group":"low","probability":0.5}]`,
		`[{"group":"low","probability":0.2},{"group":"high","probability":0.8}]`,
	} {
		t.Run(ranks, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"schema_version":"policy_router_v3","ranked_fallback":%s}`, ranks)
			}))
			defer server.Close()
			client := New(server.URL, server.Client(), time.Second, WithResilience(ResilienceConfig{FailureThreshold: 1}))
			_, err := client.Decide(context.Background(), policy.Query{SchemaVersion: policy.SchemaVersionV3})
			assert.Equal(t, policy.FailureContract, policy.FailureReasonFor(err))
			_, err = client.Decide(context.Background(), policy.Query{SchemaVersion: policy.SchemaVersionV3})
			assert.Equal(t, policy.FailureCircuitOpen, policy.FailureReasonFor(err))
		})
	}
}

func TestRequestSpecificFailureDoesNotOpenCircuit(t *testing.T) {
	r := newResilience(ResilienceConfig{FailureThreshold: 1})
	for i := 0; i < 10; i++ {
		finish, err := r.admit(context.Background())
		require.NoError(t, err)
		finish(&policy.DependencyError{Reason: policy.FailureEvidence})
	}
	assert.True(t, r.openUntil.IsZero())
}

func TestAdmissionBoundsConcurrencyAndHalfOpenProbes(t *testing.T) {
	now := time.Now()
	r := newResilience(ResilienceConfig{MaxConcurrent: 2, FailureThreshold: 1, Now: func() time.Time { return now }})
	first, err := r.admit(context.Background())
	require.NoError(t, err)
	second, err := r.admit(context.Background())
	require.NoError(t, err)
	_, err = r.admit(context.Background())
	assert.Equal(t, policy.FailureOverload, policy.FailureReasonFor(err))
	first(&policy.DependencyError{Reason: policy.FailureTransport})
	second(nil) // A late success cannot close the newly opened circuit.
	_, err = r.admit(context.Background())
	assert.Equal(t, policy.FailureCircuitOpen, policy.FailureReasonFor(err))
	now = now.Add(31 * time.Second)
	probe, err := r.admit(context.Background())
	require.NoError(t, err)
	_, err = r.admit(context.Background())
	assert.Equal(t, policy.FailureCircuitOpen, policy.FailureReasonFor(err))
	probe(nil)
	finish, err := r.admit(context.Background())
	require.NoError(t, err)
	finish(nil)
	assert.True(t, r.openUntil.IsZero())
}

func TestCanceledHalfOpenProbeDoesNotCloseCircuit(t *testing.T) {
	now := time.Now()
	r := newResilience(ResilienceConfig{FailureThreshold: 1, Now: func() time.Time { return now }})
	finish, err := r.admit(context.Background())
	require.NoError(t, err)
	finish(&policy.DependencyError{Reason: policy.FailureTransport})
	now = now.Add(31 * time.Second)
	probe, err := r.admit(context.Background())
	require.NoError(t, err)
	probe(context.Canceled)
	assert.False(t, r.ready(), "a canceled probe must not mark the sidecar recovered")
	assert.True(t, r.probeEligible(), "a canceled probe must not consume the half-open slot")
	second, err := r.admit(context.Background())
	require.NoError(t, err)
	second(nil)
	assert.True(t, r.ready())
}

func TestSharedPolicyBudgetDoesNotResetOnReroute(t *testing.T) {
	parent := policy.WithDecisionBudget(context.Background())
	first, cancel := policy.DecisionContext(parent, time.Now(), 10*time.Millisecond)
	defer cancel()
	<-first.Done()
	second, cancelSecond := policy.DecisionContext(parent, time.Now(), time.Hour)
	defer cancelSecond()
	assert.ErrorIs(t, second.Err(), context.DeadlineExceeded)
	assert.NoError(t, parent.Err())
	firstDeadline, _ := first.Deadline()
	secondDeadline, _ := second.Deadline()
	assert.Equal(t, firstDeadline, secondDeadline)
}

func TestCanceledCallerNeverContactsSidecar(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(server.URL, server.Client(), time.Second).Decide(ctx, policy.Query{})
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Zero(t, calls.Load())
}
