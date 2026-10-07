package proxy

import (
	"net/http"
	"testing"

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
