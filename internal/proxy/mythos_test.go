package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMythos52ExplicitSelectionDispatchesExactModel(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "access denied"}[denied], func(t *testing.T) {
			fresh := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Reason: "fresh"}}
			svc, upstream := newPinSvcCapturing(fresh, newFakePinStore())
			if denied {
				upstream.proxyErr = &providers.UpstreamErrorResponse{Status: http.StatusForbidden, Body: []byte(`{"error":{"type":"permission_error","message":"model access requires verification"}}`)}
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			req.Header.Set(proxy.ForceModelHeader, "claude-mythos-5.2:xhigh")
			err := svc.ProxyMessages(authedCtx(uuid.NewString()), []byte(pinTestBody), rec, req)
			if denied {
				var accessErr *providers.UpstreamErrorResponse
				require.ErrorAs(t, err, &accessErr)
				assert.Equal(t, http.StatusForbidden, accessErr.Status)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "claude-mythos-5-2", rec.Header().Get(proxy.HeaderRouterModel))
			}
			require.Len(t, upstream.proxyBodies, 1)
			assert.Equal(t, "claude-mythos-5-2", gjson.GetBytes(upstream.proxyBodies[0], "model").String())
			assert.Equal(t, "xhigh", gjson.GetBytes(upstream.proxyBodies[0], "output_config.effort").String())
			assert.Zero(t, fresh.routeCalls)
		})
	}
}
