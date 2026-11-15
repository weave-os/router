package postgres

import (
	"testing"

	"weave-os/router/internal/flags"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEscalationSelection_UsesOnlyStoredSettings(t *testing.T) {
	overrides, err := flags.ParseOverrides([]byte(`{"` + string(flags.KeyEscalationActiveClassifier) + `":"llm_escalation","` + string(flags.KeyEscalationCadence) + `":5}`))
	require.NoError(t, err)
	stored, err := overrides.MarshalJSON()
	require.NoError(t, err)

	empty, err := escalationSelection("inst-1", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, flags.EscalationClassifierNone, empty.Active)
	assert.Equal(t, 3, empty.Cadence)

	explicit, err := escalationSelection("inst-1", stored)
	require.NoError(t, err)
	assert.Equal(t, flags.EscalationClassifierLLM, explicit.Active)
	assert.Equal(t, 5, explicit.Cadence)
}
