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

func TestDeriveForceModelSessionKeyForRequest_SharedAcrossThreads(t *testing.T) {
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "shared-session"})
	parent, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role": "user", "content": "parent task"}]
	}`))
	require.NoError(t, err)
	child, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role": "user", "content": "different child task"}]
	}`))
	require.NoError(t, err)

	parentThread := deriveSessionKeyForRequest(ctx, parent, "api-key")
	childThread := deriveSessionKeyForRequest(ctx, child, "api-key")
	parentForce := deriveForceModelSessionKeyForRequest(ctx, parent, "api-key", parentThread)
	childForce := deriveForceModelSessionKeyForRequest(ctx, child, "api-key", childThread)

	assert.NotEqual(t, parentThread, childThread)
	assert.Equal(t, parentForce, childForce)
}

func TestDeriveForceModelSessionKeyForRequest_IsolatesSessionsAndAPIKeys(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role": "user", "content": "task"}]
	}`))
	require.NoError(t, err)
	ctxA := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "session-a"})
	ctxB := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "session-b"})
	threadA := deriveSessionKeyForRequest(ctxA, env, "api-key-a")
	threadB := deriveSessionKeyForRequest(ctxB, env, "api-key-a")

	keyA := deriveForceModelSessionKeyForRequest(ctxA, env, "api-key-a", threadA)
	keyB := deriveForceModelSessionKeyForRequest(ctxB, env, "api-key-a", threadB)
	otherAPIKey := deriveForceModelSessionKeyForRequest(ctxA, env, "api-key-b", threadA)

	assert.NotEqual(t, keyA, keyB)
	assert.NotEqual(t, keyA, otherAPIKey)
}

func TestDeriveForceModelSessionKeyForRequest_FallsBackToThreadScope(t *testing.T) {
	parent, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role": "user", "content": "parent task"}]
	}`))
	require.NoError(t, err)
	child, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role": "user", "content": "child task"}]
	}`))
	require.NoError(t, err)

	parentThread := deriveSessionKeyForRequest(context.Background(), parent, "api-key")
	childThread := deriveSessionKeyForRequest(context.Background(), child, "api-key")
	parentForce := deriveForceModelSessionKeyForRequest(context.Background(), parent, "api-key", parentThread)
	childForce := deriveForceModelSessionKeyForRequest(context.Background(), child, "api-key", childThread)

	assert.NotEqual(t, parentForce, childForce, "clients without a session id must remain thread-scoped")
}
