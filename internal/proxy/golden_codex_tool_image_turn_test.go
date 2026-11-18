package proxy_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

const goldenCodexLowModel = "gpt-5.6-luna"

const goldenCodexUserTurn = `{"model":"auto","stream":true,` +
	`"tools":[{"type":"function","name":"exec","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}],` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the failing build"}]}]}`

// goldenCodexBuildLog is long enough that a cheaper fresh pick on the image
// turn is EV-positive, so the cost gate would switch a misclassified turn.
var goldenCodexBuildLog = strings.Repeat("compile step ok; ", 400) + "build failed"

var goldenCodexToolLogTurn = strings.Replace(goldenCodexPortableToolTurn, `"output":"build failed"`, `"output":"`+goldenCodexBuildLog+`"`, 1)

var goldenCodexToolImageTurn = strings.TrimSuffix(goldenCodexToolLogTurn, `]}`) + `,` +
	`{"type":"function_call","call_id":"call_2","name":"exec","arguments":"{\"cmd\":\"view_image screenshot.png\"}"},` +
	`{"type":"function_call_output","call_id":"call_2","output":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}]}`

func goldenCodexHMMDecision(model, reason string, confidence float32) router.Decision {
	return router.Decision{Provider: providers.ProviderOpenAI, Model: model, Reason: reason, Metadata: &router.RoutingMetadata{
		Strategy: string(router.StrategyHMMEmbedding), ChosenScore: confidence, PolicyGroup: "golden-group",
	}}
}

func goldenCodexToolImageRequest(body string) goldenRequest {
	return goldenRequest{body: body, headers: goldenCodexSessionHeaders, decorateContext: goldenCodexCtx}
}

// goldenPromptClassifier stands in for the HMM classifier: like the
// production embedding of the user's text, it classifies by prompt, so a turn
// that is re-scored on the same user text keeps the same group.
type goldenPromptClassifier struct {
	rec      *goldenRecorder
	byPrompt map[string]router.Decision
	fallback router.Decision
}

func (c *goldenPromptClassifier) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	d, ok := c.byPrompt[req.PromptText]
	if !ok {
		d = c.fallback
	}
	return (&goldenRouter{decisions: []router.Decision{d}, rec: c.rec}).Route(ctx, req)
}

// goldenCodexToolImageTurnScenarios pin a Codex session whose newest input is
// a tool output carrying an image: the turn answers a tool call, so it keeps
// the session's model instead of being classified as a fresh user prompt.
func goldenCodexToolImageTurnScenarios() []goldenScenario {
	high := goldenCodexHMMDecision(goldenCodexModel, "hmm_policy", 0.9)
	low := goldenCodexHMMDecision(goldenCodexLowModel, "hmm_policy:tool_execution", 0.44)
	return []goldenScenario{{
		name: ingressResponses + "_stream_codex_tool_output_image_keeps_session_model", ingress: ingressResponses,
		upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
			goldenResponsesOK("planned the fix"),
			goldenResponsesOK("read the build log"),
			goldenResponsesOK("looked at the screenshot"),
		}},
		requests: []goldenRequest{
			goldenCodexToolImageRequest(goldenCodexUserTurn),
			goldenCodexToolImageRequest(goldenCodexToolLogTurn),
			goldenCodexToolImageRequest(goldenCodexToolImageTurn),
		},
		configure: func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
			return svc.WithPolicyStrategy(policy.StrategySpec{
				Strategy: router.StrategyHMMEmbedding,
				Router:   &goldenPromptClassifier{rec: h.rec, byPrompt: map[string]router.Decision{"fix the failing build": high}, fallback: low},
			})
		},
	}}
}

func TestCodexToolOutputImageKeepsSessionModel(t *testing.T) {
	t.Parallel()
	sc := goldenCodexToolImageTurnScenarios()[0]
	h := newGoldenHarness(t, sc)
	var turnTypes []string
	for i, req := range sc.requests {
		served := h.serve(t, sc.ingress, req)
		require.Equal(t, http.StatusOK, served.Response.Status, "turn %d response: %+v", i, served.Response.Body)
		require.NotEmpty(t, served.Attempts, "turn %d", i)
		require.Equal(t, goldenCodexModel, served.Attempts[len(served.Attempts)-1].Model, "turn %d left the session's model", i)
		turnTypes = append(turnTypes, goldenTelemetryTurnType(t, served))
	}
	require.Equal(t, []string{"main_loop", "tool_result", "tool_result"}, turnTypes)
}

func goldenTelemetryTurnType(t *testing.T, served goldenRequestTrace) string {
	t.Helper()
	require.Len(t, served.Telemetry, 1)
	row, ok := served.Telemetry[0].(map[string]any)
	require.True(t, ok, "telemetry row %T", served.Telemetry[0])
	turnType, _ := row["TurnType"].(string)
	return turnType
}
