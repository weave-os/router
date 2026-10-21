package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const untypedResponsesStream = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_compaction","status":"in_progress","output":[]}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","sequence_number":1,"output_index":0,"content_index":0,"item_id":"msg_compaction","delta":"handoff"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_compaction","status":"completed","output":[{"id":"msg_compaction","type":"message","role":"assistant","content":[{"type":"output_text","text":"handoff"}]}]}}` + "\n\n"

type untypedResponsesStreamProvider struct{}

func (untypedResponsesStreamProvider) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(untypedResponsesStream))
	return err
}

func (untypedResponsesStreamProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

type untypedResponsesRouter struct{}

func (untypedResponsesRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "scored"}, nil
}

// The Codex subscription backend streams Responses SSE without Content-Type.
// Compaction has no visible notice, so detection cannot depend on the badge writer.
func TestCodexCompactionPreservesUntypedResponsesStream(t *testing.T) {
	for _, tc := range []struct {
		name       string
		forceModel string
	}{
		{name: "compaction hard pin"},
		{name: "forced model", forceModel: "gpt-5.6-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(untypedResponsesRouter{}, map[string]providers.Client{providers.ProviderOpenAI: untypedResponsesStreamProvider{}},
				nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-luna", nil)
			body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Fix the bug."}]},` +
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Working."}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary."}]}]}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tc.forceModel != "" {
				req.Header.Set(ForceModelHeader, tc.forceModel)
			}
			rec := httptest.NewRecorder()
			ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, req))

			assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
			assert.Contains(t, rec.Body.String(), `"delta":"handoff"`)
			assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.completed"))
		})
	}
}
