package credentials_test

import (
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"

	"github.com/stretchr/testify/assert"
)

func TestCodexSpentObservationYieldsToLaterReactivation(t *testing.T) {
	observed := time.Now()
	observer := usage.NewObserver([]byte("synthetic-salt"), time.Hour, func() time.Time { return observed })
	observer.Record(observer.Key([]byte("subscription-account:synthetic-codex")), usage.Snapshot{Primary: usage.Window{UsedPercent: 1.0, WindowMinutes: 300}})
	now := observed.Add(time.Minute)
	seat := credentials.ManagedSubscriptionCandidate{AccountID: "synthetic-codex", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive, UpdatedAt: observed.Add(-time.Minute)}
	assert.True(t, credentials.IncludedQuotaObservedSpent(observer, seat, now), "an observation newer than the account state still counts")

	seat.UpdatedAt = observed.Add(30 * time.Second)
	assert.False(t, credentials.IncludedQuotaObservedSpent(observer, seat, now), "a reactivation recorded after the observation supersedes it")

	claude := seat
	claude.Provider = auth.SubscriptionProviderClaude
	assert.True(t, credentials.IncludedQuotaObservedSpent(observer, claude, now), "only Codex has a live quota check to fall back on")

	exhausted := seat
	exhausted.State = auth.SubscriptionAccountStateExhausted
	assert.True(t, credentials.IncludedQuotaObservedSpent(observer, exhausted, now))
}
