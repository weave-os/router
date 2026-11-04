package proxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

const sessionlessClientApp = "copilot"

func sessionlessMessagesBody(t *testing.T, announcement string) []byte {
	t.Helper()
	firstStep := []map[string]any{{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": map[string]string{"command": "ls"}}}
	if announcement != "" {
		firstStep = append([]map[string]any{{"type": "text", "text": announcement}}, firstStep...)
	}
	body, err := json.Marshal(map[string]any{
		"model": "auto", "stream": true, "max_tokens": 4096,
		"tools": []map[string]any{{"name": "bash", "description": "run", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]string{"type": "string"}}}}},
		"messages": []map[string]any{
			{"role": "user", "content": "list the files, then count them"},
			{"role": "assistant", "content": firstStep},
			{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_1", "content": "a.go\nb.go"}}},
		},
	})
	require.NoError(t, err)
	return body
}

func sessionlessMessagesService(t *testing.T, pins *fakePinStore) (*proxy.Service, *[][]byte) {
	t.Helper()
	var upstreamBodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBodies = append(upstreamBodies, body)
		writeSSE(w, anthropicSSE(earlyStopLowModel, []string{textBlock("There are two files.")}, "end_turn"))
	}))
	t.Cleanup(upstream.Close)
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: earlyStopLowModel, Reason: "test"}},
		map[string]providers.Client{providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", upstream.URL)},
		nil, false, nil, pins, false, providers.ProviderAnthropic, earlyStopLowModel, newCaptureTelemetry(),
	).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).WithRetrySleep(noRetrySleep)
	return svc, &upstreamBodies
}

// A client that sends no session identity gets no server-side pin history, so
// the announcement it replays is the only record of the model it has seen.
func TestSessionlessRoutingAnnouncementMessages(t *testing.T) {
	const announced = "✦ **Weave Router** → " + earlyStopLowModel + " · best pick for this turn · request classified as low difficulty\n\n"
	const other = "✦ **Weave Router** → claude-opus-4-7 · best pick for this turn\n\n"
	for _, test := range []struct {
		name         string
		announcement string
		priorServed  string
		wantBadge    bool
	}{
		{name: "tool-loop step after announcement", announcement: announced},
		{name: "tool-loop step after whitespace-trimmed announcement", announcement: strings.TrimSpace(announced)},
		{name: "replayed a different model", announcement: other, wantBadge: true},
		{name: "nothing announced yet", wantBadge: true},
		{name: "quoted prose is not an announcement", announcement: "Docs say: " + announced, wantBadge: true},
		{name: "server history still decides", announcement: announced, priorServed: "claude-opus-4-7", wantBadge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pins := newFakePinStore()
			if test.priorServed != "" {
				pins.hasPin = true
				pins.pin = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: earlyStopLowModel, LastServedModel: test.priorServed, PinnedUntil: time.Now().Add(time.Hour)}
			}
			svc, upstreamBodies := sessionlessMessagesService(t, pins)
			ctx := requestcontext.WithClientIdentity(authedCtx("11111111-1111-1111-1111-111111111111"), proxy.ClientIdentity{ClientApp: sessionlessClientApp})
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(ctx, sessionlessMessagesBody(t, test.announcement), rec,
				httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))))
			assert.Equal(t, test.wantBadge, strings.Contains(rec.Body.String(), "Weave Router** → "+earlyStopLowModel), rec.Body.String())
			assert.Contains(t, rec.Body.String(), "There are two files.")
			require.Len(t, *upstreamBodies, 1)
			assert.NotContains(t, string((*upstreamBodies)[0]), "**Weave Router** →", "replayed announcements must stay out of provider input")
		})
	}
}

func TestSessionlessRoutingAnnouncementResponses(t *testing.T) {
	const model = "deepseek/deepseek-v4-pro"
	const announced = "✦ **Weave Router** → " + model + " · best pick for this turn · request classified as low difficulty\n\n"
	for _, test := range []struct {
		name      string
		history   []string
		wantBadge bool
	}{
		{name: "tool-loop step after announcement", history: []string{announced}},
		{name: "tool-loop step after provenance-marked announcement", history: []string{"\u2063\u2060\u2063\u2060" + announced}},
		{name: "replayed a different model", history: []string{"✦ **Weave Router** → gpt-6.1-terra · best pick for this turn\n\n"}, wantBadge: true},
		{name: "nothing announced yet", wantBadge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"two files\"},\"finish_reason\":null}]}\n\n"+
					"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}}
			svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenRouter, Model: model, Reason: "test"}},
				map[string]providers.Client{providers.ProviderOpenRouter: provider},
				nil, false, nil, newFakePinStore(), false, providers.ProviderOpenRouter, model, nil)
			ctx := requestcontext.WithClientIdentity(authedCtx("11111111-1111-1111-1111-111111111111"), proxy.ClientIdentity{ClientApp: sessionlessClientApp})
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, announcementReplayBody(t, test.history...), rec,
				httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
			text := strings.Join(responsesTextDeltas(t, rec.Body.Bytes()), "")
			assert.Equal(t, test.wantBadge, strings.Contains(text, "✦ **Weave Router** → "+model), text)
			assert.Contains(t, text, "two files")
			require.Len(t, provider.proxyBodies, 1)
			assert.NotContains(t, string(provider.proxyBodies[0]), "**Weave Router** →", "replayed announcements must stay out of provider input")
		})
	}
}
