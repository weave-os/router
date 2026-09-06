package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/translate"

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

func TestDeriveBetaSessionKeyForRequest_SharedAcrossThreadsAndDistinctFromForceModel(t *testing.T) {
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "shared-session"})
	parent, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role": "user", "content": "parent task"}]
	}`))
	require.NoError(t, err)
	child, err := translate.ParseAnthropic([]byte(`{
		"messages": [{"role": "user", "content": "compaction summary"}]
	}`))
	require.NoError(t, err)

	parentThread := deriveSessionKeyForRequest(ctx, parent, "api-key")
	childThread := deriveSessionKeyForRequest(ctx, child, "api-key")
	parentBeta := deriveBetaSessionKeyForRequest(ctx, parent, "api-key", parentThread)
	childBeta := deriveBetaSessionKeyForRequest(ctx, child, "api-key", childThread)

	assert.Equal(t, parentBeta, childBeta)
	assert.NotEqual(t, parentBeta, parentThread)
	assert.NotEqual(t, parentBeta, deriveForceModelSessionKeyForRequest(ctx, parent, "api-key", parentThread))
	assert.NotEqual(t, parentBeta, deriveBetaSessionKeyForRequest(ctx, parent, "other-api-key", parentThread))
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

func TestDeriveSessionKeyForRequest_CodexVolatileSystemPromptKeepsOneKey(t *testing.T) {
	// Codex leads with a system message, so FirstUserMessageText is empty and
	// the discriminator used to fall through to the system text — which carries
	// per-turn cwd/timestamp state. That rerolled the key (and the upstream
	// prompt_cache_key) every turn despite an unchanged Session-Id.
	turn1, err := translate.ParseOpenAI([]byte(`{
		"model": "gpt-5.6-sol",
		"messages": [
			{"role": "system", "content": "You are Codex. cwd=/repo now=10:00:01"},
			{"role": "user", "content": "deploy the stack"}
		]
	}`))
	require.NoError(t, err)
	turn2, err := translate.ParseOpenAI([]byte(`{
		"model": "gpt-5.6-sol",
		"messages": [
			{"role": "system", "content": "You are Codex. cwd=/repo/sub now=10:04:22"},
			{"role": "user", "content": "deploy the stack"},
			{"role": "assistant", "content": "running"},
			{"role": "user", "content": "status?"}
		]
	}`))
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "codex-session-xyz"})

	assert.Equal(t,
		deriveSessionKeyForRequest(ctx, turn1, "api-key"),
		deriveSessionKeyForRequest(ctx, turn2, "api-key"),
		"a rewritten system prompt must not reroll the key when the client sent a session id",
	)
}

func TestDeriveSessionKeyForRequest_SystemTextStillSplitsSessionlessConversations(t *testing.T) {
	// Without a client session id the system text is the only discriminator an
	// OpenAI-format body offers, so it must keep unrelated conversations apart.
	convoA, err := translate.ParseOpenAI([]byte(`{
		"messages": [
			{"role": "system", "content": "You are assistant A."},
			{"role": "user", "content": "task one"}
		]
	}`))
	require.NoError(t, err)
	convoB, err := translate.ParseOpenAI([]byte(`{
		"messages": [
			{"role": "system", "content": "You are assistant B."},
			{"role": "user", "content": "task two"}
		]
	}`))
	require.NoError(t, err)

	assert.NotEqual(t,
		deriveSessionKeyForRequest(context.Background(), convoA, "api-key"),
		deriveSessionKeyForRequest(context.Background(), convoB, "api-key"),
	)
}

func TestDeriveSessionKeyForRequest_SubAgentsKeepSeparatePinsUnderOneSessionID(t *testing.T) {
	// Claude Code puts the parent conversation's id on every sub-agent thread,
	// so the first user message is what stops concurrent threads from thrashing
	// one pin. Narrowing the system-text fallback must not weaken that.
	mainLoop, err := translate.ParseAnthropic([]byte(`{
		"system": "You are Claude Code. cwd=/repo",
		"messages": [{"role": "user", "content": "Refactor the dispatch loop in server.go"}]
	}`))
	require.NoError(t, err)
	subAgent, err := translate.ParseAnthropic([]byte(`{
		"system": "You are Claude Code. cwd=/repo",
		"messages": [{"role": "user", "content": "Find every .go file under internal/"}]
	}`))
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "cc-session-abc"})

	assert.NotEqual(t,
		deriveSessionKeyForRequest(ctx, mainLoop, "api-key"),
		deriveSessionKeyForRequest(ctx, subAgent, "api-key"),
		"sub-agent threads sharing a parent session id must not collapse onto one pin",
	)
}
