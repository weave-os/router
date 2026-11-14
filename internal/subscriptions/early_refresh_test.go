package subscriptions

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestEarlyRefreshDeadlineIsOptInAndNeverExtendsExpiry(t *testing.T) {
	issued := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issued)}).
		SignedString([]byte("synthetic-test-signing-key"))
	require.NoError(t, err)
	policy, err := NewEarlyRefreshPolicy([]string{"selected-seat"}, 6*24*time.Hour)
	require.NoError(t, err)
	for _, scenario := range []struct {
		name, id, token string
		provider        Provider
		expiry, want    time.Time
	}{
		{name: "enrolled Codex", id: "selected-seat", token: token, provider: ProviderCodex, expiry: issued.Add(10 * 24 * time.Hour), want: issued.Add(6 * 24 * time.Hour)},
		{name: "unselected Codex", id: "other-seat", token: token, provider: ProviderCodex, expiry: issued.Add(10 * 24 * time.Hour), want: issued.Add(10 * 24 * time.Hour)},
		{name: "other provider", id: "selected-seat", token: token, provider: ProviderClaude, expiry: issued.Add(10 * 24 * time.Hour), want: issued.Add(10 * 24 * time.Hour)},
		{name: "earlier actual expiry", id: "selected-seat", token: token, provider: ProviderCodex, expiry: issued.Add(time.Hour), want: issued.Add(time.Hour)},
		{name: "unknown issuance", id: "selected-seat", token: "synthetic-opaque-token", provider: ProviderCodex, expiry: issued.Add(time.Hour), want: issued.Add(time.Hour)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			require.Equal(t, scenario.want, policy.deadline(Account{ID: scenario.id, Provider: scenario.provider}, scenario.token, scenario.expiry, nil))
		})
	}
	require.Equal(t, issued.Add(10*24*time.Hour), (EarlyRefreshPolicy{}).deadline(Account{ID: "selected-seat", Provider: ProviderCodex}, token, issued.Add(10*24*time.Hour), nil))
	refreshed := issued.Add(6 * 24 * time.Hour)
	require.Equal(t, issued.Add(12*24*time.Hour), policy.deadline(Account{ID: "selected-seat", Provider: ProviderCodex}, token,
		issued.Add(16*24*time.Hour), &refreshed), "a persisted refresh date takes precedence when the issuer preserves iat")
}
