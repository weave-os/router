package translate_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A Claude Code turn served on an OpenAI model must report its input in the
// requested Claude model's tokens: Claude Code budgets auto-compaction against
// the requested window from message usage, and the same history bills more
// tokens on Claude. The proxy supplies the thread's measured ratio; these tests
// use 1.8 as an arbitrary fixture. Reporting OpenAI counts let a thread grow past
// anything Claude could hold, so compaction never fired and then could not
// fit when it did.
const scaledCompletion = "event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]," +
	"\"usage\":{\"input_tokens\":600000,\"input_tokens_details\":{\"cached_tokens\":590000},\"output_tokens\":300}}}\n\n"

func messageDeltaUsage(t *testing.T, body string) gjson.Result {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "message_delta" {
			return gjson.Get(data, "usage")
		}
	}
	t.Fatalf("no message_delta in %q", body)
	return gjson.Result{}
}

func TestResponsesToAnthropicWriter_ClientInputScale(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		t.Run(map[bool]string{true: "streaming", false: "buffered"}[streaming], func(t *testing.T) {
			rec := httptest.NewRecorder()
			sink := &fakeUsageSink{}
			w := translate.NewResponsesToAnthropicWriter(rec, "gpt-6-luna", sink)
			w.SetClientInputTokenScale(1.8)
			require.NoError(t, w.Prelude(streaming))
			_, err := w.Write([]byte(scaledCompletion))
			require.NoError(t, err)
			require.NoError(t, w.Finalize())

			usage := gjson.GetBytes(rec.Body.Bytes(), "usage")
			if streaming {
				usage = messageDeltaUsage(t, rec.Body.String())
			}
			total := usage.Get("input_tokens").Int() + usage.Get("cache_read_input_tokens").Int() + usage.Get("cache_creation_input_tokens").Int()
			assert.EqualValues(t, 1_080_000, total, "client sees the history in Claude tokens")
			assert.EqualValues(t, 300, usage.Get("output_tokens").Int(), "output stays in served tokens")

			assert.Equal(t, 600_000, sink.input, "billing keeps the provider's own count")
			assert.Equal(t, 600_000, w.Summary().InputTokens, "telemetry keeps the provider's own count")
		})
	}
}

func TestAnthropicSSETranslator_ClientInputScale(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &fakeUsageSink{}
	w := translate.NewAnthropicSSETranslator(rec, "gpt-6-luna", sink)
	w.SetClientInputTokenScale(1.8)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]," +
		"\"usage\":{\"prompt_tokens\":100000,\"prompt_tokens_details\":{\"cached_tokens\":40000},\"completion_tokens\":10}}\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())

	usage := messageDeltaUsage(t, rec.Body.String())
	assert.EqualValues(t, 108_000, usage.Get("input_tokens").Int())
	assert.EqualValues(t, 72_000, usage.Get("cache_read_input_tokens").Int())
	assert.Equal(t, 100_000, sink.input)
}

func TestResponsesToAnthropicWriter_UnscaledByDefault(t *testing.T) {
	rec := httptest.NewRecorder()
	w := translate.NewResponsesToAnthropicWriter(rec, "gpt-6-luna", nil)
	require.NoError(t, w.Prelude(true))
	_, err := w.Write([]byte(scaledCompletion))
	require.NoError(t, err)
	require.NoError(t, w.Finalize())
	usage := messageDeltaUsage(t, rec.Body.String())
	assert.EqualValues(t, 10_000, usage.Get("input_tokens").Int())
	assert.EqualValues(t, 590_000, usage.Get("cache_read_input_tokens").Int())
}
