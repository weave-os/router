package ingress_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/ingress"
)

func TestAnthropicParseMessagesCanonicalizesTheOneMillionVariant(t *testing.T) {
	body := `{"model":"claude-opus-4-8[1m]","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, err := ingress.Anthropic{}.ParseMessages(context.Background(), []byte(body))
	require.NoError(t, err)
	assert.True(t, req.ModelVariant1M)
	assert.Equal(t, "claude-opus-4-8", req.Envelope.Model())
	assert.NotContains(t, string(req.Body), "[1m]")
	assert.True(t, req.Envelope.Stream())
}

func TestAnthropicParseMessagesKeepsAPlainModel(t *testing.T) {
	body := `{"model":"claude-sonnet-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	env, forwarded, err := ingress.Anthropic{}.Parse(context.Background(), []byte(body))
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5", env.Model())
	assert.JSONEq(t, body, string(forwarded))
}
