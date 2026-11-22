package conversation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func TestListModelsMessage_MarksRefusedRowsAndCountsForceable(t *testing.T) {
	msg := ListModelsMessage([]ForceableModel{
		{ID: "claude-opus-5-5", Provider: providers.ProviderAnthropic, Shorthands: []string{"opus", "claude"}},
		{ID: "gpt-6.1-sol", Provider: providers.ProviderOpenAI, Refused: true, RefusalReason: "excluded", Shorthands: []string{"sol"}},
	}, "$", translate.FormatOpenAI)

	assert.Contains(t, msg, "[x] claude-opus-5-5 (opus, claude)")
	assert.Contains(t, msg, "[ ] gpt-6.1-sol (sol)")
	assert.Contains(t, msg, "1 of 2 can be forced here")
	assert.Contains(t, msg, "refused by this installation")
}

func TestListModelsMessage_UsesTheClientsOwnSigil(t *testing.T) {
	models := []ForceableModel{{ID: "claude-opus-5-5", Provider: providers.ProviderAnthropic}}

	codex := ListModelsMessage(models, "$", translate.FormatOpenAI)
	assert.Contains(t, codex, "`$force-model <id or shorthand>`")
	assert.NotContains(t, codex, "refused by this installation")

	claude := ListModelsMessage(models, "/", translate.FormatAnthropic)
	assert.Contains(t, claude, "`/force-model <id or shorthand>`")
	assert.Contains(t, claude, routingMarkerPrefix)
}

func TestListModelsMessage_CapsShorthandsPerRow(t *testing.T) {
	msg := ListModelsMessage([]ForceableModel{{
		ID: "claude-opus-5-5", Provider: providers.ProviderAnthropic,
		Shorthands: []string{"opus", "claude", "opus55", "opus-5.5", "opus-5-5"},
	}}, "$", translate.FormatOpenAI)

	assert.Contains(t, msg, "(opus, claude, opus55, opus-5.5)")
	assert.NotContains(t, msg, "opus-5-5)")
}
