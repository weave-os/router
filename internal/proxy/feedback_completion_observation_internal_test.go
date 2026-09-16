package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

func TestFeedbackCompletionPrecedesPolicyOutcomeAdmission(t *testing.T) {
	const model = "claude-sonnet-4-6"
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("historyFailure=%v", fail), func(t *testing.T) {
			workers := testObservationWorkers(t)
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			t.Cleanup(unblock)
			for range 256 {
				workers.Remote.Submit(observability.WorkOutcome, nil, time.Minute, observability.FromContext(context.Background()), func(ctx context.Context, _ []byte) error {
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}
			reporter := &captureHMMOutcomeReporter{ch: make(chan map[string]interface{}, 1)}
			svc := (&Service{}).WithObservationWorkers(workers).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: reporter})
			rec := httptest.NewRecorder()
			store := &completionTestStore{complete: func() error {
				if fail {
					return errors.New("history unavailable")
				}
				return nil
			}}
			gate := newFeedbackCompletion(rec, translate.EscalationResponseAnthropic, false)
			gate.active, gate.store = true, store
			_, err := gate.Write([]byte(completionStreams[0].body))
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), feedbackCompletionContextKey{}, gate))
			defer cancel()
			decision := router.Decision{Provider: providers.ProviderAnthropic, Model: model, Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM), RouteID: "route-completed"}}
			submitted := make(chan struct{})
			finished := make(chan error, 1)
			finalized := make(chan struct{})
			// This runs before the first admission, after finish released/aborted output.
			gate.observations = append(gate.observations, func() { close(finalized) })
			go func() {
				svc.reportPolicyOutcome(ctx, turnLoopResult{Fresh: decision}, decision, effortResolution{}, decision.Provider, false, 10, 10, 2, 0, 0, 1, 2, nil, nil)
				close(submitted)
				finished <- gate.finish(ctx, nil)
			}()
			select {
			case <-submitted:
			case <-time.After(time.Second):
				t.Fatal("policy outcome submission blocked before response finalization")
			}
			select {
			case <-finalized:
			case <-time.After(time.Second):
				t.Fatal("response was not finalized before policy admission")
			}
			if fail {
				require.Empty(t, rec.Body.String())
				require.Empty(t, store.records)
			} else {
				require.Equal(t, completionStreams[0].body, rec.Body.String())
				require.Len(t, store.records, 1)
			}
			select {
			case <-finished:
				t.Fatal("policy admission no longer applies backpressure")
			default:
			}
			cancel()
			unblock()
			select {
			case err := <-finished:
				if fail {
					require.ErrorContains(t, err, "history unavailable")
				} else {
					require.NoError(t, err)
				}
			case <-time.After(time.Second):
				t.Fatal("policy admission did not resume")
			}
			select {
			case payload := <-reporter.ch:
				require.Equal(t, "route-completed", payload["route_id"])
				require.Equal(t, decision.Model, payload["served_model"])
			case <-time.After(time.Second):
				t.Fatal("policy outcome was lost")
			}
		})
	}
}
