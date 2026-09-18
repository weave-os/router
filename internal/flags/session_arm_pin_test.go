package flags_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/flags"
)

func TestSessionArmPinIsAnEnvBackedOverridableString(t *testing.T) {
	def, ok := flags.Lookup(flags.KeySessionArmPin)
	require.True(t, ok)
	assert.Equal(t, flags.KindString, def.Kind)
	assert.Equal(t, "ROUTER_SESSION_ARM_PIN", def.EnvVar)
	assert.True(t, def.OrgOverridable)
}

func TestParseSessionArmPinMode(t *testing.T) {
	for raw, want := range map[string]flags.SessionArmPinMode{
		"off":  flags.SessionArmPinOff,
		"main": flags.SessionArmPinMain,
		"all":  flags.SessionArmPinAll,
	} {
		got, err := flags.ParseSessionArmPinMode(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got)
	}
	for _, raw := range []string{"", "on", "true", "Main", "main-thread", " main"} {
		_, err := flags.ParseSessionArmPinMode(raw)
		require.Error(t, err, "%q must not parse", raw)
	}
}

func TestSessionArmPinOverrideValidation(t *testing.T) {
	for _, raw := range []string{
		`{"session_arm_pin":"on"}`,
		`{"session_arm_pin":""}`,
		`{"session_arm_pin":true}`,
		`{"session_arm_pin":1}`,
	} {
		_, err := flags.ParseOverrides([]byte(raw))
		require.Error(t, err, raw)
	}
	require.Error(t, flags.ValidateOverrides(flags.Overrides{Strings: map[flags.Key]string{flags.KeySessionArmPin: "sometimes"}}))

	for _, mode := range []flags.SessionArmPinMode{flags.SessionArmPinOff, flags.SessionArmPinMain, flags.SessionArmPinAll} {
		overrides, err := flags.ParseOverrides([]byte(`{"session_arm_pin":"` + string(mode) + `"}`))
		require.NoError(t, err, mode)
		ctx := flags.WithOverrides(context.Background(), overrides)
		assert.Equal(t, string(mode), flags.StringOr(ctx, flags.KeySessionArmPin, string(flags.SessionArmPinOff)))
	}
	assert.Equal(t, "off", flags.StringOr(context.Background(), flags.KeySessionArmPin, "off"),
		"no override falls back to the deployment default")
}
