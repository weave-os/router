package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

type Key string

type Kind string

const (
	KindBool   Kind = "bool"
	KindInt    Kind = "int"
	KindFloat  Kind = "float"
	KindString Kind = "string"
)

type AuthoritativeUpgradePolicy string

const (
	AuthoritativeUpgradePolicyScore AuthoritativeUpgradePolicy = "score"
	AuthoritativeUpgradePolicyOff   AuthoritativeUpgradePolicy = "off"
)

func ParseAuthoritativeUpgradePolicy(raw string) (AuthoritativeUpgradePolicy, error) {
	policy := AuthoritativeUpgradePolicy(raw)
	switch policy {
	case AuthoritativeUpgradePolicyScore, AuthoritativeUpgradePolicyOff:
		return policy, nil
	default:
		return "", fmt.Errorf("authoritative upgrade policy must be score or off, got %q", raw)
	}
}

type EarlyStopRedoMode string

const (
	EarlyStopRedoOff    EarlyStopRedoMode = "off"
	EarlyStopRedoShadow EarlyStopRedoMode = "shadow"
	EarlyStopRedoOn     EarlyStopRedoMode = "on"
)

func ParseEarlyStopRedoMode(raw string) (EarlyStopRedoMode, error) {
	mode := EarlyStopRedoMode(raw)
	switch mode {
	case EarlyStopRedoOff, EarlyStopRedoShadow, EarlyStopRedoOn:
		return mode, nil
	default:
		return "", fmt.Errorf("early stop redo mode must be off, shadow, or on, got %q", raw)
	}
}

const (
	KeyEscalationActiveClassifier           Key = "escalation_active_classifier"
	KeyEscalationShadowClassifier           Key = "escalation_shadow_classifier"
	KeyEscalationCadence                    Key = "escalation_cadence"
	KeyEscalationEpoch                      Key = "escalation_epoch"
	KeyEscalationXGBoostEnabled             Key = "escalation_xgb_enabled"
	KeyEscalationXGBoostShadowEnabled       Key = "escalation_xgb_shadow_enabled"
	KeyEscalationXGBoostShadowMarkerEnabled Key = "escalation_xgb_shadow_marker_enabled"
	KeyEscalationXGBoostEpoch               Key = "escalation_xgb_epoch"
	KeyStruggleShadowEnabled                Key = "struggle_shadow_enabled"
	KeySpiralShadowEnabled                  Key = "spiral_shadow_enabled"
	KeyTurnSignalCapture                    Key = "turn_signal_capture_enabled"
	KeyLoopEscalationEnabled                Key = "loop_escalation_enabled"
	KeyLoopEscalationHoldoutPct             Key = "loop_escalation_holdout_pct"
	KeyTextRepetitionBreak                  Key = "text_repetition_break_enabled"
	KeyPlannerEnabled                       Key = "planner_enabled"
	KeyScoreToolResultTurns                 Key = "score_tool_result_turns"
	KeyHMMToolResultHold                    Key = "hmm_tool_result_hold"
	KeyPrefixTrimFreeSwitch                 Key = "prefix_trim_free_switch"
	KeyAuthoritativeUpgradeGate             Key = "authoritative_upgrade_gate"
	KeyAuthoritativeUpgradePolicy           Key = "authoritative_upgrade_policy"
	KeyAuthoritativeUpgradeVotes            Key = "authoritative_upgrade_votes"
	KeyAuthoritativeDowngradeGate           Key = "authoritative_downgrade_gate"
	KeyAuthoritativeUpgradeSlidingThreshold Key = "authoritative_upgrade_sliding_threshold"
	KeyHMMDowngradeHysteresisTurns          Key = "hmm_downgrade_hysteresis_turns"
	KeyHMMDowngradeHysteresisShadowTurns    Key = "hmm_downgrade_hysteresis_shadow_turns"
	KeySwitchDecideShadow                   Key = "switch_decide_shadow"
	KeyAuthorityCacheShadow                 Key = "authority_cache_shadow"
	KeyAgreementShadowEpoch                 Key = "agreement_shadow_epoch"
	KeySiblingFailover                      Key = "sibling_failover"
	KeyInstallationPoolAllowance            Key = "installation_pool_allowance"
	KeyEffortEscalation                     Key = "effort_escalation"
	KeyCyberRefusalRepin                    Key = "cyber_refusal_repin"
	KeyCyberRefusalRetry                    Key = "cyber_refusal_retry"
	KeyCyberRefusalFallback                 Key = "cyber_refusal_fallback_model"
	KeyAnthropicServerFallback              Key = "anthropic_server_side_fallback"
	KeyOpenAIResponsesBroad                 Key = "openai_responses_broad"
	KeySessionPinEnabled                    Key = "session_pin_enabled"
	KeyHMMPhaseChangeGate                   Key = "hmm_phase_change_gate"
	KeyHardPinExplore                       Key = "hard_pin_explore"
	KeyTaskArmExperimentTreatmentPct        Key = "task_arm_experiment_treatment_pct"
	KeyTaskArmExperimentSteps               Key = "task_arm_experiment_steps"
	KeyTaskArmExperimentTreatmentCluster    Key = "task_arm_experiment_treatment_cluster"
	KeyCCOrchToolsCrossVendor               Key = "cc_orch_tools_crossvendor"
	KeyEscapeNormalize                      Key = "deepseek_escape_normalize"
	KeyPlannerTierUpgrade                   Key = "planner_tier_upgrade_enabled"
	KeyPlannerCorrectedEconomics            Key = "planner_corrected_economics"
	KeyAllowedModelsHeader                  Key = "allowed_models_header"
	KeyCCTaskToolsCrossVendor               Key = "cc_task_tools_crossvendor"
	KeyCCAutonomySystemAppend               Key = "cc_autonomy_system_append"
	KeyEarlyStopRedo                        Key = "early_stop_redo"
	KeyCCWorkspaceSystemAppend              Key = "cc_workspace_system_append"
	KeySubscriptionPlanAwareRouting         Key = "subscription_plan_aware_routing_enabled"
	KeyClaudeEnterpriseOnly                 Key = "claude_enterprise_only"
	KeySubscriptionExhaustedOpenRouterOnly  Key = "subscription_exhausted_openrouter_only"
	KeyCommittedStreamArmDemotion           Key = "committed_stream_arm_demotion"
	KeyRescuedFailureArmDemotion            Key = "rescued_failure_arm_demotion"
	KeyTransientRateLimit                   Key = "transient_rate_limit"
	KeyRateLimitCooldownSeconds             Key = "rate_limit_cooldown_seconds"
	KeyProviderOverloaded                   Key = "provider_overloaded"
	KeyNativeAnthropicResponseSignals       Key = "native_anthropic_response_signals"
	KeyNativeOpenAIResponseSignals          Key = "native_openai_response_signals"
	KeyPlanUserClusterLists                 Key = "plan_user_cluster_lists"
)

const (
	retiredKeyStruggleEscalationEnabled Key = "struggle_escalation_enabled"
	retiredKeyStruggleEscalationHoldout Key = "struggle_escalation_holdout_pct"
	retiredKeyStruggleEvidenceArming    Key = "struggle_evidence_arming"
	retiredKeySubscriberPaidFallback    Key = "subscriber_paid_fallback_enabled"
	retiredKeySubscriptionPoolQuota     Key = "subscription_pool_quota_aware_selection_enabled"
	retiredKeyEmbedOnlyUserMessage      Key = "embed_only_user_message"
	retiredKeyUpgradeHoldoutPct         Key = "authoritative_upgrade_holdout_pct"
	retiredKeyHMMSameTierPin            Key = "hmm_same_tier_pin"
	retiredKeyColdPinFollowFresh        Key = "planner_cold_pin_follow_fresh"
)

var retiredOverrideKeys = map[Key]struct{}{
	retiredKeyStruggleEscalationEnabled: {},
	retiredKeyStruggleEscalationHoldout: {},
	retiredKeyStruggleEvidenceArming:    {},
	retiredKeySubscriberPaidFallback:    {},
	retiredKeySubscriptionPoolQuota:     {},
	retiredKeyEmbedOnlyUserMessage:      {},
	retiredKeyUpgradeHoldoutPct:         {},
	retiredKeyHMMSameTierPin:            {},
	retiredKeyColdPinFollowFresh:        {},
}

type Definition struct {
	Key            Key
	EnvVar         string
	Kind           Kind
	Description    string
	OrgOverridable bool
}

var definitions = func() map[Key]Definition {
	m := make(map[Key]Definition, len(Registry))
	for _, d := range Registry {
		m[d.Key] = d
	}
	return m
}()

func Lookup(key Key) (def Definition, ok bool) {
	def, ok = definitions[key]
	return def, ok
}

type PublishedDefinition struct {
	Definition
	DeploymentDefault string
}

type DefinitionRepository interface {
	Publish(ctx context.Context, defs []PublishedDefinition) error
}

type Overrides struct {
	Bools   map[Key]bool
	Ints    map[Key]int
	Floats  map[Key]float64
	Strings map[Key]string
}

var ErrUnknownKey = fmt.Errorf("flags: unknown flag key")

var ErrNotOverridable = fmt.Errorf("flags: flag is not overridable per organization")

var ErrWrongKind = fmt.Errorf("flags: override value has the wrong kind")

var ErrInvalidValue = fmt.Errorf("flags: invalid override value")

func (o Overrides) IsEmpty() bool {
	return len(o.Bools) == 0 && len(o.Ints) == 0 && len(o.Floats) == 0 && len(o.Strings) == 0
}

func ValidateOverrides(o Overrides) error {
	seen := make(map[Key]struct{}, len(o.Bools)+len(o.Ints)+len(o.Floats)+len(o.Strings))
	check := func(key Key, kind Kind) error {
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: %q appears in multiple typed maps", ErrWrongKind, key)
		}
		seen[key] = struct{}{}
		def, ok := definitions[key]
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownKey, key)
		}
		if !def.OrgOverridable {
			return fmt.Errorf("%w: %q", ErrNotOverridable, key)
		}
		if def.Kind != kind {
			return fmt.Errorf("%w: %q is %s, got %s", ErrWrongKind, key, def.Kind, kind)
		}
		return nil
	}
	for key := range o.Bools {
		if err := check(key, KindBool); err != nil {
			return err
		}
	}
	for key, value := range o.Ints {
		if err := check(key, KindInt); err != nil {
			return err
		}
		if key == KeyLoopEscalationHoldoutPct || key == KeyTaskArmExperimentTreatmentPct {
			if value < 0 || value > 100 {
				return fmt.Errorf("%w: %q must be between 0 and 100, got %d", ErrInvalidValue, key, value)
			}
		}
		if (key == KeyAuthoritativeUpgradeVotes || key == KeyAgreementShadowEpoch) && value < 0 {
			return fmt.Errorf("%w: %q must be nonnegative, got %d", ErrInvalidValue, key, value)
		}
		if key == KeyRateLimitCooldownSeconds && value < 1 {
			return fmt.Errorf("%w: %q must be positive, got %d", ErrInvalidValue, key, value)
		}
	}
	for key := range o.Floats {
		if err := check(key, KindFloat); err != nil {
			return err
		}
	}
	for key, value := range o.Strings {
		if err := check(key, KindString); err != nil {
			return err
		}
		if key == KeyCyberRefusalFallback && strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %q cannot be empty", ErrInvalidValue, key)
		}
		if key == KeyAuthoritativeUpgradePolicy {
			switch AuthoritativeUpgradePolicy(value) {
			case AuthoritativeUpgradePolicyScore, AuthoritativeUpgradePolicyOff:
			default:
				return fmt.Errorf("%w: %q must be score or off, got %q", ErrInvalidValue, key, value)
			}
		}
	}
	return validateEscalationOverrides(o)
}

func (o Overrides) Overlay(top Overrides) Overrides {
	return Overrides{
		Bools:   overlayValues(o.Bools, top.Bools),
		Ints:    overlayValues(o.Ints, top.Ints),
		Floats:  overlayValues(o.Floats, top.Floats),
		Strings: overlayValues(o.Strings, top.Strings),
	}
}

func overlayValues[V any](base, top map[Key]V) map[Key]V {
	if len(base) == 0 && len(top) == 0 {
		return nil
	}
	merged := make(map[Key]V, len(base)+len(top))
	maps.Copy(merged, base)
	maps.Copy(merged, top)
	return merged
}

func (o Overrides) Keys() (keys []Key) {
	keys = make([]Key, 0, len(o.Bools)+len(o.Ints)+len(o.Floats)+len(o.Strings))
	for k := range o.Bools {
		keys = append(keys, k)
	}
	for k := range o.Ints {
		keys = append(keys, k)
	}
	for k := range o.Floats {
		keys = append(keys, k)
	}
	for k := range o.Strings {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func ParseOverrides(raw []byte) (o Overrides, err error) {
	o, err = DecodeOverrides(raw)
	if err != nil {
		return Overrides{}, err
	}
	if err := ValidateOverrides(o); err != nil {
		return Overrides{}, err
	}
	return o, nil
}

func DecodeOverrides(raw []byte) (o Overrides, err error) {
	if len(raw) == 0 {
		return Overrides{}, nil
	}
	var decoded map[string]json.RawMessage
	err = json.Unmarshal(raw, &decoded)
	if err != nil {
		return Overrides{}, fmt.Errorf("flags: decode overrides: %w", err)
	}
	for name, rawValue := range decoded {
		key := Key(name)
		def, ok := definitions[key]
		if !ok {
			if _, retired := retiredOverrideKeys[key]; retired {
				continue
			}
			return Overrides{}, fmt.Errorf("flags: unknown flag %q", name)
		}
		if !def.OrgOverridable {
			return Overrides{}, fmt.Errorf("flags: flag %q is not overridable per organization", name)
		}
		err = o.set(def, rawValue)
		if err != nil {
			return Overrides{}, err
		}
	}
	return o, nil
}

func (o *Overrides) set(def Definition, rawValue []byte) (err error) {
	switch def.Kind {
	case KindBool:
		var v bool
		err = json.Unmarshal(rawValue, &v)
		if err != nil {
			return fmt.Errorf("flags: flag %q expects a boolean: %w", def.Key, err)
		}
		if o.Bools == nil {
			o.Bools = make(map[Key]bool)
		}
		o.Bools[def.Key] = v
	case KindInt:
		var v int
		err = json.Unmarshal(rawValue, &v)
		if err != nil {
			return fmt.Errorf("flags: flag %q expects an integer: %w", def.Key, err)
		}
		if o.Ints == nil {
			o.Ints = make(map[Key]int)
		}
		o.Ints[def.Key] = v
	case KindFloat:
		var v float64
		err = json.Unmarshal(rawValue, &v)
		if err != nil {
			return fmt.Errorf("flags: flag %q expects a number: %w", def.Key, err)
		}
		if o.Floats == nil {
			o.Floats = make(map[Key]float64)
		}
		o.Floats[def.Key] = v
	case KindString:
		var v string
		err = json.Unmarshal(rawValue, &v)
		if err != nil {
			return fmt.Errorf("flags: flag %q expects a string: %w", def.Key, err)
		}
		if o.Strings == nil {
			o.Strings = make(map[Key]string)
		}
		o.Strings[def.Key] = v
	default:
		return fmt.Errorf("flags: flag %q has unsupported kind %q", def.Key, def.Kind)
	}
	return nil
}

func (o Overrides) MarshalJSON() (data []byte, err error) {
	flat := make(map[string]any, len(o.Bools)+len(o.Ints)+len(o.Floats)+len(o.Strings))
	for k, v := range o.Bools {
		flat[string(k)] = v
	}
	for k, v := range o.Ints {
		flat[string(k)] = v
	}
	for k, v := range o.Floats {
		flat[string(k)] = v
	}
	for k, v := range o.Strings {
		flat[string(k)] = v
	}
	return json.Marshal(flat)
}

type overridesContextKey struct{}

func WithOverrides(ctx context.Context, o Overrides) context.Context {
	if o.IsEmpty() {
		return ctx
	}
	return context.WithValue(ctx, overridesContextKey{}, o)
}

func OverridesFromContext(ctx context.Context) (o Overrides, ok bool) {
	o, ok = ctx.Value(overridesContextKey{}).(Overrides)
	return o, ok
}

func BoolOr(ctx context.Context, key Key, deploymentDefault bool) (value bool) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Bools[key]; found {
		return v
	}
	return deploymentDefault
}

func IntOr(ctx context.Context, key Key, deploymentDefault int) (value int) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Ints[key]; found {
		return v
	}
	return deploymentDefault
}

func FloatOr(ctx context.Context, key Key, deploymentDefault float64) (value float64) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Floats[key]; found {
		return v
	}
	return deploymentDefault
}

func StringOr(ctx context.Context, key Key, deploymentDefault string) (value string) {
	o, ok := OverridesFromContext(ctx)
	if !ok {
		return deploymentDefault
	}
	if v, found := o.Strings[key]; found {
		return v
	}
	return deploymentDefault
}
