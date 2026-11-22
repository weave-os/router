package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func TestPiSessionRejectsInvalidProofBeforeRouting(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "shared-session"})
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, conversation.PiSessionField, prepared.SessionToken)
	require.NoError(t, err)
	parsed, _, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	claims := *conversation.PiSessionFromContext(parsed)
	sign := func(method jwt.SigningMethod, claims conversation.PiSessionClaims, secret any) string {
		t.Helper()
		token, err := jwt.NewWithClaims(method, claims).SignedString(secret)
		require.NoError(t, err)
		return token
	}
	wrongIssuer := claims
	wrongIssuer.Issuer = "weave-pi-handoff-v1"
	zeroThread := claims
	zeroThread.SessionKey = [sessionpin.SessionKeyLen]byte{}
	for _, test := range []struct {
		name, path string
		value      any
		ctx        context.Context
	}{
		{"forged signature", conversation.PiSessionField, sign(jwt.SigningMethodHS256, claims, []byte("attacker-secret")), ctx},
		{"unsigned", conversation.PiSessionField, sign(jwt.SigningMethodNone, claims, jwt.UnsafeAllowNoneSignatureType), ctx},
		{"wrong algorithm", conversation.PiSessionField, sign(jwt.SigningMethodHS384, claims, svc.lifecycle.PiHandoff(nil).Secret), ctx},
		{"wrong issuer", conversation.PiSessionField, sign(jwt.SigningMethodHS256, wrongIssuer, svc.lifecycle.PiHandoff(nil).Secret), ctx},
		{"empty thread", conversation.PiSessionField, sign(jwt.SigningMethodHS256, zeroThread, svc.lifecycle.PiHandoff(nil).Secret), ctx},
		{"wrong token purpose", conversation.PiSessionField, prepared.Token, ctx},
		{"empty", conversation.PiSessionField, "", ctx},
		{"non-string", conversation.PiSessionField, 1, ctx},
		{"other API key", conversation.PiSessionField, prepared.SessionToken, context.WithValue(ctx, requestcontext.APIKeyIDContextKey{}, "other-key")},
		{"other installation", conversation.PiSessionField, prepared.SessionToken, context.WithValue(ctx, requestcontext.InstallationIDContextKey{}, "other-installation")},
		{"other header session", conversation.PiSessionField, prepared.SessionToken, context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "other-session"})},
		{"other metadata under shared header", "metadata.user_id", "pi:other-thread", ctx},
		{"subagent under shared header", "metadata.user_id", "subagent:handoff-test", ctx},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, err := sjson.Set(body, test.path, test.value)
			require.NoError(t, err)
			for _, requestCtx := range []context.Context{test.ctx, conversation.WithHandoffPreparation(test.ctx)} {
				err = svc.ProxyMessages(requestCtx, []byte(changed), httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "POST", "/v1/messages", nil))
				require.ErrorIs(t, err, conversation.ErrHandoffInvalid)
			}
		})
	}
	require.Equal(t, 1, classifier.calls, "invalid proof must fail before classification")
	require.Empty(t, upstream.body)
}

func TestManagedPiSessionRemainsBoundToServingGeneration(t *testing.T) {
	svc, _, _, ctx := handoffTestService()
	ctx = requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{StateNamespace: "release-a-generation-1"})
	original, err := translate.ParseAnthropic([]byte(handoffTestBody))
	require.NoError(t, err)
	originalKey := deriveSessionKeyForRequest(ctx, original, "handoff-test-key")
	token, err := mintPiSession(ctx, svc.lifecycle.PiHandoff(svc.clock.Now), original, originalKey)
	require.NoError(t, err)
	body, err := sjson.Set(handoffTestBody, "messages.0.content", "Compacted review; continue the remaining checks.")
	require.NoError(t, err)
	body, err = sjson.Set(body, conversation.PiSessionField, token)
	require.NoError(t, err)
	parsed, clean, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	compacted, err := translate.ParseAnthropic(clean)
	require.NoError(t, err)
	require.Equal(t, originalKey, deriveSessionKeyForRequest(parsed, compacted, "handoff-test-key"), "do not namespace an already scoped ticket twice")
	rebound := requestcontext.WithServingIdentity(parsed, requestcontext.ServingIdentity{StateNamespace: "release-a-generation-2"})
	require.False(t, piSessionMatches(rebound, conversation.PiSessionFromContext(parsed), compacted))
	require.NotEqual(t, originalKey, deriveSessionKeyForRequest(rebound, compacted, "handoff-test-key"))
	err = svc.ProxyMessages(rebound, []byte(body), httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "POST", "/v1/messages", nil))
	require.ErrorIs(t, err, conversation.ErrHandoffInvalid, "retired tickets cannot resurrect learned state")
}

func TestPiSessionPreservesOriginalDigestAcrossCompactionAndReplicas(t *testing.T) {
	svc, _, _, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	original, err := translate.ParseAnthropic([]byte(handoffTestBody))
	require.NoError(t, err)
	body, err := sjson.Set(handoffTestBody, "messages.0.content", "Compacted review; continue the remaining checks.")
	require.NoError(t, err)
	body, err = sjson.Set(body, conversation.PiSessionField, prepared.SessionToken)
	require.NoError(t, err)
	replica, classifier, _, _ := handoffTestService()
	parsed, clean, err := replica.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	compacted, err := translate.ParseAnthropic(clean)
	require.NoError(t, err)
	require.NotEqual(t, DeriveSessionKey(original, "handoff-test-key"), DeriveSessionKey(compacted, "handoff-test-key"))
	expected := DeriveSessionKey(original, "handoff-test-key")
	require.Equal(t, expected, deriveSessionKeyForRequest(parsed, compacted, "handoff-test-key"))
	_, _, loggedKey := bindRequestLogger(parsed, compacted, "handoff-test-key", "request", "anthropic_messages")
	require.Equal(t, expected, loggedKey)
	require.NoError(t, replica.ProxyMessages(conversation.WithHandoffPreparation(ctx), []byte(body), httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "POST", "/v1/route/handoff", nil)))
	require.Equal(t, 1, classifier.calls, "session continuity must still run normal routing, not replay an old model selection")
	replica.WithPiHandoffSecret(strings.Repeat("rotated-secret-", 3))
	_, _, err = replica.parseHandoff(ctx, []byte(body))
	require.ErrorIs(t, err, conversation.ErrHandoffInvalid)
}

func TestPiSessionCannotBeMixedWithAnotherThreadsHandoff(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	svc.pinStore = newForceModelMapStore()
	first := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, "messages.0.content", "An independent thread with the same client identity.")
	require.NoError(t, err)
	otherEnv, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	otherSession, err := mintPiSession(ctx, svc.lifecycle.PiHandoff(svc.clock.Now), otherEnv, DeriveSessionKey(otherEnv, "handoff-test-key"))
	require.NoError(t, err)
	body, err = sjson.Set(body, conversation.PiSessionField, otherSession)
	require.NoError(t, err)
	body, err = sjson.Set(body, conversation.PiHandoffField, first.Token)
	require.NoError(t, err)
	err = svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "POST", "/v1/messages", nil))
	require.ErrorIs(t, err, conversation.ErrHandoffInvalid)
	require.Equal(t, 1, classifier.calls)
	require.Empty(t, upstream.body)
}
