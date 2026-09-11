package proxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

type deadlinePinStore struct {
	recordingPinStore
	unavailable bool
	usage       []sessionpin.Usage
}

func (s *deadlinePinStore) Upsert(ctx context.Context, pin sessionpin.Pin) error {
	if s.unavailable {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.recordingPinStore.Upsert(ctx, pin)
}

func (s *deadlinePinStore) UpdateUsage(ctx context.Context, _ [sessionpin.SessionKeyLen]byte, _ string, usage sessionpin.Usage) error {
	if s.unavailable {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.usage = append(s.usage, usage)
	return nil
}

func TestSessionBookkeepingTimesOutAndRecoversAfterClientCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &deadlinePinStore{unavailable: true}
		service := &Service{pinStore: store}
		key := [sessionpin.SessionKeyLen]byte{1}
		installation := uuid.New()
		started := time.Now()
		err := service.setForceModelSessionPin(context.Background(), key, installation, "claude-opus-4-7", providers.ProviderAnthropic, "")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.LessOrEqual(t, time.Since(started), 300*time.Millisecond)
		assert.Empty(t, store.upserts)
		store.unavailable = false
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.NoError(t, service.setForceModelSessionPin(ctx, key, installation, "claude-opus-4-7", providers.ProviderAnthropic, ""))
		require.Len(t, store.upserts, 1)
		assert.Equal(t, "claude-opus-4-7", store.upserts[0].Model)
		assert.Equal(t, installation, store.upserts[0].InstallationID)
	})
}

func TestTurnUsageWriteIsBoundedAndRetainsCompletedUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &deadlinePinStore{unavailable: true}
		service := &Service{pinStore: store}
		turn := turnLoopResult{SessionKey: [sessionpin.SessionKeyLen]byte{1}, Strategy: router.StrategyCluster}
		started := time.Now()
		service.recordTurnUsage(context.Background(), turn, providers.ProviderAnthropic, "claude-opus-4-7", 12, 9, 0, 0)
		assert.LessOrEqual(t, time.Since(started), 300*time.Millisecond)
		assert.Empty(t, store.usage)
		store.unavailable = false
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		service.recordTurnUsage(ctx, turn, providers.ProviderAnthropic, "claude-opus-4-7", 12, 9, 3, 5)
		require.Len(t, store.usage, 1)
		assert.Equal(t, 12, store.usage[0].InputTokens)
		assert.Equal(t, 9, store.usage[0].OutputTokens)
		assert.Equal(t, 3, store.usage[0].CachedWriteTokens)
		assert.Equal(t, 5, store.usage[0].CachedReadTokens)
		assert.Equal(t, "claude-opus-4-7", store.usage[0].ServedModel)
	})
}

func TestNestedBookkeepingKeepsTheOriginalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		outer, cancelOuter := bookkeepingContext(context.Background())
		defer cancelOuter()
		time.Sleep(200 * time.Millisecond)
		inner, cancelInner := bookkeepingContext(outer)
		defer cancelInner()
		<-inner.Done()
		assert.Equal(t, 250*time.Millisecond, time.Since(started))
		assert.ErrorIs(t, inner.Err(), context.DeadlineExceeded)
	})
}
