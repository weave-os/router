package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A Claude Code turn for Opus served on Luna reports usage in Opus tokens, so
// Claude Code's auto-compaction tracks the window it believes it is in. Luna
// bills the same history ~0.58x Opus's count; reporting Luna's own numbers let
// a thread grow until no Opus call (including the compaction itself) fit.
func TestProxyMessages_LunaServedClaudeTurnReportsOpusTokens(t *testing.T) {
	const body = `{"model":"claude-opus-5-5","stream":true,"max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],"+
			"\"usage\":{\"input_tokens\":500000,\"input_tokens_details\":{\"cached_tokens\":499000},\"output_tokens\":5}}}\n\n")
	}}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", Reason: "test"}},
		map[string]providers.Client{providers.ProviderOpenAI: provider},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(body), rec, req))

	var usage gjson.Result
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "message_delta" {
			usage = gjson.Get(data, "usage")
		}
	}
	require.True(t, usage.Exists(), "no message_delta in %q", rec.Body.String())
	total := usage.Get("input_tokens").Int() + usage.Get("cache_read_input_tokens").Int() + usage.Get("cache_creation_input_tokens").Int()
	assert.EqualValues(t, 900_000, total, "500K Luna tokens are ~900K Opus tokens")
}
