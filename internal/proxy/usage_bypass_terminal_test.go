package proxy_test

import (
	"net/http"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageBypassMissingTerminalPreservesFailedCommittedAttempt(t *testing.T) {
	svc, scorer, provider := bypassFixture(t, 0.20)
	provider.proxyResponse = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic-message\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"))
	}
	rec, req, _ := bypassRequest(t)
	body := []byte(`{"model":"` + bypassRequestedMdl + `","stream":true,"messages":[{"role":"user","content":"synthetic question"}]}`)
	err := svc.ProxyMessages(bypassCtx(0.80), body, rec, req)
	require.ErrorIs(t, err, translate.ErrStreamIncomplete)
	assert.Zero(t, scorer.routeCalls, "a committed bypass cannot reroute")
	assert.Len(t, provider.proxyBodies, 1)
	assert.Contains(t, rec.Body.String(), "partial")
	assert.NotContains(t, rec.Body.String(), "message_stop")
}

func TestUsageBypassBufferedDeclaredFailureIsNotSuccess(t *testing.T) {
	svc, scorer, provider := bypassFixture(t, 0.20)
	provider.proxyResponse = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"synthetic bypass failure"}}`))
	}
	rec, req, body := bypassRequest(t)
	err := svc.ProxyMessages(bypassCtx(0.80), body, rec, req)
	require.Error(t, err)
	assert.Zero(t, scorer.routeCalls)
	assert.Contains(t, rec.Body.String(), "synthetic bypass failure")
	assert.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"error"`))
}
