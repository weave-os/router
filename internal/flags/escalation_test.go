package flags_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/flags"
)

func TestEscalationSelectorPreservesLegacyUntilExplicit(t *testing.T) {
	overrides := flags.Overrides{Bools: map[flags.Key]bool{flags.KeyEscalationXGBoostEnabled: true}, Ints: map[flags.Key]int{flags.KeyEscalationXGBoostEpoch: 7}}
	legacy := flags.EscalationFromContext(flags.WithOverrides(context.Background(), overrides))
	assert.Equal(t, flags.EscalationClassifierXGB, legacy.Active)
	assert.Equal(t, 7, legacy.Epoch)
	overrides.Strings = map[flags.Key]string{flags.KeyEscalationActiveClassifier: string(flags.EscalationClassifierNone), flags.KeyEscalationShadowClassifier: string(flags.EscalationClassifierSwitchyard)}
	overrides.Ints[flags.KeyEscalationEpoch] = 9
	explicit := flags.EscalationFromContext(flags.WithOverrides(context.Background(), overrides))
	assert.Equal(t, flags.EscalationClassifierNone, explicit.Active)
	assert.Equal(t, flags.EscalationClassifierSwitchyard, explicit.Shadow)
	assert.Equal(t, 3, explicit.Cadence)
	assert.Equal(t, 9, explicit.Epoch)
}

func TestEscalationOverrideValidation(t *testing.T) {
	for _, raw := range []string{
		`{"escalation_active_classifier":"unknown"}`,
		`{"escalation_shadow_classifier":"switchyard_llm_v1"}`,
		`{"escalation_active_classifier":"xgb","escalation_shadow_classifier":"xgb"}`,
		`{"escalation_cadence":2}`,
		`{"escalation_cadence":6}`,
		`{"escalation_epoch":-1}`,
	} {
		_, err := flags.ParseOverrides([]byte(raw))
		require.Error(t, err, raw)
	}
	_, err := flags.ParseOverrides([]byte(`{"escalation_active_classifier":"switchyard_llm_v1","escalation_shadow_classifier":"xgb","escalation_cadence":5,"escalation_epoch":1}`))
	require.NoError(t, err)
}
