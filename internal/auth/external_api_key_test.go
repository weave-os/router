package auth_test

import (
	"fmt"
	"strings"
	"testing"

	"workweave/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeBaseURL(t *testing.T) {
	t.Run("nil and blank clear the override", func(t *testing.T) {
		for name, in := range map[string]*string{
			"nil":        nil,
			"empty":      ptr(""),
			"whitespace": ptr("   "),
		} {
			got, err := auth.NormalizeBaseURL(in)
			require.NoError(t, err, name)
			assert.Nil(t, got, "%s must store NULL so the deployment endpoint stays in effect", name)
		}
	})

	t.Run("trims whitespace and trailing slashes", func(t *testing.T) {
		got, err := auth.NormalizeBaseURL(ptr("  https://gateway.example.com/llm//  "))
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "https://gateway.example.com/llm", *got,
			"providers append their own path, so a trailing slash would produce a double-slash upstream URL")
	})

	t.Run("rejects values that cannot address an upstream", func(t *testing.T) {
		for name, in := range map[string]string{
			"no scheme":       "gateway.example.com",
			"relative path":   "/v1/messages",
			"no host":         "https://",
			"unsupported ftp": "ftp://gateway.example.com",
		} {
			_, err := auth.NormalizeBaseURL(ptr(in))
			assert.ErrorIs(t, err, auth.ErrInvalidBaseURL, name)
		}
	})
}

func ptr(s string) *string { return &s }

func TestNormalizeModelAliases(t *testing.T) {
	t.Run("nil when nothing survives", func(t *testing.T) {
		for name, in := range map[string]map[string]string{
			"nil":         nil,
			"empty":       {},
			"blank alias": {"claude-fable-5": "   "},
			"blank model": {"  ": "gateway-model"},
		} {
			got, err := auth.NormalizeModelAliases(in, nil)
			require.NoError(t, err, name)
			assert.Nil(t, got, "%s must collapse to nil so \"no aliases\" has one representation", name)
		}
	})

	t.Run("trims both sides", func(t *testing.T) {
		got, err := auth.NormalizeModelAliases(map[string]string{"  claude-fable-5 ": "  gw-fable  "}, nil)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"claude-fable-5": "gw-fable"}, got,
			"an untrimmed key would never match the routed model id, silently disabling the alias")
	})

	t.Run("rejects a model outside the allowed set", func(t *testing.T) {
		allowed := map[string]struct{}{"claude-fable-5": {}}
		_, err := auth.NormalizeModelAliases(map[string]string{"claude-fabel-5": "gw-fable"}, allowed)
		require.ErrorIs(t, err, auth.ErrUnknownModel)
	})

	t.Run("rejects an oversized map", func(t *testing.T) {
		raw := make(map[string]string, 300)
		for i := range 300 {
			raw[fmt.Sprintf("model-%d", i)] = "gw"
		}
		_, err := auth.NormalizeModelAliases(raw, nil)
		require.ErrorIs(t, err, auth.ErrInvalidModelAlias)
	})

	t.Run("rejects an overlong alias", func(t *testing.T) {
		_, err := auth.NormalizeModelAliases(map[string]string{"claude-fable-5": strings.Repeat("m", 256)}, nil)
		require.ErrorIs(t, err, auth.ErrInvalidModelAlias)
	})
}
