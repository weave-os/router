package translate_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"
	providerusage "weave-os/router/internal/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesUsageResetDropsFailedAttemptSource(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := translate.NewResponsesWriter(rec, "synthetic-model")
	writer.SetUsageSource(func() providerusage.Facts {
		return providerusage.ParseOpenAI(gjson.Parse(`{"prompt_tokens":999,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800,"cache_write_tokens":99}}`))
	})
	writer.ResetAttempt()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, err := writer.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	require.NoError(t, err)
	require.NoError(t, writer.Finalize())
	facts := gjson.GetBytes(rec.Body.Bytes(), "usage")
	assert.EqualValues(t, 10, facts.Get("input_tokens").Int())
	assert.EqualValues(t, 2, facts.Get("output_tokens").Int())
	assert.False(t, facts.Get("input_tokens_details").Exists(), "failed attempt cache categories must not survive")
}
