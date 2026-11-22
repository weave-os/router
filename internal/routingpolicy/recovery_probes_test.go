package routingpolicy_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

func TestRecoveryProbeLeasesAdmitOneLocalProbeUntilReleaseOrExpiry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := func() time.Time { return now }
	var leases routingpolicy.RecoveryProbeLeases
	key := [sessionpin.SessionKeyLen]byte{1}

	_, acquired := leases.Acquire(context.Background(), newMemPinStore(), clock, key, "model-a")
	require.True(t, acquired)
	_, acquired = leases.Acquire(context.Background(), newMemPinStore(), clock, key, "model-a")
	assert.False(t, acquired, "a live lease admits one probe")
	_, acquired = leases.Acquire(context.Background(), newMemPinStore(), clock, key, "model-b")
	assert.True(t, acquired, "leases are per model")

	now = now.Add(routingpolicy.RecoveryProbeLeaseDuration)
	release, acquired := leases.Acquire(context.Background(), newMemPinStore(), clock, key, "model-a")
	require.True(t, acquired, "an abandoned lease frees after its safety duration")
	release()
	_, acquired = leases.Acquire(context.Background(), newMemPinStore(), clock, key, "model-a")
	assert.True(t, acquired, "a released lease frees immediately")
}

func TestRecoveryProbeLeasesAdmitUnkeyedProbes(t *testing.T) {
	var leases routingpolicy.RecoveryProbeLeases
	release, acquired := leases.Acquire(context.Background(), newMemPinStore(), time.Now, [sessionpin.SessionKeyLen]byte{}, "model-a")
	require.True(t, acquired)
	release()
}

func TestRecoveryProbeLeaseForCoversTheRequestDeadline(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	assert.Equal(t, routingpolicy.RecoveryProbeLeaseDuration, routingpolicy.RecoveryProbeLeaseFor(context.Background(), now))

	ctx, cancel := context.WithDeadline(context.Background(), now.Add(30*time.Minute))
	defer cancel()
	assert.Equal(t, 31*time.Minute, routingpolicy.RecoveryProbeLeaseFor(ctx, now))
}
