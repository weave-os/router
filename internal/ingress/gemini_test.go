package ingress_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

const geminiStreamRequest = `{"model":"gemini-3-pro","stream":true,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

func TestNewServesGeminiWithKeepalive(t *testing.T) {
	formats := ingress.New(3 * time.Second)
	assert.Equal(t, 3*time.Second, formats.Keepalive)
	require.NotNil(t, formats.Gemini)
	assert.Equal(t, translate.FormatGemini, formats.Gemini.Format())
}

func TestGeminiTraceBodyDropsSyntheticRoutingFields(t *testing.T) {
	got := ingress.Gemini{}.TraceBody([]byte(geminiStreamRequest))
	assert.JSONEq(t, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, string(got))
}

func TestGeminiParseReturnsEnvelopeAndBody(t *testing.T) {
	req, err := ingress.Gemini{}.Parse(context.Background(), []byte(geminiStreamRequest))
	require.NoError(t, err)
	require.NotNil(t, req.Envelope)
	assert.True(t, req.Envelope.Stream())
	assert.Equal(t, "hi", req.Envelope.LastUserMessage().Text)
	assert.JSONEq(t, geminiStreamRequest, string(req.Body))
}

func TestGeminiCheckTargetAllowsOnlyGoogle(t *testing.T) {
	gemini := ingress.Gemini{}
	require.NoError(t, gemini.CheckTarget(providers.ProviderGoogle))
	err := gemini.CheckTarget(providers.ProviderAnthropic)
	assert.True(t, errors.Is(err, ingress.ErrGeminiCrossFormatUnsupported))
	assert.Contains(t, err.Error(), `"anthropic"`)
}

func TestGeminiWriteStreamErrorFlushesUnavailableFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	ingress.Gemini{}.WriteStreamError(rec)
	assert.Equal(t, "data: {\"error\":{\"code\":\"UNAVAILABLE\",\"message\":\"upstream stream failed\"}}\n\n", rec.Body.String())
	assert.True(t, rec.Flushed)
}
