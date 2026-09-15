package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func TestPiSessionRejectsInvalidProofBeforeRouting(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "shared-session"})
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piSessionField, prepared.SessionToken)
	require.NoError(t, err)
	parsed, _, err := svc.parsePiSession(ctx, []byte(body))
	require.NoError(t, err)
	claims := *piSessionFromContext(parsed)
	sign := func(method jwt.SigningMethod, claims piSessionClaims, secret any) string {
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
		{"forged signature", piSessionField, sign(jwt.SigningMethodHS256, claims, []byte("attacker-secret")), ctx},
		{"unsigned", piSessionField, sign(jwt.SigningMethodNone, claims, jwt.UnsafeAllowNoneSignatureType), ctx},
		{"wrong algorithm", piSessionField, sign(jwt.SigningMethodHS384, claims, svc.piHandoffSecret), ctx},
		{"wrong issuer", piSessionField, sign(jwt.SigningMethodHS256, wrongIssuer, svc.piHandoffSecret), ctx},
		{"empty thread", piSessionField, sign(jwt.SigningMethodHS256, zeroThread, svc.piHandoffSecret), ctx},
		{"wrong token purpose", piSessionField, prepared.Token, ctx},
		{"empty", piSessionField, "", ctx},
		{"non-string", piSessionField, 1, ctx},
		{"other API key", piSessionField, prepared.SessionToken, context.WithValue(ctx, APIKeyIDContextKey{}, "other-key")},
		{"other installation", piSessionField, prepared.SessionToken, context.WithValue(ctx, InstallationIDContextKey{}, "other-installation")},
		{"other header session", piSessionField, prepared.SessionToken, context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "other-session"})},
		{"other metadata under shared header", "metadata.user_id", "pi:other-thread", ctx},
		{"subagent under shared header", "metadata.user_id", "subagent:handoff-test", ctx},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, err := sjson.Set(body, test.path, test.value)
			require.NoError(t, err)
			for _, requestCtx := range []context.Context{test.ctx, WithHandoffPreparation(test.ctx)} {
				err = svc.ProxyMessages(requestCtx, []byte(changed), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
				require.ErrorIs(t, err, ErrHandoffInvalid)
			}
		})
	}
	require.Equal(t, 1, classifier.calls, "invalid proof must fail before classification")
	require.Empty(t, upstream.body)
}

func TestPiSessionPreservesOriginalDigestAcrossCompactionAndReplicas(t *testing.T) {
	svc, _, _, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	original, err := translate.ParseAnthropic([]byte(handoffTestBody))
	require.NoError(t, err)
	body, err := sjson.Set(handoffTestBody, "messages.0.content", "Compacted review; continue the remaining checks.")
	require.NoError(t, err)
	body, err = sjson.Set(body, piSessionField, prepared.SessionToken)
	require.NoError(t, err)
	replica, classifier, _, _ := handoffTestService()
	parsed, clean, err := replica.parsePiSession(ctx, []byte(body))
	require.NoError(t, err)
	compacted, err := translate.ParseAnthropic(clean)
	require.NoError(t, err)
	require.NotEqual(t, DeriveSessionKey(original, "handoff-test-key"), DeriveSessionKey(compacted, "handoff-test-key"))
	expected := DeriveSessionKey(original, "handoff-test-key")
	require.Equal(t, expected, deriveSessionKeyForRequest(parsed, compacted, "handoff-test-key"))
	_, _, loggedKey := bindRequestLogger(parsed, compacted, "handoff-test-key", "request", "anthropic_messages")
	require.Equal(t, expected, loggedKey)
	require.NoError(t, replica.ProxyMessages(WithHandoffPreparation(ctx), []byte(body), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/route/handoff", nil)))
	require.Equal(t, 1, classifier.calls, "session continuity must still run normal routing, not replay an old model selection")
	replica.WithPiHandoffSecret(strings.Repeat("rotated-secret-", 3))
	_, _, err = replica.parsePiSession(ctx, []byte(body))
	require.ErrorIs(t, err, ErrHandoffInvalid)
}

func TestPiSessionCannotBeMixedWithAnotherThreadsHandoff(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	svc.pinStore = newForceModelMapStore()
	first := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, "messages.0.content", "An independent thread with the same client identity.")
	require.NoError(t, err)
	otherEnv, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	otherSession, err := svc.mintPiSession(ctx, otherEnv, DeriveSessionKey(otherEnv, "handoff-test-key"))
	require.NoError(t, err)
	body, err = sjson.Set(body, piSessionField, otherSession)
	require.NoError(t, err)
	body, err = sjson.Set(body, piHandoffField, first.Token)
	require.NoError(t, err)
	err = svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
	require.ErrorIs(t, err, ErrHandoffInvalid)
	require.Equal(t, 1, classifier.calls)
	require.Empty(t, upstream.body)
}
