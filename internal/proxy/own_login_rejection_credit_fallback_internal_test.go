package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry/turnrecord"
)

func TestOwnSubscriptionLoginRejectionServesOnCreditsWithReconnectNotice(t *testing.T) {
	for _, in := range []parityIngress{parityAnthropicIngress()} {
		for _, stream := range []bool{false, true} {
			t.Run(in.name+"/stream="+boolLit(stream), func(t *testing.T) {
				upstream := &parityUpstream{subErr: upstreamErr(http.StatusUnauthorized, `{"error":{"type":"authentication_error"}}`), okBody: in.upstreamOK(stream)}
				svc := in.quotaService(upstream)

				for turn := range 2 {
					rec, req, body := in.request(t, stream)
					require.NoError(t, in.call(svc, in.subCtx(), body, rec, req), "turn %d", turn)
					assert.Equal(t, http.StatusOK, rec.Code, "turn %d", turn)
					if stream {
						assert.Contains(t, rec.Body.String(), "your Claude login was rejected. Reconnect your Claude seat", "turn %d must tell the user to reconnect", turn)
					}
				}
				assert.Equal(t, 1, upstream.subDispatches, "a rejected login is not retried on the next turn")
				assert.Equal(t, 2, upstream.paidDispatches, "a rejected login cannot serve, so Weave credits serve each turn")
			})
		}
	}
}

func TestRecordOwnClaudeLoginRejectedOnlyHoldsTheCallersOwnLogin(t *testing.T) {
	const ownToken = "sk-ant-oat01-synthetic-own"
	rejected := upstreamErr(http.StatusUnauthorized, `{"error":{"type":"authentication_error"}}`)
	now := time.Unix(1_800_000_000, 0)
	headers := http.Header{"Authorization": []string{"Bearer " + ownToken}}
	ownLogin := &Credentials{APIKey: []byte(ownToken), OAuth: true}
	for _, tc := range []struct {
		name  string
		creds *Credentials
		err   error
		held  bool
	}{
		{name: "own login", creds: &Credentials{APIKey: []byte(ownToken), OAuth: true}, held: true},
		{name: "managed seat", creds: &Credentials{APIKey: []byte("sk-ant-oat01-synthetic-seat"), OAuth: true, SubscriptionAccountID: "synthetic-seat"}},
		{name: "BYOK key", creds: &Credentials{APIKey: []byte(ownToken)}},
		{name: "model access denied", creds: ownLogin, err: upstreamErr(http.StatusForbidden, `{"error":{"type":"permission_error"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := usage.NewObserver([]byte("synthetic-salt"), time.Hour, func() time.Time { return now })
			ctx := context.WithValue(context.Background(), CredentialsContextKey{}, tc.creds)
			err := rejected
			if tc.err != nil {
				err = tc.err
			}
			recordOwnClaudeLoginRejected(ctx, headers, observer, now, err)
			assert.Equal(t, tc.held, ownClaudeLoginRejected(ctx, headers, observer))
		})
	}
}

func TestIncludedFailureOrNamesCodexReconnectWhenNothingElseCanServe(t *testing.T) {
	rejected := upstreamErr(http.StatusUnauthorized, `{"error":{"code":"token_revoked"}}`)
	err := includedFailureOr(rejected, router.ErrAllowlistEmptiesPool)
	class, ok := ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, class.Status)
	assert.Contains(t, class.Message, "reconnect=codex")
	assert.Equal(t, turnrecord.FailureOwnerClient, turnFailureOwnerFor(context.Background(), err, classifyTurnError(err, "", 0)), "only the caller can fix a rejected own login")
}
