package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const serverSideFallbackBody = `{"model":"claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`

func prepareWithFallback(t *testing.T, in http.Header, body string, opts translate.EmitOptions) providers.PreparedRequest {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	opts.Capabilities = router.Lookup(opts.TargetModel)
	prep, err := env.PrepareAnthropic(in, opts)
	require.NoError(t, err)
	return prep
}

func TestServerSideFallback_SetsFieldAndBeta(t *testing.T) {
	prep := prepareWithFallback(t, http.Header{}, serverSideFallbackBody, translate.EmitOptions{
		TargetModel:              "claude-opus-5",
		TargetProvider:           providers.ProviderAnthropic,
		EnableServerSideFallback: true,
	})

	assert.Equal(t, "default", gjson.GetBytes(prep.Body, "fallbacks").String())
	assert.Contains(t, prep.Headers.Get("anthropic-beta"), "server-side-fallback-2026-07-01")
}

func TestServerSideFallback_DisabledLeavesRequestUntouched(t *testing.T) {
	prep := prepareWithFallback(t, http.Header{}, serverSideFallbackBody, translate.EmitOptions{
		TargetModel:    "claude-opus-5",
		TargetProvider: providers.ProviderAnthropic,
	})

	assert.False(t, gjson.GetBytes(prep.Body, "fallbacks").Exists())
	assert.NotContains(t, prep.Headers.Get("anthropic-beta"), "server-side-fallback")
}

// The beta is a first-party Anthropic field; a gateway rejects the unknown key.
func TestServerSideFallback_SkippedForNonFirstPartyTarget(t *testing.T) {
	prep := prepareWithFallback(t, http.Header{}, serverSideFallbackBody, translate.EmitOptions{
		TargetModel:              "claude-opus-5",
		TargetProvider:           providers.ProviderAnthropicGateway,
		EnableServerSideFallback: true,
	})

	assert.False(t, gjson.GetBytes(prep.Body, "fallbacks").Exists())
	assert.NotContains(t, prep.Headers.Get("anthropic-beta"), "server-side-fallback")
}

func TestServerSideFallback_PreservesClientChoice(t *testing.T) {
	body := `{"model":"claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"fallbacks":"none"}`
	prep := prepareWithFallback(t, http.Header{}, body, translate.EmitOptions{
		TargetModel:              "claude-opus-5",
		TargetProvider:           providers.ProviderAnthropic,
		EnableServerSideFallback: true,
	})

	assert.Equal(t, "none", gjson.GetBytes(prep.Body, "fallbacks").String())
	assert.Contains(t, prep.Headers.Get("anthropic-beta"), "server-side-fallback-2026-07-01")
}

// Only opus-5 / fable-5 accept the field; the rest 400 on it.
func TestServerSideFallback_SkippedForUnsupportedModel(t *testing.T) {
	prep := prepareWithFallback(t, http.Header{}, serverSideFallbackBody, translate.EmitOptions{
		TargetModel:              "claude-haiku-4-5",
		TargetProvider:           providers.ProviderAnthropic,
		EnableServerSideFallback: true,
	})

	assert.False(t, gjson.GetBytes(prep.Body, "fallbacks").Exists())
	assert.NotContains(t, prep.Headers.Get("anthropic-beta"), "server-side-fallback")
}

// A re-pin off opus-5 must not forward the client's own "fallbacks" to a model
// that rejects it, nor its beta token.
func TestServerSideFallback_StripsClientFieldOnUnsupportedModel(t *testing.T) {
	in := http.Header{}
	in.Set("anthropic-beta", "server-side-fallback-2026-07-01")
	body := `{"model":"claude-haiku-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"fallbacks":"default"}`
	prep := prepareWithFallback(t, in, body, translate.EmitOptions{
		TargetModel:              "claude-haiku-4-5",
		TargetProvider:           providers.ProviderAnthropic,
		EnableServerSideFallback: true,
	})

	assert.False(t, gjson.GetBytes(prep.Body, "fallbacks").Exists())
	assert.NotContains(t, prep.Headers.Get("anthropic-beta"), "server-side-fallback")
}

func TestServerSideFallback_DedupesClientBeta(t *testing.T) {
	in := http.Header{}
	in.Set("anthropic-beta", "server-side-fallback-2026-07-01")
	prep := prepareWithFallback(t, in, serverSideFallbackBody, translate.EmitOptions{
		TargetModel:              "claude-opus-5",
		TargetProvider:           providers.ProviderAnthropic,
		EnableServerSideFallback: true,
	})

	assert.Equal(t, "server-side-fallback-2026-07-01", prep.Headers.Get("anthropic-beta"))
}
