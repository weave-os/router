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

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type keyRecordingPinStore struct {
	*fakePinStore
	getKeys map[string][][sessionpin.SessionKeyLen]byte
}

func newKeyRecordingPinStore() *keyRecordingPinStore {
	return &keyRecordingPinStore{fakePinStore: newFakePinStore(), getKeys: map[string][][sessionpin.SessionKeyLen]byte{}}
}

func (k *keyRecordingPinStore) Get(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	k.mu.Lock()
	k.getKeys[role] = append(k.getKeys[role], key)
	k.mu.Unlock()
	return k.fakePinStore.Get(ctx, key, role)
}

func TestForceModelCommand_PinKeyIsDerivedFromUnstrippedOpeningMessage(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		provider string
		model    string
		turnOne  string
		turnTwo  string
		send     func(svc *proxy.Service, ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error
	}{
		{
			name: "anthropic_messages", path: "/v1/messages", provider: providers.ProviderAnthropic, model: "claude-haiku-4-5",
			turnOne: `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"/force-model opus\nfix the parser"}]}`,
			turnTwo: `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"/force-model opus\nfix the parser"},{"role":"assistant","content":"working on it"},{"role":"user","content":"keep going"}]}`,
			send: func(svc *proxy.Service, ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
				return svc.ProxyMessages(ctx, body, w, r)
			},
		},
		{
			name: "openai_chat_completions", path: "/v1/chat/completions", provider: providers.ProviderOpenAI, model: "gpt-4o",
			turnOne: `{"model":"gpt-4o","messages":[{"role":"user","content":"/force-model gpt-5\nfix the parser"}]}`,
			turnTwo: `{"model":"gpt-4o","messages":[{"role":"user","content":"/force-model gpt-5\nfix the parser"},{"role":"assistant","content":"working on it"},{"role":"user","content":"keep going"}]}`,
			send: func(svc *proxy.Service, ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
				return svc.ProxyOpenAIChatCompletion(ctx, body, w, r)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newKeyRecordingPinStore()
			fr := &fakeRouter{decision: router.Decision{Provider: tc.provider, Model: tc.model, Reason: "cluster"}}
			svc := proxy.NewService(fr, map[string]providers.Client{tc.provider: &fakeProvider{}}, nil, store, false, tc.provider, tc.model, nil)
			ctx := authedCtx(uuid.New().String())

			for i, body := range []string{tc.turnOne, tc.turnTwo} {
				store.getKeys = map[string][][sessionpin.SessionKeyLen]byte{}
				rec := httptest.NewRecorder()
				httpReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.path, strings.NewReader(""))
				require.NoError(t, tc.send(svc, ctx, []byte(body), rec, httpReq), "turn %d", i+1)
			}

			require.NotEmpty(t, store.upserts, "the /force-model turn must write a pin")
			forcePin := store.upserts[0]
			assert.Contains(t, store.getKeys[forcePin.Role], forcePin.SessionKey,
				"the next turn carries the opening message unstripped, so it only finds the pin when the command turn keyed it before stripping")
		})
	}
}
