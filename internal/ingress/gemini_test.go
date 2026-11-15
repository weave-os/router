package ingress_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
)

const geminiStreamRequest = `{"model":"gemini-3-pro","stream":true,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

func TestGeminiTraceBodyDropsSyntheticRoutingFields(t *testing.T) {
	got := ingress.Gemini{}.TraceBody([]byte(geminiStreamRequest))
	assert.JSONEq(t, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, string(got))
}

func TestGeminiParseReturnsEnvelopeAndBody(t *testing.T) {
	env, body, err := ingress.Gemini{}.Parse(context.Background(), []byte(geminiStreamRequest))
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.True(t, env.Stream())
	assert.Equal(t, "hi", env.LastUserMessage().Text)
	assert.JSONEq(t, geminiStreamRequest, string(body))
}

func TestGeminiCheckTargetAllowsOnlyGoogle(t *testing.T) {
	gemini := ingress.Gemini{}
	require.NoError(t, gemini.CheckTarget(providers.ProviderGoogle))
	err := gemini.CheckTarget(providers.ProviderAnthropic)
	assert.ErrorIs(t, err, ingress.ErrGeminiCrossFormatUnsupported)
	assert.Contains(t, err.Error(), `"anthropic"`)
}

func TestGeminiWriteStreamErrorFlushesUnavailableFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	ingress.Gemini{}.WriteStreamError(rec)
	assert.Equal(t, "data: {\"error\":{\"code\":\"UNAVAILABLE\",\"message\":\"upstream stream failed\"}}\n\n", rec.Body.String())
	assert.True(t, rec.Flushed)
}
