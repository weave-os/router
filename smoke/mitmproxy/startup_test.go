package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestReplayStartupFixtureExercisesServingAdapters(t *testing.T) {
	for _, test := range []struct {
		provider, model, host, path, output string
		tokens                              int
	}{
		{providers.ProviderAnthropic, "claude-haiku-4-5", "api.anthropic.com", "/v1/messages", "content.0.text", 32},
		{providers.ProviderAnthropic, "claude-opus-4-8", "api.anthropic.com", "/v1/messages", "content.0.text", 1024},
		{providers.ProviderOpenAI, "gpt-4.1", "api.openai.com", "/v1/chat/completions", "choices.0.message.content", 32},
		{providers.ProviderOpenAI, "gpt-5.5", "api.openai.com", "/v1/responses", "output.0.content.0.text", 1024},
	} {
		t.Run(test.model, func(t *testing.T) {
			store, err := newStore(t.TempDir())
			require.NoError(t, err)
			p := &proxy{cfg: config{mode: modeReplayOnly}, store: store}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.URL.Path != test.path {
					t.Errorf("provider endpoint = %q, want %q", r.URL.Path, test.path)
				}
				c, err := p.resolve(test.host, r, body, requestKey(r.Method, r.URL.Path, body))
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				for name, value := range c.Headers {
					w.Header().Set(name, value)
				}
				w.WriteHeader(c.StatusCode)
				_, _ = w.Write(c.Body)
			}))
			defer upstream.Close()

			body, err := json.Marshal(map[string]any{"model": test.model, "messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}}, "max_tokens": test.tokens, "stream": false})
			require.NoError(t, err)
			envelope, err := translate.ParseOpenAI(body)
			require.NoError(t, err)
			options := translate.EmitOptions{TargetModel: test.model, TargetProvider: test.provider, Capabilities: router.Lookup(test.model)}
			var client providers.Client
			var prepared providers.PreparedRequest
			switch test.provider {
			case providers.ProviderAnthropic:
				client = anthropic.NewClient(startupFixtureKey, upstream.URL)
				envelope, err = translate.ParseAnthropic(body)
				require.NoError(t, err)
				if test.tokens == 1024 {
					options.ForceEffort = "low"
				}
				prepared, err = envelope.PrepareAnthropic(nil, options)
			case providers.ProviderOpenAI:
				client = openai.NewClient(startupFixtureKey, upstream.URL)
				if test.path == "/v1/responses" {
					options.ForceReasoningEffort = "low"
					prepared, err = envelope.PrepareOpenAIResponses(nil, options)
				} else {
					prepared, err = envelope.PrepareOpenAI(nil, options)
				}
			}
			require.NoError(t, err)
			outputField := "max_tokens"
			if prepared.Endpoint == providers.EndpointResponses {
				outputField = "max_output_tokens"
			}
			prepared.Body, err = sjson.SetBytes(prepared.Body, outputField, test.tokens)
			require.NoError(t, err)
			prepared.Body, err = sjson.SetBytes(prepared.Body, "stream", false)
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "http://router.invalid/", bytes.NewReader(prepared.Body))
			response := httptest.NewRecorder()
			err = client.Proxy(t.Context(), router.Decision{Model: test.model, Provider: test.provider}, prepared, response, request)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "OK", gjson.GetBytes(response.Body.Bytes(), test.output).String())
			files, err := os.ReadDir(store.dir)
			require.NoError(t, err)
			require.Empty(t, files, "authored startup replies must not mutate recorded cassettes")
		})
	}
}

const startupChatRequest = `{"model":"gpt-4.1","messages":[{"role":"user","content":"Reply with OK."}],"max_tokens":32,"stream":false}`

func TestReplayStartupFixtureCannotHideOrdinaryCassetteMisses(t *testing.T) {
	for _, test := range []struct {
		name, host, path, key, body string
	}{
		{name: "ordinary prompt", body: strings.Replace(startupChatRequest, "Reply with OK.", "Explain the test fixture.", 1)},
		{name: "extra system prompt", body: strings.Replace(startupChatRequest, `"messages":[`, `"messages":[{"role":"system","content":"extra"},`, 1)},
		{name: "tools", body: strings.Replace(startupChatRequest, `"stream":false`, `"stream":false,"tools":[]`, 1)},
		{name: "extra message field", body: strings.Replace(startupChatRequest, `"role":"user"`, `"role":"user","name":"customer"`, 1)},
		{name: "streaming", body: strings.Replace(startupChatRequest, `"stream":false`, `"stream":true`, 1)},
		{name: "unbounded output", body: strings.Replace(startupChatRequest, `"max_tokens":32`, `"max_tokens":32000`, 1)},
		{name: "unknown host", host: "provider.invalid"},
		{name: "unknown endpoint", path: "/v1/unknown"},
		{name: "query parameters", path: "/v1/chat/completions?extra=true"},
		{name: "real credential", key: "synthetic-non-fixture-credential"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := newStore(t.TempDir())
			require.NoError(t, err)
			p := &proxy{cfg: config{mode: modeReplayOnly}, store: store}
			if test.host == "" {
				test.host = "api.openai.com"
			}
			if test.path == "" {
				test.path = "/v1/chat/completions"
			}
			if test.key == "" {
				test.key = startupFixtureKey
			}
			if test.body == "" {
				test.body = startupChatRequest
			}
			request := httptest.NewRequest(http.MethodPost, "https://"+test.host+test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+test.key)
			_, err = p.resolve(test.host, request, []byte(test.body), requestKey(request.Method, request.URL.Path, []byte(test.body)))
			require.ErrorIs(t, err, errCacheMiss)
		})
	}
}

type recordingTransport func(*http.Request) (*http.Response, error)

func (transport recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestRecordingModesDoNotSubstituteStartupFixtures(t *testing.T) {
	for _, mode := range []string{modeRecord, modeReplayOrRecord} {
		t.Run(mode, func(t *testing.T) {
			store, err := newStore(t.TempDir())
			require.NoError(t, err)
			upstreamErr := errors.New("synthetic live upstream refusal")
			p := &proxy{cfg: config{mode: mode}, store: store, live: &http.Client{Transport: recordingTransport(func(*http.Request) (*http.Response, error) {
				return nil, upstreamErr
			})}}
			request := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", strings.NewReader(startupChatRequest))
			request.Header.Set("Authorization", "Bearer "+startupFixtureKey)
			_, err = p.resolve("api.openai.com", request, []byte(startupChatRequest), requestKey(request.Method, request.URL.Path, []byte(startupChatRequest)))
			require.ErrorIs(t, err, upstreamErr)
		})
	}
}
