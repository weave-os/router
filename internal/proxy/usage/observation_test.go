package usage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservationRecoveryFencesOnlyOlderAccountEvidence(t *testing.T) {
	now := time.Now()
	observer := NewObserver([]byte("recovery-test"), time.Minute, func() time.Time { return now })
	account, alias, other := observer.Key([]byte("account")), observer.Key([]byte("alias")), observer.Key([]byte("other"))
	oldGeneration, newGeneration := int64(0), int64(1)
	old := observer.BeginObservation(account, &oldGeneration, alias)
	peer := observer.BeginObservation(other, &oldGeneration)
	observer.Recover(account, newGeneration)
	exhausted := Snapshot{Primary: Window{UsedPercent: 1, WindowMinutes: 1}}
	old.Record(exhausted)
	old.Close()
	_, found := observer.Snapshot(account)
	require.False(t, found)
	_, found = observer.Snapshot(alias)
	require.False(t, found)
	peer.Record(exhausted)
	peer.Close()
	_, found = observer.Snapshot(other)
	require.True(t, found, "a different account's response remains authoritative")
	fresh := observer.BeginObservation(account, &newGeneration, alias)
	fresh.Record(Snapshot{Primary: Window{UsedPercent: 0.1, WindowMinutes: 1}})
	fresh.Close()
	observer.Recover(account, newGeneration)
	late := observer.BeginObservation(account, &oldGeneration, alias)
	late.Record(exhausted)
	late.Close()
	snapshot, found := observer.Snapshot(account)
	require.True(t, found)
	require.Equal(t, 0.1, snapshot.Primary.UsedPercent, "equal recovery generations retain fresh evidence and old leases cannot replace it")
	now = now.Add(2 * time.Minute)
	observer.Sweep()
	require.Empty(t, observer.observations, "finished epochs must not accumulate after their quota observations expire")
}

func TestObservationWithoutHeadersReleasesEpoch(t *testing.T) {
	observer := NewObserver([]byte("no-header-test"), time.Minute, time.Now)
	observation := observer.BeginObservation(observer.Key([]byte("account")), nil)
	observation.Close()
	observation.Close()
	observation.Record(Snapshot{Primary: Window{UsedPercent: 1, WindowMinutes: 1}})
	require.Empty(t, observer.observations)
	require.Empty(t, observer.data)
}
