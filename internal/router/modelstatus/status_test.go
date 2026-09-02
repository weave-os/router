package modelstatus_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/modelstatus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreCooldownAndSuccessRecovery(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store := modelstatus.New(func() time.Time { return now }, time.Minute, 5*time.Minute, nil)
	key := modelstatus.Key{ModelID: "model", Provider: providers.ProviderOpenAI}
	store.Initialize(context.Background(), key, true)

	store.RecordOutcome(context.Background(), key, &providers.UpstreamErrorResponse{Status: 429}, false)
	entry, ok := store.Get(context.Background(), key)
	require.True(t, ok)
	assert.Equal(t, modelstatus.StatusRateLimited, entry.Status)
	assert.Equal(t, now.Add(time.Minute), entry.ExpiresAt)

	store.RecordOutcome(context.Background(), key, nil, false)
	assert.Equal(t, modelstatus.StatusOnline, store.Lookup(context.Background(), key))

	store.RecordOutcome(context.Background(), key, &providers.UpstreamErrorResponse{Status: 500}, false)
	now = now.Add(5 * time.Minute)
	entry, ok = store.Get(context.Background(), key)
	require.True(t, ok)
	assert.Equal(t, modelstatus.StatusOnline, entry.Status)
	assert.Equal(t, modelstatus.SourceAutoRecover, entry.Source)
}

func TestStoreAdminPinAndReset(t *testing.T) {
	store := modelstatus.New(time.Now, time.Minute, 5*time.Minute, nil)
	key := modelstatus.Key{ModelID: "model", Provider: providers.ProviderAnthropic}
	store.Initialize(context.Background(), key, true)
	store.SetStatus(context.Background(), key, modelstatus.StatusMaintenance, "window", modelstatus.SourceAdmin, true, 0)

	store.RecordOutcome(context.Background(), key, nil, false)
	assert.Equal(t, modelstatus.StatusMaintenance, store.Lookup(context.Background(), key))

	entry, ok := store.ResetToAuto(context.Background(), key)
	require.True(t, ok)
	assert.Equal(t, modelstatus.StatusOnline, entry.Status)
	assert.False(t, entry.AdminPinned)
}

func TestStoreBYOKDoesNotChangeGlobalStatus(t *testing.T) {
	store := modelstatus.New(time.Now, time.Minute, 5*time.Minute, nil)
	key := modelstatus.Key{ModelID: "model", Provider: providers.ProviderGoogle}
	store.Initialize(context.Background(), key, true)
	store.RecordOutcome(context.Background(), key, &providers.UpstreamErrorResponse{Status: 429}, true)
	assert.Equal(t, modelstatus.StatusOnline, store.Lookup(context.Background(), key))
}

func TestStoreConcurrentAccess(t *testing.T) {
	store := modelstatus.New(time.Now, time.Minute, 5*time.Minute, nil)
	key := modelstatus.Key{ModelID: "model", Provider: providers.ProviderOpenRouter}
	store.Initialize(context.Background(), key, true)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			store.RecordOutcome(context.Background(), key, &providers.UpstreamErrorResponse{Status: 429}, false)
		}()
		go func() {
			defer wg.Done()
			_ = store.Lookup(context.Background(), key)
		}()
	}
	wg.Wait()
	assert.NotEqual(t, modelstatus.StatusUnknown, store.Lookup(context.Background(), key))
}
