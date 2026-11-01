package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

// A directive that opens the conversation is the session key's first-message
// discriminator, so it must survive the history strip or the pin it created
// is orphaned on the very next turn. A directive on a later turn carries no
// such weight and must leave the upstream body.
func TestService_HistoricalDirective_KeepsSessionPin(t *testing.T) {
	for _, tc := range []struct {
		name          string
		directive     string
		sessionID     string
		opening       bool
		upstreamKeeps bool
	}{
		{name: "opening force-parent with client session", directive: "/force-parent gpt-5", sessionID: "ses_codex_1", opening: true, upstreamKeeps: true},
		{name: "opening force-model without client session", directive: "/force-model gpt-5", sessionID: "", opening: true, upstreamKeeps: true},
		{name: "later force-parent with client session", directive: "/force-parent gpt-5", sessionID: "ses_codex_1"},
		{name: "later force-model without client session", directive: "/force-model gpt-5", sessionID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakePinStore()
			store.persistUpserts = true
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
			openAI := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`))
			}}
			svc := proxy.NewService(fr, map[string]providers.Client{
				providers.ProviderAnthropic: &fakeProvider{},
				providers.ProviderOpenAI:    openAI,
			}, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)

			identity := proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: tc.sessionID}
			ctx := context.WithValue(authedCtx(uuid.New().String()), proxy.ClientIdentityContextKey{}, identity)
			send := func(body string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
				require.NoError(t, svc.ProxyOpenAIResponses(ctx, []byte(body), rec, req))
				return rec
			}

			userItem := func(text string) string {
				return `{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}`
			}
			assistantItem := func(text string) string {
				return `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}`
			}
			var history []string
			if !tc.opening {
				send(`{"model":"gpt-4o","input":[` + userItem("hello") + `]}`)
				history = append(history, userItem("hello"), assistantItem("ok"))
			}
			history = append(history, userItem(tc.directive))
			send(`{"model":"gpt-4o","input":[` + strings.Join(history, ",") + `]}`)
			require.NotEmpty(t, store.upserts, "the directive turn pins the session")
			assert.Equal(t, "gpt-5", store.upserts[len(store.upserts)-1].Model)

			openAI.proxyBodies = nil
			fr.routeCalls = 0
			history = append(history, assistantItem("Weave Router: force-model applied: gpt-5 (openai)."), userItem("now write the parser"))
			rec := send(`{"model":"gpt-4o","input":[` + strings.Join(history, ",") + `]}`)
			assert.Equal(t, 0, fr.routeCalls, "the pin must still be found on the next turn")
			assert.Equal(t, "gpt-5", rec.Header().Get(proxy.HeaderRouterModel))
			require.Len(t, openAI.proxyBodies, 1)
			assert.Equal(t, "gpt-5", gjson.GetBytes(openAI.proxyBodies[0], "model").Str)
			upstreamSawDirective := false
			for _, item := range gjson.GetBytes(openAI.proxyBodies[0], "input").Array() {
				for _, part := range item.Get("content").Array() {
					if strings.Contains(part.Get("text").Str, tc.directive) {
						upstreamSawDirective = true
					}
				}
			}
			assert.Equal(t, tc.upstreamKeeps, upstreamSawDirective, "an opening directive is the thread key and stays; a later one goes")
		})
	}
}
