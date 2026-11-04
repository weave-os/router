package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

func TestOwnCodexPlanSpentNudgeShowsOncePerSession(t *testing.T) {
	ctx := withSubscriptionReconnectNudge(context.Background(), ownCodexPlanSpentNudge)

	first := withSubscriptionOnlyWarningEcho(ctx, []byte(`{"input":[{"role":"user","content":"hi"}]}`))
	notice := routerNoticeForRequest(first, http.Header{}, subscriptions.ProviderCodex, "gpt-6", "✦ **Weave Router** → gpt-6\n\n")
	require.Equal(t, translate.NoticeSubscriptionReconnect, notice.Kind)
	require.Contains(t, notice.Text, SubscriptionsDashboardURL+"&reconnect=codex", "the link starts the Codex login")

	history, err := json.Marshal(map[string]any{"input": []map[string]any{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": ownCodexPlanSpentNudge + "hello"}}},
		{"role": "user", "content": "again"},
	}})
	require.NoError(t, err)
	later := withSubscriptionOnlyWarningEcho(ctx, history)
	notice = routerNoticeForRequest(later, http.Header{}, subscriptions.ProviderCodex, "gpt-6", "✦ **Weave Router** → gpt-6\n\n")
	require.Equal(t, translate.NoticeModelSelection, notice.Kind, "an echoed nudge is not repeated")
}

func TestRoutingMarkerUnchangedWithoutReconnectNudge(t *testing.T) {
	ctx := withSubscriptionOnlyWarningEcho(context.Background(), []byte(`{}`))
	notice := routerNoticeForRequest(ctx, http.Header{}, subscriptions.ProviderCodex, "gpt-6", "marker")
	require.Equal(t, translate.NoticeModelSelection, notice.Kind)
	require.Equal(t, "marker", notice.Text)
}
