package continuity_test

import (
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
)

func TestSettleChoosesTheHandoverOnlyForAPinnedSwitch(t *testing.T) {
	stay := planner.Decision{Outcome: planner.OutcomeStay, Reason: "ev"}
	switching := planner.Decision{Outcome: planner.OutcomeSwitch, Reason: "ev"}

	assert.Equal(t, continuity.Outcome{Stay: true, PinTier: "postgres_stay_ev"}, continuity.Settle(stay, true, false))
	assert.Equal(t, continuity.Outcome{}, continuity.Settle(stay, false, false))
	assert.Equal(t, continuity.Outcome{PinTier: "switch_ev", Handover: continuity.HandoverSummarize}, continuity.Settle(switching, true, false))
	assert.Equal(t, continuity.Outcome{PinTier: "switch_ev", Handover: continuity.HandoverSkippedTrimmedHistory}, continuity.Settle(switching, true, true))
}

func TestJudgeReanchorServesAnExpiredPinNoWeakerThanTheFreshPick(t *testing.T) {
	pin := sessionpin.Pin{Provider: "anthropic", Model: string(catalog.ModelIDClaudeOpus47)}
	fresh := router.Decision{Provider: "anthropic", Model: string(catalog.ModelIDClaudeHaiku45)}
	available := map[string]struct{}{pin.Model: {}}

	assert.Equal(t, continuity.ReanchorServe, continuity.JudgeReanchor(pin, fresh, router.Request{}, available))
	assert.Equal(t, continuity.ReanchorNone, continuity.JudgeReanchor(pin, fresh, router.Request{}, nil))
	assert.Equal(t, continuity.ReanchorNone, continuity.JudgeReanchor(pin, fresh, router.Request{EnabledProviders: map[string]struct{}{"openai": {}}}, available))
}
