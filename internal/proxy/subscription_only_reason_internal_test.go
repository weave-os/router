package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/subscriptions"

	"github.com/stretchr/testify/assert"
)

// The subscription-only warning replaces the routing marker, so it must name
// the gate that actually disabled paid fallback — a reached spend cap is not a
// depleted balance — and stay off the linked-first path every subscriber takes.
func TestSubscriptionOnlyWarningsForReason(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"not subscription-only": {context.Background(), ""},
		"credits depleted": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted),
			subscriptionOnlyWarningMarker,
		},
		"spend cap reached": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlySpendCapReached),
			subscriptionSpendCapWarningMarker,
		},
		"linked first": {
			billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyLinkedFirst),
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, routerNoticeForRequest(tc.ctx, http.Header{}, subscriptions.ProviderClaude, "model", "").Text)
		})
	}
}

func TestSpendCapWarningDoesNotClaimDepletedCredits(t *testing.T) {
	for _, warning := range []string{subscriptionSpendCapWarningMarker, subscriptionSpendCapWarningMarkerCodex} {
		assert.NotContains(t, warning, "credits")
		assert.NotContains(t, warning, topUpURL)
		assert.Contains(t, warning, "spend cap")
	}
}

func TestSubscriptionOnlyUnavailableNamesTheGate(t *testing.T) {
	capCtx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlySpendCapReached)
	assert.Equal(t, ErrSpendCapSubscriptionUnavailable, subscriptionOnlyUnavailable(capCtx))
	assert.ErrorIs(t, subscriptionOnlyUnavailable(capCtx), ErrCreditsExhaustedSubscriptionUnavailable,
		"refusal checks match the credits sentinel, so the cap variant must wrap it")

	depletedCtx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	assert.Equal(t, ErrCreditsExhaustedSubscriptionUnavailable, subscriptionOnlyUnavailable(depletedCtx))
}

func TestSubscriptionOnlyWarningShowsOncePerConversation(t *testing.T) {
	capCtx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlySpendCapReached)
	fresh := withSubscriptionOnlyWarningEcho(capCtx, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	assert.Equal(t, subscriptionSpendCapWarningMarker, routerNoticeForRequest(fresh, http.Header{}, subscriptions.ProviderClaude, "model", "").Text)

	shownBody := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":` +
		jsonString(subscriptionSpendCapWarningMarker+"hello") + `}]},{"role":"user","content":"again"}]}`)
	shown := withSubscriptionOnlyWarningEcho(capCtx, shownBody)
	assert.Empty(t, routerNoticeForRequest(shown, http.Header{}, subscriptions.ProviderClaude, "model", "").Text,
		"a warning the conversation already shows must not repeat")

	depleted := billing.WithSubscriptionOnly(shown, billing.SubscriptionOnlyCreditsDepleted)
	assert.Equal(t, subscriptionOnlyWarningMarker, routerNoticeForRequest(depleted, http.Header{}, subscriptions.ProviderClaude, "model", "").Text,
		"a changed reason is new information and must show")

	delegated := withSubscriptionOnlyWarningEcho(shown, []byte(`{"messages":[{"role":"user","content":"stripped"}]}`))
	assert.Empty(t, routerNoticeForRequest(delegated, http.Header{}, subscriptions.ProviderClaude, "model", "").Text,
		"a later stripped body must not forget an earlier echo")
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
