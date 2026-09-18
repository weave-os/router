package flags

import (
	"context"
	"fmt"
)

// EscalationClassifier identifies an independently selectable escalation policy.
type EscalationClassifier string

const (
	EscalationClassifierNone       EscalationClassifier = "none"
	EscalationClassifierXGB        EscalationClassifier = "xgb"
	EscalationClassifierSwitchyard EscalationClassifier = "switchyard_llm_v1"
)

// EscalationConfig is one request's effective, immutable classifier selection.
type EscalationConfig struct {
	Active   EscalationClassifier
	Shadow   EscalationClassifier
	Cadence  int
	Epoch    int
	Explicit bool
}

// EscalationFromContext preserves legacy behavior until an active selector is set.
func EscalationFromContext(ctx context.Context) EscalationConfig {
	overrides, _ := OverridesFromContext(ctx)
	active, explicit := overrides.Strings[KeyEscalationActiveClassifier]
	config := EscalationConfig{Active: EscalationClassifierNone, Shadow: EscalationClassifierNone, Cadence: IntOr(ctx, KeyEscalationCadence, 3), Epoch: IntOr(ctx, KeyEscalationEpoch, 0), Explicit: explicit}
	if explicit {
		config.Active = EscalationClassifier(active)
		config.Shadow = EscalationClassifier(StringOr(ctx, KeyEscalationShadowClassifier, string(EscalationClassifierNone)))
		return config
	}
	config.Epoch = IntOr(ctx, KeyEscalationXGBoostEpoch, 0)
	if BoolOr(ctx, KeyEscalationXGBoostEnabled, false) {
		config.Active = EscalationClassifierXGB
	} else if BoolOr(ctx, KeyEscalationXGBoostShadowEnabled, false) {
		config.Shadow = EscalationClassifierXGB
	}
	return config
}

func validEscalationClassifier(classifier EscalationClassifier) bool {
	switch classifier {
	case EscalationClassifierNone, EscalationClassifierXGB, EscalationClassifierSwitchyard:
		return true
	default:
		return false
	}
}

func validateEscalationOverrides(overrides Overrides) error {
	for _, key := range []Key{KeyEscalationActiveClassifier, KeyEscalationShadowClassifier} {
		if value, found := overrides.Strings[key]; found && !validEscalationClassifier(EscalationClassifier(value)) {
			return fmt.Errorf("%w: unsupported escalation classifier for %q", ErrInvalidValue, key)
		}
	}
	if cadence, found := overrides.Ints[KeyEscalationCadence]; found && (cadence < 3 || cadence > 5) {
		return fmt.Errorf("%w: escalation cadence must be 3, 4, or 5", ErrInvalidValue)
	}
	if epoch, found := overrides.Ints[KeyEscalationEpoch]; found && epoch < 0 {
		return fmt.Errorf("%w: escalation epoch must be nonnegative", ErrInvalidValue)
	}
	active, explicit := overrides.Strings[KeyEscalationActiveClassifier]
	shadow, hasShadow := overrides.Strings[KeyEscalationShadowClassifier]
	if hasShadow && !explicit {
		return fmt.Errorf("%w: shadow classifier requires an explicit active selector", ErrInvalidValue)
	}
	if explicit && hasShadow && active == shadow && EscalationClassifier(active) != EscalationClassifierNone {
		return fmt.Errorf("%w: active and shadow classifiers must differ", ErrInvalidValue)
	}
	return nil
}
