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
	EscalationClassifierLLM        EscalationClassifier = "llm_escalation"
)

// EscalationConfig is one request's effective, immutable classifier selection.
type EscalationConfig struct {
	Active   EscalationClassifier
	Shadow   EscalationClassifier
	Cadence  int
	Epoch    int
	Explicit bool
}

// EscalationWithDeploymentShadow shadows classifier for installations that
// select no classifier explicitly and run none actively; legacy XGB shadow
// yields to it. Only LLM classifiers can be a deployment shadow.
func EscalationWithDeploymentShadow(ctx context.Context, classifier EscalationClassifier) EscalationConfig {
	config := EscalationFromContext(ctx)
	if config.Explicit || config.Active != EscalationClassifierNone || !IsLLMEscalationClassifier(classifier) {
		return config
	}
	config.Shadow = classifier
	config.Epoch = IntOr(ctx, KeyEscalationEpoch, 0)
	return config
}

// EscalationFromContext preserves legacy behavior until an active selector is set.
func EscalationFromContext(ctx context.Context) EscalationConfig {
	overrides, _ := OverridesFromContext(ctx)
	return EscalationFromOverrides(overrides)
}

// EscalationFromOverrides is EscalationFromContext for overrides that are not
// carried by a request context, such as an installation's stored settings.
func EscalationFromOverrides(overrides Overrides) EscalationConfig {
	active, explicit := overrides.Strings[KeyEscalationActiveClassifier]
	config := EscalationConfig{Active: EscalationClassifierNone, Shadow: EscalationClassifierNone, Cadence: overrideOr(overrides.Ints, KeyEscalationCadence, 3), Epoch: overrideOr(overrides.Ints, KeyEscalationEpoch, 0), Explicit: explicit}
	if explicit {
		config.Active = EscalationClassifier(active)
		config.Shadow = EscalationClassifier(overrideOr(overrides.Strings, KeyEscalationShadowClassifier, string(EscalationClassifierNone)))
		return config
	}
	config.Epoch = overrideOr(overrides.Ints, KeyEscalationXGBoostEpoch, 0)
	if overrideOr(overrides.Bools, KeyEscalationXGBoostEnabled, false) {
		config.Active = EscalationClassifierXGB
	} else if overrideOr(overrides.Bools, KeyEscalationXGBoostShadowEnabled, false) {
		config.Shadow = EscalationClassifierXGB
	}
	return config
}

func overrideOr[V any](values map[Key]V, key Key, deploymentDefault V) V {
	if v, found := values[key]; found {
		return v
	}
	return deploymentDefault
}

func validEscalationClassifier(classifier EscalationClassifier) bool {
	switch classifier {
	case EscalationClassifierNone, EscalationClassifierXGB, EscalationClassifierSwitchyard, EscalationClassifierLLM:
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
	if explicit && hasShadow && IsLLMEscalationClassifier(EscalationClassifier(active)) && IsLLMEscalationClassifier(EscalationClassifier(shadow)) {
		return fmt.Errorf("%w: only one LLM escalation classifier may be selected", ErrInvalidValue)
	}
	return nil
}

// IsLLMEscalationClassifier identifies selectors that share the asynchronous judge slot.
func IsLLMEscalationClassifier(classifier EscalationClassifier) bool {
	return classifier == EscalationClassifierSwitchyard || classifier == EscalationClassifierLLM
}
