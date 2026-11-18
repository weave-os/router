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

func rescueVerdict(model string) string {
	return `{"id":"msg_v","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"<block>no</block>"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4}}`
}

type modelScriptedProvider struct {
	replies map[string]string
	models  []string
	bodies  [][]byte
	betas   []string
}

func (*modelScriptedProvider) SupportsSubscriptions() bool { return true }

func (p *modelScriptedProvider) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	model := gjson.GetBytes(prep.Body, "model").String()
	p.models = append(p.models, model)
	p.bodies = append(p.bodies, append([]byte(nil), prep.Body...))
	p.betas = append(p.betas, prep.Headers.Get("anthropic-beta"))
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
	svc := &Service{clients: dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: p})}
	svc.anthropicServerSideFallback = true
	svc.cyberRefusalRetry = retry
	svc.cyberRefusalFallbackModel = rescueReviewerModel
	sink := newBypassCaptureTelemetry()
	svc.telemetry = sink

	if stream {
		body = strings.Replace(body, `"max_tokens":10240,`, `"max_tokens":10240,"stream":true,`, 1)
	}
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+classifierTestSubToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", classifierClientBeta)
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, uuid.New().String())

	require.NoError(t, svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(), false, time.Now(), "req-rescue", "ext-rescue", turntype.Classifier, reasonClassifierPassthrough, req, rec))
	return rec, sink
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
		rescueReviewerModel: rescueVerdict(rescueReviewerModel),
	}}

	rec, sink := runRescueBypass(t, p, true, false)

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel}, p.models)
	assert.JSONEq(t, rescueVerdict(rescueReviewerModel), rec.Body.String())
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
		rescueReviewerModel: rescueVerdict(rescueReviewerModel),
	}}
	body := strings.Replace(classifierStage2Body(""), `run node a.js`, strings.Repeat("x", 4_000_000), 1)

	rec, _ := runRescueBypassBody(t, p, true, false, body)

	assert.Equal(t, []string{rescueRefusingModel, rescueReviewerModel}, p.models)
	assert.JSONEq(t, rescueVerdict(rescueReviewerModel), rec.Body.String())
	assert.Contains(t, p.betas[1], "context-1m")
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
				rescueReviewerModel: rescueVerdict(rescueReviewerModel),
			}}

			rec, _ := runRescueBypass(t, p, tc.retry, tc.stream)

			assert.Equal(t, []string{rescueRefusingModel}, p.models)
			if !tc.stream {
				assert.Contains(t, rec.Body.String(), `"stop_reason":"refusal"`)
			}
		})
	}
}
