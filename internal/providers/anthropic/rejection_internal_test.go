package anthropic

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func validationBody(message string) []byte {
	return []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"` + message + `"}}`)
}

func TestClassifyRejection(t *testing.T) {
	prepared := []byte(`{"model":"claude","max_tokens":"lots","reasoning":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`)
	sent := []byte(`{"model":"claude-x","max_tokens":"lots","reasoning":{"effort":"xhigh"},"messages":[{"role":"user","content":"hi"}]}`)
	credential := providers.UpstreamRejection{Kind: providers.UpstreamRejectionCredential}
	field := func(path string) providers.UpstreamRejection {
		return providers.UpstreamRejection{Kind: providers.UpstreamRejectionField, Field: path}
	}
	cases := []struct {
		name    string
		status  int
		errBody []byte
		want    providers.UpstreamRejection
	}{
		{"api usage limit reached", http.StatusBadRequest, validationBody("You have reached your specified API usage limits. You will regain access on 2026-12-01 at 00:00 UTC."), credential},
		{"plan out of extra usage", http.StatusBadRequest, validationBody("You're out of extra usage. Add more at claude.ai/admin-settings/usage and keep going."), credential},
		{"key not scoped to a workspace", http.StatusBadRequest, validationBody("This API key is not scoped to a workspace, so this request must name one."), credential},
		{"field sent as prepared", http.StatusBadRequest, validationBody("max_tokens: Input should be a valid integer"), field("max_tokens")},
		{"required field absent from both bodies", http.StatusBadRequest, validationBody("tools: Field required"), field("tools")},
		{"field the adapter rewrote after preparation", http.StatusBadRequest, validationBody("reasoning.effort: Input should be 'low', 'medium' or 'high'"), providers.UpstreamRejection{}},
		{"model the adapter aliased", http.StatusBadRequest, validationBody("model: Input should be a valid model"), providers.UpstreamRejection{}},
		{"absent path without a required-field reason", http.StatusBadRequest, validationBody("messages.0.content.0.input: Input should be an object"), providers.UpstreamRejection{}},
		{"message without a field path", http.StatusBadRequest, validationBody("`temperature` is deprecated for this model."), providers.UpstreamRejection{}},
		{"same wording on a 500", http.StatusInternalServerError, validationBody("You have reached your specified API usage limits."), providers.UpstreamRejection{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyRejection(tc.status, tc.errBody, prepared, sent))
		})
	}
}
