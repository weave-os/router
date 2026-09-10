package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/translate"
)

func set(ids ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

// The whole point of answering server-side: the skill's managed-router path
// degrades to a flat catalog and has to say it cannot report what is enabled.
// The exclusions are on the request here, so each row gets marked.
func TestRouterModelsMessage_MarksExcludedRowsOff(t *testing.T) {
	msg := routerModelsMessage(
		set("claude-opus-5", "gpt-5.5"),
		set("gpt-5.5"),
		"$", translate.FormatOpenAI)

	assert.Contains(t, msg, "[x] claude-opus-5")
	assert.Contains(t, msg, "[ ] gpt-5.5")
	assert.Contains(t, msg, "1 of 2 routable here")
}

func TestRouterModelsMessage_UsesTheClientsOwnSigilInTheFooter(t *testing.T) {
	codex := routerModelsMessage(set("claude-opus-5"), nil, "$", translate.FormatOpenAI)
	assert.Contains(t, codex, "$router-models enable")
	assert.NotContains(t, codex, "/router-models enable")

	claude := routerModelsMessage(set("claude-opus-5"), nil, "/", translate.FormatAnthropic)
	assert.Contains(t, claude, "/router-models enable")
	assert.Contains(t, claude, routingMarkerPrefix)
}

func TestRouterModelsMessage_EmptyUniverseSaysSo(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatOpenAI, translate.FormatAnthropic} {
		msg := routerModelsMessage(nil, nil, "$", format)
		assert.Contains(t, msg, "no routable models")
		assert.NotContains(t, msg, "[x]")
	}
}

// Grouping is by the model's primary catalog binding, and both the group
// order and the ids inside a group are sorted so the listing is stable
// between turns rather than reordering with Go's map iteration.
func TestRouterModelsMessage_IsDeterministicallyOrdered(t *testing.T) {
	models := set("claude-opus-5", "claude-haiku-4-5", "gpt-5.5", "gemini-3-pro-preview")
	first := routerModelsMessage(models, nil, "$", translate.FormatOpenAI)
	for range 8 {
		assert.Equal(t, first, routerModelsMessage(models, nil, "$", translate.FormatOpenAI))
	}
}
