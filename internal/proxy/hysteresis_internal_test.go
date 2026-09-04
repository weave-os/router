package proxy

import (
	"testing"

	"weave-os/router/internal/router"
)

func hysteresisDecision(model string, armScores map[string]float32) router.Decision {
	return router.Decision{
		Model: model,
		Metadata: &router.RoutingMetadata{
			SelectedRosterArmID: "anthropic/" + model + ":high",
			ArmScores:           armScores,
		},
	}
}

func TestEffortHysteresisHoldsSmallGapOnSameModel(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:low":  4.0,
		"anthropic/claude-opus-5:high": 4.5,
	})

	got := effortHysteresisHold(fresh, "claude-opus-5:low", "claude-opus-5", "high")

	if got != "low" {
		t.Fatalf("expected incumbent effort held, got %q", got)
	}
}

func TestEffortHysteresisAllowsGapAboveThreshold(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:low":  4.0,
		"anthropic/claude-opus-5:high": 6.0,
	})

	got := effortHysteresisHold(fresh, "claude-opus-5:low", "claude-opus-5", "high")

	if got != "" {
		t.Fatalf("expected switch allowed, got %q", got)
	}
}

func TestEffortHysteresisAllowsCrossModelSwitch(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:low":  4.0,
		"anthropic/claude-opus-5:high": 4.5,
	})

	got := effortHysteresisHold(fresh, "claude-fable-5:low", "claude-opus-5", "high")

	if got != "" {
		t.Fatalf("expected cross-model switch allowed, got %q", got)
	}
}

func TestEffortHysteresisAllowsStayOnModelTheFreshArmDoesNotDescribe(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:low":  4.0,
		"anthropic/claude-opus-5:high": 4.5,
	})

	got := effortHysteresisHold(fresh, "claude-fable-5:low", "claude-fable-5", "high")

	if got != "" {
		t.Fatalf("expected pass-through when the stay model differs from the scored arm, got %q", got)
	}
}

func TestEffortHysteresisPassesThroughWithoutArmScores(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", nil)

	got := effortHysteresisHold(fresh, "claude-opus-5:low", "claude-opus-5", "high")

	if got != "" {
		t.Fatalf("expected pass-through on a pre-B1 sidecar, got %q", got)
	}
}

func TestEffortHysteresisPassesThroughOnBarePin(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:low":  4.0,
		"anthropic/claude-opus-5:high": 4.5,
	})

	got := effortHysteresisHold(fresh, "claude-opus-5", "claude-opus-5", "high")

	if got != "" {
		t.Fatalf("expected pre-effort bare pin to pass through, got %q", got)
	}
}

func TestEffortHysteresisPassesThroughWhenIncumbentArmUnscored(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:high": 4.5,
	})

	got := effortHysteresisHold(fresh, "claude-opus-5:low", "claude-opus-5", "high")

	if got != "" {
		t.Fatalf("expected pass-through when the incumbent arm has no score, got %q", got)
	}
}

func TestEffortHysteresisHoldsWhenChallengerScoresWorse(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:low":  5.0,
		"anthropic/claude-opus-5:high": 1.0,
	})

	got := effortHysteresisHold(fresh, "claude-opus-5:low", "claude-opus-5", "high")

	if got != "low" {
		t.Fatalf("expected incumbent held against a worse challenger, got %q", got)
	}
}

func TestEffortHysteresisPassesThroughOnUnchangedEffort(t *testing.T) {
	fresh := hysteresisDecision("claude-opus-5", map[string]float32{
		"anthropic/claude-opus-5:low": 4.0,
	})

	got := effortHysteresisHold(fresh, "claude-opus-5:low", "claude-opus-5", "low")

	if got != "" {
		t.Fatalf("expected no hold when effort is unchanged, got %q", got)
	}
}
