package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Synthetic Claude Code system prompt: a plain instruction block followed by
// the gitStatus block as a separate content block, the shape Claude Code sends.
const clientGitContextBody = `{
	"model":"claude-opus-4-7",
	"system":[
		{"type":"text","text":"You are Claude Code."},
		{"type":"text","text":"gitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.\nCurrent branch: feature/synthetic-branch\n\nMain branch (you will usually use this for PRs): main\n\nGit user: Synthetic User\n\nStatus:\n M internal/example.go\n?? notes.txt\n\nRecent commits:\nabc1234 synthetic subject one\ndef5678 synthetic subject two"}
	],
	"messages":[{"role":"user","content":"original prompt"}]
}`

func trialCtx(installationID string) context.Context {
	return context.WithValue(authedCtx(installationID), proxy.InstallationTrialCaptureContextKey{}, true)
}

func proxyClientGitContextTurn(t *testing.T, ctx context.Context, store *fakePinStore) proxy.InsertTelemetryParams {
	t.Helper()
	telem := newCaptureTelemetry()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Reason: "cluster:v0.2"}}
	svc := newPinSvcWithTelemetry(fr, store, telem)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(clientGitContextBody), rec, httpReq))
	return telem.firstRow(t)
}

func assertClientGitContextNull(t *testing.T, row proxy.InsertTelemetryParams) {
	t.Helper()
	assert.Empty(t, row.ClientGitHeadSHA)
	assert.Empty(t, row.ClientGitBranch)
	assert.Nil(t, row.ClientGitDirty)
}

func TestProxyMessages_ClientGitContext_TrialFirstTurnStamps(t *testing.T) {
	row := proxyClientGitContextTurn(t, trialCtx(uuid.New().String()), newFakePinStore())

	assert.Equal(t, "abc1234", row.ClientGitHeadSHA)
	assert.Equal(t, "feature/synthetic-branch", row.ClientGitBranch)
	require.NotNil(t, row.ClientGitDirty)
	assert.True(t, *row.ClientGitDirty)
}

func TestProxyMessages_ClientGitContext_NonTrialInstallationLeavesNulls(t *testing.T) {
	row := proxyClientGitContextTurn(t, authedCtx(uuid.New().String()), newFakePinStore())
	assertClientGitContextNull(t, row)
}

func TestProxyMessages_ClientGitContext_SecondTurnLeavesNulls(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderAnthropic,
		Model:         "claude-haiku-4-5",
		Reason:        "cluster:v0.2",
		PinnedUntil:   time.Now().Add(30 * time.Minute),
		FirstPinnedAt: time.Now().Add(-5 * time.Minute),
	}
	row := proxyClientGitContextTurn(t, trialCtx(uuid.New().String()), store)
	assertClientGitContextNull(t, row)
}

func TestProxyMessages_ClientGitContext_PinStoreErrorLeavesNulls(t *testing.T) {
	store := newFakePinStore()
	store.getErr = errors.New("postgres unreachable")
	row := proxyClientGitContextTurn(t, trialCtx(uuid.New().String()), store)
	assertClientGitContextNull(t, row)
}

func TestProxyMessages_ClientGitContext_ExpiredPinIsNotFirstTurn(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderAnthropic,
		Model:         "claude-haiku-4-5",
		Reason:        "cluster:v0.2",
		PinnedUntil:   time.Now().Add(-time.Minute),
		FirstPinnedAt: time.Now().Add(-time.Hour),
	}
	row := proxyClientGitContextTurn(t, trialCtx(uuid.New().String()), store)
	assertClientGitContextNull(t, row)
}
