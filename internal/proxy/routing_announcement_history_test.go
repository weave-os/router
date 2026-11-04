package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

func TestRoutingAnnouncementSharedCompletionHistory(t *testing.T) {
	for _, test := range []struct {
		name      string
		completed string
		announced string
		wantBadge bool
	}{
		{name: "sibling completed a different model", completed: "gpt-6.1-terra", announced: "gpt-6-luna"},
		{name: "sibling completed the target before this thread switched", completed: "gpt-6-luna", announced: "gpt-6.1-terra", wantBadge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pins := newFakePinStore()
			pins.hasPin = true
			pins.pin = sessionpin.Pin{Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", LastServedModel: test.completed, PinnedUntil: time.Now().Add(time.Hour)}
			provider := &fakeProvider{proxyResponse: responsesTextUpstream}
			decision := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", Reason: "test"}
			svc := proxy.NewService(&fakeRouter{decision: decision}, map[string]providers.Client{providers.ProviderOpenAI: provider},
				nil, false, nil, pins, false, providers.ProviderOpenAI, "gpt-6-luna", nil)
			ctx := context.WithValue(authedCtx("00000000-0000-0000-0000-000000000001"), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{
				ClientApp: proxy.ClientAppCodex, SessionID: "synthetic-shared-conversation",
			})
			badge := codexBadgeSentinelForTest + "✦ **Weave Router** → " + test.announced + " · best pick for this turn\n\n"
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, announcementReplayBody(t, badge), rec,
				httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
			text := strings.Join(responsesTextDeltas(t, rec.Body.Bytes()), "")
			assert.Equal(t, test.wantBadge, strings.Contains(text, "✦ **Weave Router** → gpt-6-luna"))
			assert.Contains(t, text, "cached answer")
			require.NotEmpty(t, pins.usages)
			assert.Equal(t, test.completed, pins.usages[0].PriorServedModel, "notice history must not replace accounting or routing history")
		})
	}
}
