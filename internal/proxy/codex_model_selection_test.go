package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexModelSwitchInput is the retained developer fragment Codex records when
// the user changes models with its native /model picker mid-session.
const codexModelSwitchInput = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"<model_switch>\nThe user was previously using a different model. Please continue the conversation according to the following instructions:\n\nYou are GPT-6.\n</model_switch>"}]}`

func TestCodexModelSelectionAlwaysScores(t *testing.T) {
	for _, tc := range []struct {
		name            string
		model           string
		legacyPinHeader bool
		switched        bool
		want            string
	}{
		{name: "automatic choice", model: proxy.CodexAutomaticModel, legacyPinHeader: true, switched: true, want: proxy.CodexAutomaticModel},
		{name: "hmm router choice", model: string(router.ModelHMM), legacyPinHeader: true, switched: true, want: string(router.ModelHMM)},
		{name: "qwen router choice", model: string(router.ModelQwen), legacyPinHeader: true, switched: true, want: string(router.ModelQwen)},
		{name: "native /model switch on a legacy pin install", model: "gpt-6-sol", legacyPinHeader: true, switched: true, want: "gpt-6-sol"},
		{name: "older install", model: "gpt-6-sol", legacyPinHeader: false, switched: true, want: "gpt-6-sol"},
		{name: "launch model without switch", model: "gpt-6-sol", legacyPinHeader: true, switched: false, want: "gpt-6-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`)
			}}
			routerStub := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "fresh"}}
			svc := proxy.NewService(routerStub, map[string]providers.Client{providers.ProviderOpenAI: provider}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)
			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
			input := `[{"type":"message","role":"user","content":"review this change"}]`
			if tc.switched {
				input = `[` + codexModelSwitchInput + `,{"type":"message","role":"user","content":"review this change"}]`
			}
			body := []byte(`{"model":"` + tc.model + `","input":` + input + `,"reasoning":{"effort":"max"}}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tc.legacyPinHeader {
				req.Header.Set(proxy.CodexNativeModelPinHeader, "1")
			}
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))
			assert.Positive(t, routerStub.routeCalls, "automatic routing must still invoke the scorer")
			require.NotNil(t, routerStub.capturedReq)
			assert.Equal(t, tc.want, routerStub.capturedReq.RequestedModel)
			assert.Equal(t, "gpt-5.6-luna", rec.Header().Get(proxy.HeaderRouterModel))
		})
	}
}
