package proxy

import (
	"context"
	"testing"

	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveSessionKeyForRequest_UsesHeaderSessionIDWhenBodyHasNone(t *testing.T) {
	env, err := translate.ParseOpenAI([]byte(`{
		"messages": [
			{"role": "system", "content": "You are Codex"},
			{"role": "user", "content": "<system_instruction>\nYou are working inside Conductor"}
		]
	}`))
	require.NoError(t, err)

	sessionA := "01a049b4-5372-71a0-8eba-f0836a5c68ee"
	sessionB := "01a04922-b33f-7582-b63b-faa672eb414c"
	ctxA := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: sessionA})
	ctxB := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: sessionB})

	kA := deriveSessionKeyForRequest(ctxA, env, "api-key")
	kB := deriveSessionKeyForRequest(ctxB, env, "api-key")
	kNoHeader := deriveSessionKeyForRequest(context.Background(), env, "api-key")

	assert.NotEqual(t, kA, kB, "Codex Session-Id must split otherwise identical first messages")
	assert.NotEqual(t, kA, kNoHeader, "header session id must change the pin vs prompt-only fallback")
}

func TestClientSessionIDForRequest_PrefersHeaderOverBody(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"metadata": {"user_id": "{\"session_id\":\"body-session\"}"},
		"messages": [{"role": "user", "content": "hi"}]
	}`))
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "header-session"})
	assert.Equal(t, "header-session", clientSessionIDForRequest(ctx, env))
	assert.Equal(t, "body-session", clientSessionIDForRequest(context.Background(), env))
}
