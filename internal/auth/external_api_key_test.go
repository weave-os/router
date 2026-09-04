package auth_test

import (
	"fmt"
	"strings"
	"testing"

	"weave-os/router/internal/auth"

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

func TestNormalizeIdentityHeader(t *testing.T) {
	t.Run("both empty forwards nothing", func(t *testing.T) {
		name, format, err := auth.NormalizeIdentityHeader(nil, nil)
		require.NoError(t, err)
		assert.Nil(t, name)
		assert.Nil(t, format)
	})

	t.Run("trims and lowercases the format", func(t *testing.T) {
		name, format, err := auth.NormalizeIdentityHeader(ptr("  X-Caller-Identity "), ptr(" JSON "))
		require.NoError(t, err)
		require.NotNil(t, name)
		require.NotNil(t, format)
		assert.Equal(t, "X-Caller-Identity", *name)
		assert.Equal(t, auth.IdentityFormatJSON, *format)
	})

	t.Run("rejects a format without a name", func(t *testing.T) {
		_, _, err := auth.NormalizeIdentityHeader(nil, ptr("email"))
		require.ErrorIs(t, err, auth.ErrInvalidIdentityHeader)
	})

	t.Run("rejects an unknown format", func(t *testing.T) {
		_, _, err := auth.NormalizeIdentityHeader(ptr("X-Caller-Identity"), ptr("protobuf"))
		require.ErrorIs(t, err, auth.ErrInvalidIdentityHeader)
	})

	t.Run("rejects a name that is not a field token", func(t *testing.T) {
		for _, bad := range []string{"X Caller Identity", "X-Caller:Identity", "X-Caller\nIdentity"} {
			_, _, err := auth.NormalizeIdentityHeader(ptr(bad), ptr("email"))
			require.ErrorIsf(t, err, auth.ErrInvalidIdentityHeader,
				"%q would let a config value inject or split a header", bad)
		}
	})

	t.Run("rejects a reserved header", func(t *testing.T) {
		for _, reserved := range []string{"Authorization", "x-api-key", "Content-Length"} {
			_, _, err := auth.NormalizeIdentityHeader(ptr(reserved), ptr("email"))
			require.ErrorIsf(t, err, auth.ErrInvalidIdentityHeader,
				"naming %q would let identity forwarding strip or corrupt the upstream request", reserved)
		}
	})
}

func TestNormalizeForwardedClientHeaders(t *testing.T) {
	t.Run("empty forwards nothing", func(t *testing.T) {
		got, err := auth.NormalizeForwardedClientHeaders(nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("trims, drops blanks, and de-duplicates case-insensitively", func(t *testing.T) {
		got, err := auth.NormalizeForwardedClientHeaders([]string{" X-SNOWFLAKE-APPLICATION ", "", "x-snowflake-application"})
		require.NoError(t, err)
		assert.Equal(t, []string{"X-SNOWFLAKE-APPLICATION"}, got)
	})

	t.Run("rejects a name that is not a field token", func(t *testing.T) {
		for _, bad := range []string{"X Snowflake App", "X-Snowflake:App", "X-Snowflake\nApp"} {
			_, err := auth.NormalizeForwardedClientHeaders([]string{bad})
			require.ErrorIsf(t, err, auth.ErrInvalidForwardedHeader,
				"%q would let a config value inject or split a header", bad)
		}
	})

	t.Run("rejects a reserved header", func(t *testing.T) {
		for _, reserved := range []string{"Authorization", "x-api-key", "Host"} {
			_, err := auth.NormalizeForwardedClientHeaders([]string{reserved})
			require.ErrorIsf(t, err, auth.ErrInvalidForwardedHeader,
				"forwarding %q would let a caller redirect its own credentials upstream", reserved)
		}
	})

	t.Run("rejects more headers than the limit", func(t *testing.T) {
		many := make([]string, 0, 17)
		for i := range 17 {
			many = append(many, fmt.Sprintf("X-Custom-%d", i))
		}
		_, err := auth.NormalizeForwardedClientHeaders(many)
		require.ErrorIs(t, err, auth.ErrInvalidForwardedHeader)
	})
}

func TestNormalizeBaggageHeader(t *testing.T) {
	t.Run("blank forwards nothing", func(t *testing.T) {
		got, err := auth.NormalizeBaggageHeader(ptr("  "))
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("trims a valid name", func(t *testing.T) {
		got, err := auth.NormalizeBaggageHeader(ptr(" X-SNOWFLAKE-BAGGAGE "))
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "X-SNOWFLAKE-BAGGAGE", *got)
	})

	t.Run("rejects a reserved header", func(t *testing.T) {
		_, err := auth.NormalizeBaggageHeader(ptr("Authorization"))
		require.ErrorIs(t, err, auth.ErrInvalidForwardedHeader)
	})
}
