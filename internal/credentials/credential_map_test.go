package credentials_test

import (
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCredentialsMap_NilOnEmptySlice(t *testing.T) {
	m := credentials.BuildCredentialsMap(nil)
	assert.Nil(t, m,
		"BuildCredentialsMap must return nil when given no keys so callers can "+
			"cheaply distinguish 'no BYOK configured' from 'BYOK configured but empty'")
}

func TestBuildCredentialsMap_IndexesByProvider(t *testing.T) {
	keys := []*auth.ExternalAPIKey{
		{Provider: "anthropic", Plaintext: []byte("sk-ant-byok")},
		{Provider: "openai", Plaintext: []byte("sk-oai-byok")},
	}
	m := credentials.BuildCredentialsMap(keys)
	require.NotNil(t, m)
	require.Contains(t, m, "anthropic")
	assert.Equal(t, []byte("sk-ant-byok"), m["anthropic"].APIKey)
	assert.Equal(t, "byok", m["anthropic"].Source,
		"BYOK credentials must carry Source='byok' for observability")
	require.Contains(t, m, "openai")
	assert.Equal(t, []byte("sk-oai-byok"), m["openai"].APIKey)
}

func TestBuildCredentialsMap_DropsEmptyPlaintext(t *testing.T) {
	// An empty Plaintext means a stale/malformed BYOK row; it must not enroll
	// the provider, or argmax would pick it and the upstream call would 401.
	keys := []*auth.ExternalAPIKey{
		{Provider: "openrouter", Plaintext: []byte{}},
		{Provider: "anthropic", Plaintext: []byte("sk-ant-byok")},
	}
	m := credentials.BuildCredentialsMap(keys)
	require.NotNil(t, m)
	assert.NotContains(t, m, "openrouter",
		"BuildCredentialsMap must drop entries with empty Plaintext so the routing layer cannot enroll a provider that would 401 on dispatch")
	assert.Contains(t, m, "anthropic")
}

func TestBuildCredentialsMap_NilWhenAllEmpty(t *testing.T) {
	keys := []*auth.ExternalAPIKey{
		{Provider: "openrouter", Plaintext: []byte{}},
		{Provider: "fireworks", Plaintext: nil},
	}
	m := credentials.BuildCredentialsMap(keys)
	assert.Nil(t, m,
		"when every BYOK entry is empty the map must be nil so callers see 'no BYOK configured' rather than 'BYOK present but unusable'")
}

func TestBuildCredentialsMap_CarriesModelAliases(t *testing.T) {
	m := credentials.BuildCredentialsMap([]*auth.ExternalAPIKey{{
		Provider:     "anthropic_gateway",
		Plaintext:    []byte("token"),
		ModelAliases: map[string]string{"claude-fable-5": "gw-fable"},
	}})
	require.NotNil(t, m["anthropic_gateway"])
	assert.Equal(t, map[string]string{"claude-fable-5": "gw-fable"}, m["anthropic_gateway"].ModelAliases,
		"aliases must ride on the credential, or the endpoint receives catalog names it doesn't publish")
}

func TestBuildCredentialsMap_DropsRoutingModelAliases(t *testing.T) {
	keys := []*auth.ExternalAPIKey{{
		Provider:  "anthropic",
		Plaintext: []byte("sk-ant-byok"),
		ModelAliases: map[string]string{
			"claude-sonnet-4-5": "weave-auto",
			"claude-haiku-4-5":  "weave-auto-efficient",
			"claude-sonnet-4-6": "weave-auto-beta",
			"claude-opus-4-1":   "gateway-opus",
		},
	}}
	m := credentials.BuildCredentialsMap(keys)
	require.Contains(t, m, "anthropic")
	assert.Equal(t, map[string]string{"claude-opus-4-1": "gateway-opus"}, m["anthropic"].ModelAliases,
		"a stored alias naming a routing model must never reach an upstream request")
}
