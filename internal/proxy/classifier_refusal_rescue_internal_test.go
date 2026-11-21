package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	rescueRefusingModel = "claude-sonnet-5-5"
	rescueReviewerModel = "claude-sonnet-5"
)

func rescueRefusal(model string) string {
	return `{"id":"msg_r","type":"message","role":"assistant","model":"` + model + `","content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"reasoning_extraction"},"usage":{"input_tokens":10,"output_tokens":0}}`
}

func rescueVerdict() string {
	return `{"id":"msg_v","type":"message","role":"assistant","model":"` + rescueReviewerModel + `","content":[{"type":"text","text":"<block>no</block>"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4}}`
}

type modelScriptedProvider struct {
	replies     map[string]string
	unavailable map[string]bool
	models      []string
	bodies      [][]byte
	betas       []string
}

func (*modelScriptedProvider) SupportsSubscriptions() bool { return true }

func (p *modelScriptedProvider) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	model := gjson.GetBytes(prep.Body, "model").String()
	p.models = append(p.models, model)
	p.bodies = append(p.bodies, append([]byte(nil), prep.Body...))
	p.betas = append(p.betas, prep.Headers.Get("anthropic-beta"))
	if p.unavailable[model] {
		return &providers.UpstreamErrorResponse{Status: http.StatusNotFound, Body: []byte(`{"type":"error","error":{"type":"not_found_error","message":"model: ` + model + `"}}`)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, p.replies[model])
	return nil
}

func (*modelScriptedProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func runRescueBypass(t *testing.T, p *modelScriptedProvider, retry bool, stream bool) (*httptest.ResponseRecorder, *bypassCaptureTelemetry) {
	t.Helper()
	return runRescueBypassBody(t, p, retry, stream, classifierStage2Body(""))
}

func runRescueBypassBody(t *testing.T, p *modelScriptedProvider, retry bool, stream bool, body string) (*httptest.ResponseRecorder, *bypassCaptureTelemetry) {
	t.Helper()
	return runRescueBypassOn(t, p, retry, stream, body, rescueReviewerModel)
}

func runRescueBypassOn(t *testing.T, p *modelScriptedProvider, retry bool, stream bool, body, fallbackModel string) (*httptest.ResponseRecorder, *bypassCaptureTelemetry) {
	t.Helper()
	svc, sink := rescueService(p, retry, fallbackModel)
	if stream {
		body = strings.Replace(body, `"max_tokens":10240,`, `"max_tokens":10240,"stream":true,`, 1)
	}
	return runRescueWith(t, svc, body), sink
}

func rescueService(p *modelScriptedProvider, retry bool, fallbackModel string) (*Service, *bypassCaptureTelemetry) {
	svc := &Service{hmmServing: hmmPorts(), clients: dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: p})}
	svc.anthropicServerSideFallback = true
	svc.cyberRefusalRetry = retry
	svc.cyberRefusalFallbackModel = fallbackModel
	sink := newBypassCaptureTelemetry()
	svc.telemetry = sink
	return svc, sink
}

func runRescueWith(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+classifierTestSubToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", classifierClientBeta)
	ctx := context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, uuid.New().String())

	require.NoError(t, svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(), false, time.Now(), "req-rescue", "ext-rescue", turntype.Classifier, reasonClassifierPassthrough, req, rec))
	return rec
}

// Some Claude subscriptions don't serve the reviewer model (Anthropic answers
// 404 not_found_error "model: claude-sonnet-5"). The re-serve then moves on to
// the next candidate the subscription can serve instead of returning the
// refusal.
func TestBypass_ClassifierRefusal_TriesNextModelWhenSubscriptionLacksReviewer(t *testing.T) {
	const nextModel = "claude-opus-5-5"
	p := &modelScriptedProvider{
		replies:     map[string]string{rescueRefusingModel: rescueRefusal(rescueRefusingModel), nextModel: rescueVerdict()},
		unavailable: map[string]bool{rescueReviewerModel: true},
	}
	svc, sink := rescueService(p, true, rescueReviewerModel)
	rescueServeOn(t, svc, sink, p, nextModel)

	rec := runRescueWith(t, svc, classifierStage2Body(""))

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel, nextModel}, p.models)
	assert.JSONEq(t, rescueVerdict(), rec.Body.String())
	assert.Equal(t, nextModel, rec.Header().Get(HeaderRouterModel))
	assert.Equal(t, nextModel, rescueTelemetryRow(t, sink).DecisionModel)
}

// Once a subscription has rejected a candidate, later re-serves skip it.
func TestBypass_ClassifierRefusal_SkipsModelSubscriptionRejected(t *testing.T) {
	const nextModel = "claude-opus-5-5"
	p := &modelScriptedProvider{
		replies:     map[string]string{rescueRefusingModel: rescueRefusal(rescueRefusingModel), nextModel: rescueVerdict()},
		unavailable: map[string]bool{rescueReviewerModel: true},
	}
	svc, sink := rescueService(p, true, rescueReviewerModel)
	rescueServeOn(t, svc, sink, p, nextModel)
	runRescueWith(t, svc, classifierStage2Body(""))
	p.models = nil

	rec := runRescueWith(t, svc, classifierStage2Body(""))

	assert.Equal(t, []string{rescueRefusingModel, nextModel}, p.models)
	assert.JSONEq(t, rescueVerdict(), rec.Body.String())
}

// rescueServeOn serves one classifier check on model for the test
// subscription, as an earlier turn in the session would.
func rescueServeOn(t *testing.T, svc *Service, sink *bypassCaptureTelemetry, p *modelScriptedProvider, model string) {
	t.Helper()
	body, err := sjson.Set(classifierStage2Body(""), "model", model)
	require.NoError(t, err)
	runRescueWith(t, svc, body)
	rescueTelemetryRow(t, sink)
	sink.mu.Lock()
	sink.rows = nil
	sink.mu.Unlock()
	p.models = nil
}

// Rescue candidates come from what this subscription has actually served, so
// a plan that lacks both default reviewers is still rescued on a model it can
// run, strongest first.
func TestBypass_ClassifierRefusal_RescuesOnStrongestModelSubscriptionServed(t *testing.T) {
	const lowModel, servedModel = "claude-haiku-5-5", "claude-opus-5"
	p := &modelScriptedProvider{
		replies: map[string]string{
			rescueRefusingModel: rescueRefusal(rescueRefusingModel),
			lowModel:            rescueVerdict(),
			servedModel:         rescueVerdict(),
		},
		unavailable: map[string]bool{rescueReviewerModel: true, "claude-opus-5-5": true},
	}
	svc, sink := rescueService(p, true, rescueReviewerModel)
	rescueServeOn(t, svc, sink, p, lowModel)
	rescueServeOn(t, svc, sink, p, servedModel)

	rec := runRescueWith(t, svc, classifierStage2Body(""))

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel, servedModel}, p.models)
	assert.JSONEq(t, rescueVerdict(), rec.Body.String())
	assert.Equal(t, servedModel, rec.Header().Get(HeaderRouterModel))
}

// An Enterprise admin can restrict which models a seat may use, so a model
// the seat hasn't served would most likely 404 and waste the retry.
func TestBypass_ClassifierRefusal_DoesNotTryModelsSubscriptionNeverServed(t *testing.T) {
	p := &modelScriptedProvider{
		replies:     map[string]string{rescueRefusingModel: rescueRefusal(rescueRefusingModel), "claude-opus-5-5": rescueVerdict()},
		unavailable: map[string]bool{rescueReviewerModel: true},
	}
	svc, _ := rescueService(p, true, rescueReviewerModel)

	rec := runRescueWith(t, svc, classifierStage2Body(""))

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel}, p.models)
	assert.JSONEq(t, rescueRefusal(rescueRefusingModel), rec.Body.String())
}

func rescueTelemetryRow(t *testing.T, sink *bypassCaptureTelemetry) InsertTelemetryParams {
	t.Helper()
	select {
	case <-sink.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("classifier bypass never persisted a telemetry row")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.rows, 1)
	return sink.rows[0]
}

// A safety check Anthropic refuses without re-serving it (e.g.
// reasoning_extraction, which has no recommended fallback) makes Claude Code
// deny the tool call. The router re-serves that one check on the reviewer
// fallback model before anything reaches the client, changing only "model"
// and dropping the fallback opt-in the reviewer model can't accept.
func TestBypass_ClassifierRefusal_ReservedOnReviewerModel(t *testing.T) {
	p := &modelScriptedProvider{replies: map[string]string{
		rescueRefusingModel: rescueRefusal(rescueRefusingModel),
		rescueReviewerModel: rescueVerdict(),
	}}

	rec, sink := runRescueBypass(t, p, true, false)

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel}, p.models)
	assert.JSONEq(t, rescueVerdict(), rec.Body.String())
	assert.Equal(t, rescueReviewerModel, rec.Header().Get(HeaderRouterModel))

	require.Len(t, p.bodies, 2)
	assert.False(t, gjson.GetBytes(p.bodies[1], "fallbacks").Exists())
	assert.Equal(t, classifierClientBeta, p.betas[1])
	first, err := sjson.DeleteBytes(p.bodies[0], "fallbacks")
	require.NoError(t, err)
	second, err := sjson.SetBytes(p.bodies[1], "model", rescueRefusingModel)
	require.NoError(t, err)
	assert.JSONEq(t, string(first), string(second))

	row := rescueTelemetryRow(t, sink)
	assert.Equal(t, rescueReviewerModel, row.DecisionModel)
	require.NotNil(t, row.StopReason)
	assert.Equal(t, "end_turn", *row.StopReason)
}

// A check too large for the reviewer model's base window is re-served with
// the extended-context beta that model needs.
func TestBypass_ClassifierRefusal_LargeCheckCarriesExtendedContextBeta(t *testing.T) {
	p := &modelScriptedProvider{replies: map[string]string{
		rescueRefusingModel: rescueRefusal(rescueRefusingModel),
		rescueReviewerModel: rescueVerdict(),
	}}
	body := strings.Replace(classifierStage2Body(""), `run node a.js`, strings.Repeat("x", 4_000_000), 1)

	rec, _ := runRescueBypassBody(t, p, true, false, body)

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel}, p.models)
	assert.JSONEq(t, rescueVerdict(), rec.Body.String())
	assert.Contains(t, p.betas[1], "context-1m")
}

// An effort level the reviewer model can't take is clamped to its highest one.
func TestBypass_ClassifierRefusal_ClampsUnsupportedEffort(t *testing.T) {
	p := &modelScriptedProvider{replies: map[string]string{
		rescueRefusingModel: rescueRefusal(rescueRefusingModel),
		rescueReviewerModel: rescueVerdict(),
	}}
	body := strings.Replace(classifierStage2Body(""), `"max_tokens":10240,`, `"max_tokens":10240,"output_config":{"effort":"xhigh"},`, 1)

	runRescueBypassBody(t, p, true, false, body)

	require.Len(t, p.bodies, 2)
	assert.Equal(t, "xhigh", gjson.GetBytes(p.bodies[0], "output_config.effort").String())
	assert.Equal(t, "max", gjson.GetBytes(p.bodies[1], "output_config.effort").String())
}

// A fallback model that accepts Anthropic's server-side fallback gets both the
// field and its beta, never the field alone.
func TestBypass_ClassifierRefusal_FallbackCapableModelGetsFallbackBeta(t *testing.T) {
	const capableFallback = "claude-opus-5"
	p := &modelScriptedProvider{replies: map[string]string{
		rescueRefusingModel: rescueRefusal(rescueRefusingModel),
		capableFallback:     rescueVerdict(),
	}}

	runRescueBypassOn(t, p, true, false, classifierStage2Body(""), capableFallback)

	require.Len(t, p.bodies, 2)
	assert.Equal(t, "default", gjson.GetBytes(p.bodies[1], "fallbacks").String())
	assert.Contains(t, p.betas[1], "server-side-fallback")
}

// A client's own fallbacks choice survives the re-serve on a model that
// accepts the field.
func TestBypass_ClassifierRefusal_KeepsClientFallbackChoice(t *testing.T) {
	const capableFallback = "claude-opus-5"
	p := &modelScriptedProvider{replies: map[string]string{
		rescueRefusingModel: rescueRefusal(rescueRefusingModel),
		capableFallback:     rescueVerdict(),
	}}
	body := strings.Replace(classifierStage2Body(""), `"max_tokens":10240,`, `"max_tokens":10240,"fallbacks":"none",`, 1)

	runRescueBypassOn(t, p, true, false, body, capableFallback)

	require.Len(t, p.bodies, 2)
	assert.Equal(t, "none", gjson.GetBytes(p.bodies[1], "fallbacks").String())
	assert.Contains(t, p.betas[1], "server-side-fallback")
}

// If the reviewer model refuses too, the client gets the original refusal.
func TestBypass_ClassifierRefusal_FallbackAlsoRefusesReturnsOriginal(t *testing.T) {
	p := &modelScriptedProvider{replies: map[string]string{
		rescueRefusingModel: rescueRefusal(rescueRefusingModel),
		rescueReviewerModel: rescueRefusal(rescueReviewerModel),
	}}

	rec, sink := runRescueBypass(t, p, true, false)

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel}, p.models)
	assert.JSONEq(t, rescueRefusal(rescueRefusingModel), rec.Body.String())
	row := rescueTelemetryRow(t, sink)
	assert.Equal(t, rescueRefusingModel, row.DecisionModel)
	require.NotNil(t, row.StopReason)
	assert.Equal(t, "refusal", *row.StopReason)
}

// With the safety-refusal retry flag off, or on a streamed check, the refusal
// is forwarded untouched after a single dispatch.
func TestBypass_ClassifierRefusal_NotReservedWhenDisabledOrStreaming(t *testing.T) {
	for name, tc := range map[string]struct{ retry, stream bool }{
		"flag_off":  {retry: false},
		"streaming": {retry: true, stream: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := &modelScriptedProvider{replies: map[string]string{
				rescueRefusingModel: rescueRefusal(rescueRefusingModel),
				rescueReviewerModel: rescueVerdict(),
			}}

			rec, _ := runRescueBypass(t, p, tc.retry, tc.stream)

			assert.Equal(t, []string{rescueRefusingModel}, p.models)
			if !tc.stream {
				assert.Contains(t, rec.Body.String(), `"stop_reason":"refusal"`)
			}
		})
	}
}

// A check that turned thinking off is re-served with the reviewer model's own
// off switch, not the one rewritten for the refusing model.
func TestBypass_ClassifierRefusal_ReviewerGetsItsOwnThinkingOff(t *testing.T) {
	p := &modelScriptedProvider{replies: map[string]string{
		rescueRefusingModel: rescueRefusal(rescueRefusingModel),
		rescueReviewerModel: rescueVerdict(),
	}}

	rec, _ := runRescueBypassBody(t, p, true, false, classifierStage2Body(`{"type":"disabled"}`))

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel}, p.models)
	assert.JSONEq(t, rescueVerdict(), rec.Body.String())
	require.Len(t, p.bodies, 2)
	assert.Equal(t, "between_tools", gjson.GetBytes(p.bodies[0], "thinking.type").String())
	assert.JSONEq(t, `{"type":"disabled"}`, gjson.GetBytes(p.bodies[1], "thinking").Raw)
	assert.False(t, gjson.GetBytes(p.bodies[1], "output_config").Exists())
}

func TestBypass_ClassifierRefusal_DoesNotRetryUndatedIDOfRefusingModel(t *testing.T) {
	dated := rescueRefusingModel + "-20261101"
	p := &modelScriptedProvider{replies: map[string]string{dated: rescueRefusal(dated), rescueRefusingModel: rescueVerdict()}}
	svc, _ := rescueService(p, true, rescueRefusingModel)
	body, err := sjson.Set(classifierStage2Body(""), "model", dated)
	require.NoError(t, err)

	rec := runRescueWith(t, svc, body)

	assert.Equal(t, []string{dated}, p.models)
	assert.JSONEq(t, rescueRefusal(dated), rec.Body.String())
}
