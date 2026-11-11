package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

func TestResponseWritersEscapeHTMLWithoutChangingJSON(t *testing.T) {
	wrappers := map[string]func(http.ResponseWriter) http.ResponseWriter{
		"capture":  func(w http.ResponseWriter) http.ResponseWriter { return newCaptureWriter(w, 4096) },
		"prelude":  func(w http.ResponseWriter) http.ResponseWriter { b := newPreludeBuffer(w); b.Seal(); return b },
		"refusal":  func(w http.ResponseWriter) http.ResponseWriter { return newRefusalObserver(w) },
		"terminal": func(w http.ResponseWriter) http.ResponseWriter { return newResponsesTerminalObserver(w) },
		"cost":     func(w http.ResponseWriter) http.ResponseWriter { return newStreamCostWriter(w) },
		"usage": func(w http.ResponseWriter) http.ResponseWriter {
			return otel.NewUsageExtractor(w, providers.ProviderOpenAI)
		},
		"keepalive": func(w http.ResponseWriter) http.ResponseWriter { return sse.NewKeepaliveWriter(w, nil, 0) },
		"anthropic marker": func(w http.ResponseWriter) http.ResponseWriter {
			return translate.NewAnthropicRoutingMarkerWriter(w, "", "")
		},
		"openai marker": func(w http.ResponseWriter) http.ResponseWriter {
			return translate.NewOpenAIRoutingMarkerWriter(w, "", "")
		},
		"gemini marker": func(w http.ResponseWriter) http.ResponseWriter { return translate.NewGeminiRoutingMarkerWriter(w, "") },
		"anthropic footer": func(w http.ResponseWriter) http.ResponseWriter {
			return translate.NewAnthropicRoutingFooterWriter(w, "")
		},
		"openai footer": func(w http.ResponseWriter) http.ResponseWriter { return translate.NewOpenAIRoutingFooterWriter(w, "") },
		"gemini footer": func(w http.ResponseWriter) http.ResponseWriter { return translate.NewGeminiRoutingFooterWriter(w, "") },
		"tool names":    func(w http.ResponseWriter) http.ResponseWriter { return translate.NewAnthropicToolNameWriter(w, nil) },
	}
	const payload = `{"text":"</script><script>alert(1)</script>&","large":9007199254740993}`
	for name, wrap := range wrappers {
		t.Run(name, func(t *testing.T) {
			sink := httptest.NewRecorder()
			writer := wrap(sink)
			writer.Header().Set("Content-Type", "text/html")
			writer.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			writer.WriteHeader(http.StatusOK)
			consumed, err := writer.Write([]byte(payload))
			require.NoError(t, err)
			require.Equal(t, len(payload), consumed)
			require.NotContains(t, sink.Body.String(), "<script>")
			require.Equal(t, "application/json", sink.Result().Header.Get("Content-Type"))
			require.Equal(t, "nosniff", sink.Result().Header.Get("X-Content-Type-Options"))
			require.Empty(t, sink.Result().Header.Get("Content-Length"))
			require.JSONEq(t, payload, sink.Body.String())
			require.True(t, json.Valid(sink.Body.Bytes()))
			require.Contains(t, sink.Body.String(), "9007199254740993")
		})
	}
}
