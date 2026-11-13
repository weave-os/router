package proxy

import (
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/require"
)

func TestServedGroupTelemetryKeepsFreshRecommendationSeparate(t *testing.T) {
	params := InsertTelemetryParams{SelectionTrace: []byte(`{"selected_group":"maximum","selected_arm":"sol"}`)}
	routed := turnLoopResult{Decision: router.Decision{Model: "luna"}, PinModel: "luna", PinPolicyGroup: "low", StickyHit: true}
	applyServedGroupTelemetry(t.Context(), &params, routed, router.Decision{Model: "luna"})
	require.JSONEq(t, `{"selected_group":"maximum","selected_arm":"sol","served_group":"low"}`, string(params.SelectionTrace))

	applyServedGroupTelemetry(t.Context(), &params, routed, router.Decision{Model: "astra"})
	require.JSONEq(t, `{"selected_group":"maximum","selected_arm":"sol","served_group":""}`, string(params.SelectionTrace), "a rescue model must not inherit the original selection group")

	routed.StickyHit = false
	applyServedGroupTelemetry(t.Context(), &params, routed, router.Decision{Model: "luna"})
	require.JSONEq(t, `{"selected_group":"maximum","selected_arm":"sol","served_group":""}`, string(params.SelectionTrace), "an unclassified fresh decision must not inherit historical pin evidence")

	routed.StickyHit = true
	routed.Decision.Reason = translate.ReasonUserForceModel
	applyServedGroupTelemetry(t.Context(), &params, routed, router.Decision{Model: "luna"})
	require.JSONEq(t, `{"selected_group":"maximum","selected_arm":"sol","served_group":""}`, string(params.SelectionTrace), "explicit force-model decisions have no automatic complexity selection")
}

func TestLatestToolOutcomeCoverageDistinguishesEmptyAndUnsupported(t *testing.T) {
	anthropic, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(latestToolCallCountsJSON(anthropic)))

	openai, err := translate.ParseOpenAI([]byte(`{"model":"gpt-6-sol","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.Nil(t, latestToolCallCountsJSON(openai), "missing tool-error semantics must not appear as zero errors")
}
