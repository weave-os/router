package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

type completionObservationStore struct {
	proxy.TelemetryRepository
	history   chan proxy.FeedbackRequest
	telemetry chan proxy.InsertTelemetryParams
	attempts  chan proxy.InsertInferenceAttemptParams
}

func (s *completionObservationStore) CompleteFeedbackRequest(ctx context.Context, r proxy.FeedbackRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.history <- r
	return nil
}

func (*completionObservationStore) AcceptRouterFeedback(_ context.Context, event proxy.RouterFeedbackEvent) (proxy.RouterFeedbackEvent, error) {
	return event, nil
}

func (s *completionObservationStore) InsertRequestTelemetry(_ context.Context, p proxy.InsertTelemetryParams) error {
	s.telemetry <- p
	return nil
}

func (s *completionObservationStore) InsertInferenceAttempt(_ context.Context, p proxy.InsertInferenceAttemptParams) error {
	s.attempts <- p
	return nil
}

type completionOutput struct {
	mu     sync.Mutex
	header http.Header
	body   strings.Builder
}

func (w *completionOutput) Header() http.Header { return w.header }
func (*completionOutput) WriteHeader(int)       {}
func (*completionOutput) Flush()                {}
func (w *completionOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}
func (w *completionOutput) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func TestFeedbackCompletionPrecedesSaturatedObservations(t *testing.T) {
	cases := append(completionSurfaces[:len(completionSurfaces):len(completionSurfaces)], completionSurfaces[0])
	cases[len(cases)-1].name = "usage_bypass"
	cases[len(cases)-1].model = bypassRequestedMdl
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				workers := testObservationWorkers(t)
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				t.Cleanup(unblock)
				saturated := make(chan struct{})
				store := &completionObservationStore{history: make(chan proxy.FeedbackRequest, 2), telemetry: make(chan proxy.InsertTelemetryParams, 2), attempts: make(chan proxy.InsertInferenceAttemptParams, 4)}
				body, contentType := tc.body, "application/json"
				request := tc.request
				if stream {
					body, contentType = tc.stream, "text/event-stream"
					request = strings.TrimSuffix(request, "}") + `,"stream":true}`
				}
				provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", contentType)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(body))
					// Reservations include running jobs, so all 256 slots stay full.
					for range 256 {
						workers.Database.Submit(observability.WorkTelemetry, nil, time.Minute, observability.FromContext(context.Background()), func(ctx context.Context, _ []byte) error {
							select {
							case <-release:
								return nil
							case <-ctx.Done():
								return ctx.Err()
							}
						})
					}
					close(saturated)
				}}
				clients := map[string]providers.Client{tc.provider: provider}
				executor, err := dispatch.NewExecutor(dispatch.NewClients(clients), dispatch.WithAttemptSink(proxy.NewAttemptSink(store, workers.Database)))
				require.NoError(t, err)
				svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: tc.provider, Model: tc.model}}, clients, nil, false, nil, nil, false, tc.provider, tc.model, store).
					WithRouterFeedbackStore(store).WithObservationWorkers(workers).WithInferenceExecutor(executor).WithOpenAIResponsesBroad(false)
				ctx := authedCtx(uuid.NewString())
				if tc.name == "usage_bypass" {
					obs := usage.NewObserver([]byte("test-salt"), time.Minute, time.Now)
					obs.Record(obs.Key([]byte(bypassSubToken)), usage.Snapshot{Primary: usage.Window{UsedPercent: .2, WindowMinutes: 300}})
					svc.WithSubscriptionAwareRouting(obs, .05, 2)
					ctx = context.WithValue(ctx, proxy.AnthropicSubscriptionContextKey{}, bypassSubToken)
					threshold := .8
					ctx = context.WithValue(ctx, proxy.InstallationUsageBypassContextKey{}, proxy.UsageBypassConfig{Enabled: true, Threshold: &threshold})
				}
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				out := &completionOutput{header: make(http.Header)}
				done := make(chan error, 1)
				req := httptest.NewRequest(http.MethodPost, "/test", nil)
				req.Header.Set(routingMarkerHeader, "off")
				go func() { done <- tc.call(svc, ctx, []byte(request), out, req) }()
				select {
				case <-saturated:
				case <-time.After(3 * time.Second):
					t.Fatal("upstream did not fill observation capacity")
				}
				var record proxy.FeedbackRequest
				select {
				case record = <-store.history:
				case <-time.After(3 * time.Second):
					t.Fatal("observation admission blocked durable completion")
				}
				require.Equal(t, tc.model, record.ServedModel)
				require.Equal(t, tc.provider, record.ServedProvider)
				require.Eventually(t, func() bool {
					if !stream && tc.name == "translated_responses" {
						return strings.Contains(out.text(), `"status":"completed"`)
					}
					return strings.Contains(out.text(), tc.terminal)
				}, time.Second, time.Millisecond)
				select {
				case <-done:
					t.Fatal("observation admission no longer applies backpressure")
				default:
				}
				cancel()
				unblock()
				select {
				case err := <-done:
					require.NoError(t, err, "cancellation after terminal release must not undo completion")
				case <-time.After(3 * time.Second):
					t.Fatal("observation admission did not resume")
				}
				select {
				case row := <-store.telemetry:
					require.Equal(t, record.RequestID, row.RequestID)
					require.Equal(t, tc.model, row.DecisionModel)
				case <-time.After(time.Second):
					t.Fatal("telemetry was lost")
				}
				if tc.name != "usage_bypass" {
					select {
					case attempt := <-store.attempts:
						require.Equal(t, inference.AttemptOutcomeServed, attempt.Event.Outcome)
						require.Equal(t, tc.provider, attempt.Event.Target.Provider)
					case <-time.After(time.Second):
						t.Fatal("attempt observation was lost")
					}
				}
				require.Empty(t, store.history, "completion must commit once")
				require.Len(t, provider.proxyBodies, 1)
			})
		}
	}
}
