package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failover_used credits a turn to the key that served it, so it must stay
// false when the rescue also failed; failover_attempted is what shows a
// rescue was dispatched at all.
func TestSubscriptionFailover_RecordsAttemptSeparatelyFromUse(t *testing.T) {
	overloaded := upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`)
	cases := []struct {
		name          string
		subErr        error
		paidErr       error
		wantTurnErr   bool
		wantUsed      bool
		wantAttempted bool
	}{
		{name: "failed failover", subErr: overloaded, paidErr: overloaded, wantTurnErr: true, wantAttempted: true},
		{name: "served failover", subErr: overloaded, wantUsed: true, wantAttempted: true},
		{name: "no failover needed"},
	}
	for _, in := range parityIngresses() {
		for _, tc := range cases {
			t.Run(in.name+"/"+tc.name, func(t *testing.T) {
				upstream := &parityUpstream{subErr: tc.subErr, paidErr: tc.paidErr, okBody: in.upstreamOK(false)}
				svc := in.parityService(upstream)
				telemetry := &auxTelemetryRepo{}
				svc.telemetry = telemetry
				rec, req, body := in.request(t, false)

				err := in.call(svc, in.subCtx(), body, rec, req)
				if tc.wantTurnErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}

				rows := telemetry.waitForRows(1)
				require.Len(t, rows, 1)
				require.NotNil(t, rows[0].FailoverUsed)
				require.NotNil(t, rows[0].FailoverAttempted)
				assert.Equal(t, tc.wantUsed, *rows[0].FailoverUsed)
				assert.Equal(t, tc.wantAttempted, *rows[0].FailoverAttempted)
			})
		}
	}
}

// A same-cluster sibling rescue is attempted whether or not it serves, on both
// ingresses. failover_used already reads true for a failed cross-provider
// rescue (the final provider differs from the primary), so only the attempt is
// pinned on that case.
func TestSiblingRescue_RecordsAttemptSeparatelyFromUse(t *testing.T) {
	upstream502 := &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"error":{"type":"api_error","message":"bad gateway"}}`)}
	cases := []struct {
		name       string
		siblingErr error
		wantUsed   bool
	}{
		{name: "failed sibling rescue", siblingErr: upstream502},
		{name: "served sibling rescue", wantUsed: true},
	}
	ingresses := []struct {
		name string
		path string
		body []byte
		call func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{name: "messages", path: "/v1/messages", body: anthropicMessagesBody(), call: (*Service).ProxyMessages},
		{name: "chat", path: "/v1/chat/completions", body: openaiChatBody(), call: (*Service).ProxyOpenAIChatCompletion},
	}
	for _, ingress := range ingresses {
		for _, tc := range cases {
			t.Run(ingress.name+"/"+tc.name, func(t *testing.T) {
				svc := newRescuedFailureTurnService(&demotionStubPinStore{},
					rescuableDecision("hmm:authoritative model="+rescuedPrimaryModel, true),
					&failingClient{err: upstream502}, &servingClient{err: tc.siblingErr}, true)
				telemetry := &auxTelemetryRepo{}
				svc.telemetry = telemetry

				err := ingress.call(svc, rescuedFailureCtx(), ingress.body, httptest.NewRecorder(),
					httptest.NewRequest(http.MethodPost, ingress.path, strings.NewReader(string(ingress.body))))
				if tc.wantUsed {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}

				rows := telemetry.waitForRows(1)
				require.Len(t, rows, 1)
				require.NotNil(t, rows[0].FailoverAttempted)
				assert.True(t, *rows[0].FailoverAttempted)
				if tc.wantUsed {
					assert.True(t, *rows[0].FailoverUsed)
				}
			})
		}
	}
}
