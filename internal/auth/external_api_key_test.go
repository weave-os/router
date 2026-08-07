package auth_test

import (
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
