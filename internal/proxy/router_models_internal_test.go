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
		nil,
		"$", translate.FormatOpenAI)

	assert.Contains(t, msg, "[x] claude-opus-5")
	assert.Contains(t, msg, "[ ] gpt-5.5")
	assert.Contains(t, msg, "1 of 2 routable here")
}

func TestRouterModelsMessage_UsesTheClientsOwnSigilInTheFooter(t *testing.T) {
	codex := routerModelsMessage(set("claude-opus-5"), nil, nil, "$", translate.FormatOpenAI)
	assert.Contains(t, codex, "$router-models enable")
	assert.NotContains(t, codex, "/router-models enable")

	claude := routerModelsMessage(set("claude-opus-5"), nil, nil, "/", translate.FormatAnthropic)
	assert.Contains(t, claude, "/router-models enable")
	assert.Contains(t, claude, routingMarkerPrefix)
}

func TestRouterModelsMessage_EmptyUniverseSaysSo(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatOpenAI, translate.FormatAnthropic} {
		msg := routerModelsMessage(nil, nil, nil, "$", format)
		assert.Contains(t, msg, "no routable models")
		assert.NotContains(t, msg, "[x]")
	}
}

// Grouping is by the model's primary catalog binding, and both the group
// order and the ids inside a group are sorted so the listing is stable
// between turns rather than reordering with Go's map iteration.
func TestRouterModelsMessage_IsDeterministicallyOrdered(t *testing.T) {
	models := set("claude-opus-5", "claude-haiku-4-5", "gpt-5.5", "gemini-3-pro-preview")
	first := routerModelsMessage(models, nil, nil, "$", translate.FormatOpenAI)
	for range 8 {
		assert.Equal(t, first, routerModelsMessage(models, nil, nil, "$", translate.FormatOpenAI))
	}
}

// A deployment-wide automatic exclusion is not an org exclusion: the scorer
// will not pick the model, but an explicit force still serves it. Collapsing
// the two into [x]/[ ] would have claimed one or the other.
func TestRouterModelsMessage_AutomaticExclusionGetsItsOwnMark(t *testing.T) {
	msg := routerModelsMessage(
		set("claude-opus-5", "gpt-5.5", "gemini-3-pro-preview"),
		set("gemini-3-pro-preview"),
		set("gpt-5.5"),
		"$", translate.FormatOpenAI)

	assert.Contains(t, msg, "[x] claude-opus-5")
	assert.Contains(t, msg, "[-] gpt-5.5", "automatic exclusion is force-only, not off")
	assert.Contains(t, msg, "[ ] gemini-3-pro-preview", "an org exclusion is off outright")
	// Only the freely-routable one counts as enabled.
	assert.Contains(t, msg, "1 of 3 routable here")
	assert.Contains(t, msg, "$force-model still serves it")
}

// The legend is noise on the overwhelmingly common listing, so it only
// appears when something is actually in that state.
func TestRouterModelsMessage_NoAutomaticExclusionsMeansNoLegend(t *testing.T) {
	msg := routerModelsMessage(set("claude-opus-5"), nil, nil, "$", translate.FormatOpenAI)
	assert.NotContains(t, msg, "[-]")
	assert.NotContains(t, msg, "still serves it")
}
