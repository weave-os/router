package proxy

import (
	"context"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
)

func TestResolveEffort_SourcesAndClamping(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, nil, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).WithEffortEscalation(true)

	for _, tc := range []struct {
		name         string
		model        string
		armEffort    string
		knob         string
		escalate     bool
		wantSelected string
		wantSent     string
		wantSource   string
		wantMismatch bool
	}{
		{
			name:         "no arm or knob falls to the per-model default",
			model:        "gpt-5.6-luna",
			wantSelected: "low",
			wantSent:     "low",
			wantSource:   effortSourceModelPolicy,
		},
		{
			name:  "no arm, no knob, no per-model default",
			model: "claude-opus-5",
		},
		{
			name:         "arm level the target accepts",
			model:        "claude-opus-5",
			armEffort:    "xhigh",
			wantSelected: "xhigh",
			wantSent:     "xhigh",
			wantSource:   effortSourceArm,
		},
		{
			name:         "arm level above the target's menu is sent clamped",
			model:        "gpt-5.5",
			armEffort:    "xhigh",
			wantSelected: "xhigh",
			wantSent:     "high",
			wantSource:   effortSourceArm,
			wantMismatch: true,
		},
		{
			name:         "gpt-5.6 serves xhigh unclamped",
			model:        "gpt-5.6-luna",
			armEffort:    "xhigh",
			wantSelected: "xhigh",
			wantSent:     "xhigh",
			wantSource:   effortSourceArm,
		},
		{
			name:         "user knob outranks the arm",
			model:        "claude-opus-5",
			armEffort:    "xhigh",
			knob:         "low",
			wantSelected: "low",
			wantSent:     "low",
			wantSource:   effortSourceUser,
			wantMismatch: true,
		},
		{
			name:         "escalation raises an arm below it",
			model:        "gpt-5.6-luna",
			armEffort:    "low",
			escalate:     true,
			wantSelected: "high",
			wantSent:     "high",
			wantSource:   effortSourceEscalation,
			wantMismatch: true,
		},
		{
			name:         "escalation never downgrades a richer arm",
			model:        "gpt-5.6-luna",
			armEffort:    "xhigh",
			escalate:     true,
			wantSelected: "xhigh",
			wantSent:     "xhigh",
			wantSource:   effortSourceArm,
		},
		{
			name:         "grok escalation never downgrades a richer arm",
			model:        "grok-4.6",
			armEffort:    "xhigh",
			escalate:     true,
			wantSelected: "xhigh",
			wantSent:     "xhigh",
			wantSource:   effortSourceArm,
		},
		{
			name:         "max on an xhigh-ceiling menu serves xhigh",
			model:        "gpt-5.6-luna",
			armEffort:    "max",
			wantSelected: "max",
			wantSent:     "xhigh",
			wantSource:   effortSourceArm,
			wantMismatch: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.knob != "" {
				ctx = router.WithRoutingKnobs(ctx, &router.Overrides{ForceEffort: tc.knob})
			}
			decision := router.Decision{Model: tc.model, Effort: tc.armEffort}

			got := svc.resolveEffort(ctx, decision, router.Lookup(tc.model), tc.escalate)

			assert.Equal(t, tc.wantSelected, got.Selected)
			assert.Equal(t, tc.wantSent, got.Sent)
			assert.Equal(t, tc.wantSource, got.Source)
			assert.Equal(t, router.CanonicalizeEffort(tc.armEffort), got.Arm)
			assert.Equal(t, tc.wantMismatch, got.Mismatch())
		})
	}
}

// The arm's pre-cap level goes on ForceEffort so the per-model cap still
// applies downstream; the wire level is what the emit paths resolve.
func TestEffortResolution_Apply(t *testing.T) {
	caps := router.Lookup("gpt-5.5")
	opts := translate.EmitOptions{Capabilities: caps}

	effortResolutionFor(caps, "xhigh", "xhigh", effortSourceArm).apply(&opts)
	assert.Equal(t, "xhigh", opts.ForceEffort)
	assert.Equal(t, "max", opts.ForceReasoningEffort)

	effortResolution{}.apply(&opts)
	assert.Empty(t, opts.ForceEffort)
	assert.Empty(t, opts.ForceReasoningEffort)
}

// A rescue candidate never served the failed model's level, so keeping it
// would report an identity the sibling never served.
func TestSiblingDecisionFor_DropsEffort(t *testing.T) {
	failed := router.Decision{
		Provider: providers.ProviderOpenAI,
		Model:    "gpt-5.6-luna",
		Effort:   "xhigh",
		Metadata: &router.RoutingMetadata{SelectedArmID: "gpt-5.6-luna:xhigh"},
	}

	out := siblingDecisionFor(failed, "gpt-5.6-sol", providers.ProviderOpenAI)

	assert.Empty(t, out.Effort)
	assert.Equal(t, "gpt-5.6-sol", out.ServedIdentity())
}
