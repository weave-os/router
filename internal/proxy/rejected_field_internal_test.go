package proxy

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func fieldRejection(path string) error {
	return &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Rejection: providers.UpstreamRejection{Kind: providers.UpstreamRejectionField, Field: path}}
}

func TestRejectedFieldUnchanged(t *testing.T) {
	inbound := []byte(`{"model":"claude","max_tokens":"lots","metadata":{"client_app":"cli"},"messages":[{"role":"user","content":"hi"}]}`)
	sent := []byte(`{"model":"claude-x","max_tokens":"lots","metadata":{"client_app":"cli"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":"marker"}`)
	cases := []struct {
		name string
		err  error
		sent []byte
		want bool
	}{
		{"invalid scalar the client sent", fieldRejection("max_tokens"), sent, true},
		{"extra field the client sent", fieldRejection("metadata.client_app"), sent, true},
		{"required field the client omitted", fieldRejection("tools"), sent, true},
		{"field the router rewrote", fieldRejection("messages.0.content"), sent, false},
		{"field the router added", fieldRejection("system"), sent, false},
		{"field the router dropped", fieldRejection("metadata"), []byte(`{"model":"claude-x"}`), false},
		{"credential refusal names no field", &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Rejection: providers.UpstreamRejection{Kind: providers.UpstreamRejectionCredential}}, sent, false},
		{"unclassified 400", &providers.UpstreamErrorResponse{Status: http.StatusBadRequest}, sent, false},
		{"no sent body", fieldRejection("max_tokens"), nil, false},
		{"not an upstream response", errors.New("max_tokens: Input should be a valid integer"), sent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rejectedFieldUnchanged(tc.err, inbound, tc.sent))
		})
	}
}
