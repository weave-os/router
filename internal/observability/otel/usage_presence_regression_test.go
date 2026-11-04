package otel_test

import (
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageExtractor_ReportedZeroReplacesSnapshot(t *testing.T) {
	ex := otel.NewUsageExtractor(httptest.NewRecorder(), providers.ProviderOpenAI)
	for _, frame := range []string{
		`data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":50},"completion_tokens_details":{"reasoning_tokens":3}}}` + "\n\n",
		`data: {"usage":{"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}` + "\n\n",
	} {
		_, err := ex.Write([]byte(frame))
		require.NoError(t, err)
	}
	input, output := ex.Tokens()
	_, read := ex.CacheTokens()
	assert.Equal(t, 100, input)
	assert.Zero(t, output)
	assert.Zero(t, read)
	assert.Zero(t, ex.ReasoningTokens())
}
