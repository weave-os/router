package proxy

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func anthropicValidation400(message string) error {
	return &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"` + message + `"}}`)}
}

func TestRejectedFieldUnchanged(t *testing.T) {
	inbound := []byte(`{"model":"claude","max_tokens":"lots","metadata":{"client_app":"cli"},"messages":[{"role":"user","content":"hi"}]}`)
	sent := []byte(`{"model":"claude-x","max_tokens":"lots","metadata":{"client_app":"cli"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":"marker"}`)
	cases := []struct {
		name    string
		err     error
		inbound []byte
		sent    []byte
		want    bool
	}{
		{"invalid scalar the client sent", anthropicValidation400("max_tokens: Input should be a valid integer"), inbound, sent, true},
		{"extra field the client sent", anthropicValidation400("metadata.client_app: Extra inputs are not permitted"), inbound, sent, true},
		{"field the router rewrote", anthropicValidation400("messages.0.content: Input should be a valid array"), inbound, sent, false},
		{"field the router added", anthropicValidation400("system: Input should be a valid list"), inbound, sent, false},
		{"required field the client omitted", anthropicValidation400("tools: Field required"), inbound, sent, true},
		{"required field the router dropped", anthropicValidation400("metadata: Field required"), inbound, []byte(`{"model":"claude-x"}`), false},
		{"path missing from both bodies without a required-field verdict", anthropicValidation400("messages.0.content.0.tool_use.input: Input should be an object"), inbound, sent, false},
		{"message without a field path", anthropicValidation400("`temperature` is deprecated for this model."), inbound, sent, false},
		{"wildcard path", anthropicValidation400("metadata.*: Extra inputs are not permitted"), inbound, sent, false},
		{"non-400 status", &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"max_tokens: slow down"}}`)}, inbound, sent, false},
		{"no sent body", anthropicValidation400("max_tokens: Input should be a valid integer"), inbound, nil, false},
		{"not an upstream response", errors.New("max_tokens: Input should be a valid integer"), inbound, sent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rejectedFieldUnchanged(tc.err, tc.inbound, tc.sent))
		})
	}
}
