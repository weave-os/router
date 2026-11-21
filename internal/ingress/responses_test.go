package ingress_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/ingress"
)

func TestResponsesDispatchBodiesConvertsTheInboundBodyOffTerminalSurfaces(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`)
	native, conversion, err := ingress.Responses{}.DispatchBodies(body, false)
	require.NoError(t, err)
	assert.JSONEq(t, string(body), string(native))
	assert.Equal(t, body, conversion)
}

func TestResponsesConvertSkipsTheLossyRescueForNonCodexClients(t *testing.T) {
	body := []byte(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	conversion, err := ingress.Responses{}.Convert(context.Background(), body, false, func(string) bool {
		t.Fatal("non-Codex conversion must not ask whether to rescue")
		return false
	})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5", conversion.Model)
	assert.Equal(t, "hi", gjson.GetBytes(conversion.Body, "messages.0.content").String())
}
