package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// codexCtx stands in for a request that arrived with `X-App: codex`.
func codexCtx(sessionID string) context.Context {
	return context.WithValue(context.Background(), ClientIdentityContextKey{},
		ClientIdentity{ClientApp: ClientAppCodex, SessionID: sessionID})
}

// The reported bug: Codex users were told "Use /unforce-model to clear", and
// Codex answers "Unrecognized command '/unforce-model'" because it never
// forwards a leading slash. The ack must name the form that actually works.
func TestForceModelCommand_CodexAckNamesTheDollarForm(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(codexCtx("sess-1"), rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	body := rec.Body.String()
	require.Contains(t, body, "force-model applied")
	assert.Contains(t, body, "$unforce-model")
	assert.NotContains(t, body, "/unforce-model")
}

// Claude Code expands and forwards slash commands, so it must keep them.
func TestForceModelCommand_ClaudeCodeAckKeepsTheSlashForm(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{},
		ClientIdentity{ClientApp: ClientAppClaudeCode})
	require.NoError(t, svc.handleForceModelCommand(ctx, rec, env,
		translate.ForceModelResult{Model: "opus"},
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Contains(t, rec.Body.String(), "/unforce-model")
}

// router-session used to reach no handler at all, so it fell through to a
// routed turn: the model read the skill, shelled out to emit.sh, and echoed
// the result. It must answer from the router's own identity instead, with no
// upstream and no pin store touched.
func TestRouterSessionCommand_AnswersSyntheticallyFromRequestIdentity(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleRouterSessionCommand(
		codexCtx("01a08ca1-6f83-7233-9e53-bc6c07d2a805"), rec, env, 10))

	assert.Contains(t, rec.Body.String(), "01a08ca1-6f83-7233-9e53-bc6c07d2a805")
	assert.Empty(t, store.upserts, "reporting the session id must not touch routing state")
}

// Codex answers "Unrecognized command '/unforce-model'" and never forwards the
// line, so a hint printed to a Codex user has to name the $ form. Every other
// client sends "/" through.
func TestDirectivePrefix(t *testing.T) {
	assert.Equal(t, "$", directivePrefix(ClientAppCodex))

	for _, app := range []string{
		ClientAppClaudeCode,
		ClientAppCursor,
		ClientAppGeminiCLI,
		ClientAppOpencode,
		"",
		"something-unknown",
	} {
		assert.Equal(t, "/", directivePrefix(app), "expected slash for %q", app)
	}
}

func TestRouterSessionMessage_NamesTheIDAndTheClientsOwnSigil(t *testing.T) {
	codex := routerSessionMessage("01a08ca1-6f83-7233", ClientAppCodex, translate.FormatOpenAI)
	assert.Contains(t, codex, "01a08ca1-6f83-7233")
	assert.Contains(t, codex, "$router-feedback")
	assert.NotContains(t, codex, "/router-feedback")

	claude := routerSessionMessage("sess-abc", ClientAppClaudeCode, translate.FormatAnthropic)
	assert.Contains(t, claude, "sess-abc")
	assert.Contains(t, claude, "/router-feedback")
	// Anthropic-format acks are formatted as routing markers so
	// StripRoutingMarkerFromMessages removes them from later requests.
	assert.Contains(t, claude, routingMarkerPrefix)
}

// Naming a substitute id would send support at the wrong session, so an
// absent id has to read as absent.
func TestRouterSessionMessage_MissingIDSaysSoRatherThanGuessing(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatOpenAI, translate.FormatAnthropic} {
		msg := routerSessionMessage("", ClientAppCodex, format)
		assert.Contains(t, msg, "no session id")
		assert.NotContains(t, msg, "session id: ")
	}
}
