package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// codexCtx stands in for a request that arrived with `X-App: codex`.
func codexCtx(sessionID string) context.Context {
	return context.WithValue(context.Background(), ClientIdentityContextKey{},
		ClientIdentity{ClientApp: requestcontext.ClientAppCodex, SessionID: sessionID})
}

// The reported bug: Codex users were told "Use /unforce-model to clear", and
// Codex answers "Unrecognized command '/unforce-model'" because it never
// forwards a leading slash. The ack must name the form that actually works.
func TestForceModelCommand_CodexAckNamesTheDollarForm(t *testing.T) {
	store := &recordingPinStore{}
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(nil), nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, (forceModelCommands{svc}).handle(codexCtx("sess-1"), rec, env,
		translate.ForceModelResult{Model: "opus"}, sessionTarget{strategy: router.StrategyFromContext(codexCtx("sess-1")), installationID: uuid.New(), sessionKey: DeriveSessionKey(env, "key-1")},

		DeriveSessionKey(env, "key-1"), 10))

	body := rec.Body.String()
	require.Contains(t, body, "force-model applied")
	assert.Contains(t, body, "$unforce-model")
	assert.NotContains(t, body, "/unforce-model")
}

// Claude Code expands and forwards slash commands, so it must keep them.
func TestForceModelCommand_ClaudeCodeAckKeepsTheSlashForm(t *testing.T) {
	store := &recordingPinStore{}
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(nil), nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{},
		ClientIdentity{ClientApp: requestcontext.ClientAppClaudeCode})
	require.NoError(t, (forceModelCommands{svc}).handle(ctx, rec, env,
		translate.ForceModelResult{Model: "opus"}, sessionTarget{strategy: router.StrategyFromContext(ctx), installationID: uuid.New(), sessionKey: DeriveSessionKey(env, "key-1")},

		DeriveSessionKey(env, "key-1"), 10))

	assert.Contains(t, rec.Body.String(), "/unforce-model")
}

// router-session used to reach no handler at all, so it fell through to a
// routed turn: the model read the skill, shelled out to emit.sh, and echoed
// the result. It must answer from the router's own identity instead, with no
// upstream and no pin store touched.
func TestRouterSessionCommand_AnswersSyntheticallyFromRequestIdentity(t *testing.T) {
	store := &recordingPinStore{}
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(nil), nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil))

	env := forceCommandEnv(t)
	rec := httptest.NewRecorder()
	require.NoError(t, (routerInfoCommands{svc}).session(
		codexCtx("01a08ca1-6f83-7233-9e53-bc6c07d2a805"), rec, env, 10))

	assert.Contains(t, rec.Body.String(), "01a08ca1-6f83-7233-9e53-bc6c07d2a805")
	assert.Empty(t, store.upserts, "reporting the session id must not touch routing state")
}

// Codex answers "Unrecognized command '/unforce-model'" and never forwards the
// line, so a hint printed to a Codex user has to name the $ form. Every other
// client sends "/" through.
func TestDirectivePrefix(t *testing.T) {
	assert.Equal(t, "$", requestcontext.DirectivePrefix(requestcontext.ClientAppCodex))

	for _, app := range []string{
		requestcontext.ClientAppClaudeCode,
		requestcontext.ClientAppCursor,
		requestcontext.ClientAppGeminiCLI,
		requestcontext.ClientAppOpencode,
		"",
		"something-unknown",
	} {
		assert.Equal(t, "/", requestcontext.DirectivePrefix(app), "expected slash for %q", app)
	}
}

func TestSupportsResponsesTerminalSurfaces(t *testing.T) {
	assert.True(t, requestcontext.SupportsResponsesTerminalSurfaces(requestcontext.ClientAppCodex))
	assert.True(t, requestcontext.SupportsResponsesTerminalSurfaces(requestcontext.ClientAppOpencode))
	assert.False(t, requestcontext.SupportsResponsesTerminalSurfaces(requestcontext.ClientAppCursor))
	assert.False(t, requestcontext.SupportsResponsesTerminalSurfaces(""))
}

func TestRouterSessionMessage_NamesTheIDAndTheClientsOwnSigil(t *testing.T) {
	codex := conversation.RouterSessionMessage("01a08ca1-6f83-7233", requestcontext.DirectivePrefix(requestcontext.ClientAppCodex), translate.FormatOpenAI)
	assert.Contains(t, codex, "01a08ca1-6f83-7233")
	assert.Contains(t, codex, "$router-feedback")
	assert.NotContains(t, codex, "/router-feedback")

	claude := conversation.RouterSessionMessage("sess-abc", requestcontext.DirectivePrefix(requestcontext.ClientAppClaudeCode), translate.FormatAnthropic)
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
		msg := conversation.RouterSessionMessage("", requestcontext.DirectivePrefix(requestcontext.ClientAppCodex), format)
		assert.Contains(t, msg, "no session id")
		assert.NotContains(t, msg, "session id: ")
	}
}
