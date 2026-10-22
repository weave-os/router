package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// calibratingPinStore is a fakePinStore that also accepts tokenizer
// calibration writes.
type calibratingPinStore struct {
	*fakePinStore
	calMu  sync.Mutex
	writes []sessionpin.TokenizerCalibration
}

func (s *calibratingPinStore) UpdateTokenizerCalibration(_ context.Context, _ [sessionpin.SessionKeyLen]byte, _ string, _ router.Strategy, cal sessionpin.TokenizerCalibration) error {
	s.calMu.Lock()
	defer s.calMu.Unlock()
	s.writes = append(s.writes, cal)
	return nil
}

// lunaServedClaudeTurn is a main-loop Claude Code turn: real conversation
// turns always carry the tool registry, while a short tool-less call is a
// side call (classifier) that never touches the thread pin.
const lunaServedClaudeTurn = `{"model":"claude-opus-5-5","stream":true,"max_tokens":32000,"messages":[{"role":"user","content":"fix the bug"}],"tools":[{"name":"Read","description":"r","input_schema":{"type":"object"}}]}`

func lunaProvider(billedPrompt int) *fakeProvider {
	usage := "\"usage\":{\"input_tokens\":" + strconv.Itoa(billedPrompt) +
		",\"input_tokens_details\":{\"cached_tokens\":" + strconv.Itoa(billedPrompt-1000) + "},\"output_tokens\":5}"
	return &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],"+usage+"}}\n\n")
	}}
}

func clientPromptTokens(t *testing.T, body string) int64 {
	t.Helper()
	var usage gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "message_delta" {
			usage = gjson.Get(data, "usage")
		}
	}
	require.True(t, usage.Exists(), "no message_delta in %q", body)
	return usage.Get("input_tokens").Int() + usage.Get("cache_read_input_tokens").Int() + usage.Get("cache_creation_input_tokens").Int()
}

func serveLunaTurn(t *testing.T, store sessionpin.Store, billedPrompt int) string {
	t.Helper()
	return serveLunaTurnBody(t, store, billedPrompt, lunaServedClaudeTurn)
}

func serveLunaTurnBody(t *testing.T, store sessionpin.Store, billedPrompt int, body string) string {
	t.Helper()
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", Reason: "test"}},
		map[string]providers.Client{providers.ProviderOpenAI: lunaProvider(billedPrompt)},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	require.NoError(t, svc.ProxyMessages(context.Background(), []byte(body), rec, req))
	return rec.Body.String()
}

// A Claude Code turn for Opus served on Luna reports usage in Opus tokens,
// converted by the ratio this thread measured, so Claude Code's
// auto-compaction tracks the window it believes it is in.
func calibratedLunaThread() *calibratingPinStore {
	now := time.Now()
	store := &calibratingPinStore{fakePinStore: newFakePinStore()}
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", Reason: "test",
		PinnedUntil: now.Add(time.Hour), LastServedModel: "gpt-6-luna", LastTurnEndedAt: now,
		TokenizerCalibration: sessionpin.TokenizerCalibration{}.
			Observe(sessionpin.TokenizerFamilyAnthropic, 860_000, now.Add(-time.Minute)).
			Observe(sessionpin.TokenizerFamilyOpenAI, 500_000, now.Add(-30*time.Second)),
	}
	return store
}

func TestProxyMessages_LunaServedClaudeTurnReportsMeasuredOpusTokens(t *testing.T) {
	body := serveLunaTurn(t, calibratedLunaThread(), 500_000)
	assert.EqualValues(t, 860_000, clientPromptTokens(t, body), "500K Luna tokens on a thread measured at 1.72 are 860K Opus tokens")
}

// Ingress accepts provider-qualified ids; the conversion must still apply.
func TestProxyMessages_ProviderQualifiedRequestReportsMeasuredOpusTokens(t *testing.T) {
	qualified := strings.Replace(lunaServedClaudeTurn, `"model":"claude-opus-5-5"`, `"model":"anthropic/claude-opus-5-5"`, 1)
	require.NotEqual(t, lunaServedClaudeTurn, qualified)
	body := serveLunaTurnBody(t, calibratedLunaThread(), 500_000, qualified)
	assert.EqualValues(t, 860_000, clientPromptTokens(t, body))
}

// Until this thread has been served by both tokenizers, the router has no
// measurement and reports the provider's own count rather than guessing.
func TestProxyMessages_UncalibratedThreadReportsProviderTokens(t *testing.T) {
	store := &calibratingPinStore{fakePinStore: newFakePinStore()}
	body := serveLunaTurn(t, store, 500_000)
	assert.EqualValues(t, 500_000, clientPromptTokens(t, body))
}

// Each served conversation turn records the full prompt its tokenizer billed,
// so the thread's next turn on the other tokenizer can be calibrated.
func TestProxyMessages_ServedTurnRecordsTokenizerObservation(t *testing.T) {
	store := &calibratingPinStore{fakePinStore: newFakePinStore()}
	serveLunaTurn(t, store, 500_000)

	store.calMu.Lock()
	defer store.calMu.Unlock()
	require.Len(t, store.writes, 1)
	obs := store.writes[0].Observations[sessionpin.TokenizerFamilyOpenAI]
	assert.Equal(t, 500_000, obs.Prompt, "OpenAI input already includes cached tokens")
}
