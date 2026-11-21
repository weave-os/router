package ingress_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/ingress"
)

func TestStripMessageThreadDropsACreateThreadAndItsBeta(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-beta", "message-threads-2025-01-01,context-1m-2025-08-07")
	body, continued, err := ingress.Anthropic{}.StripMessageThread([]byte(`{"model":"m","thread":{"type":"create"}}`), h)
	require.NoError(t, err)
	assert.False(t, continued)
	assert.False(t, gjson.GetBytes(body, "thread").Exists())
	assert.Equal(t, "context-1m-2025-08-07", h.Get("anthropic-beta"))
}

func TestStripMessageThreadReportsAContinueThread(t *testing.T) {
	in := []byte(`{"model":"m","thread":{"type":"continue","previous_message_id":"msg_1"}}`)
	body, continued, err := ingress.Anthropic{}.StripMessageThread(in, http.Header{})
	require.NoError(t, err)
	assert.True(t, continued)
	assert.JSONEq(t, string(in), string(body))
}
