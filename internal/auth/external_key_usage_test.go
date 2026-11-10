package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
)

type markRecordingExternalKeyRepo struct {
	fakeExternalAPIKeyRepo
	marked chan string
	err    error
}

func (r *markRecordingExternalKeyRepo) MarkUsed(_ context.Context, id string) error {
	r.marked <- id
	return r.err
}

func TestMarkExternalAPIKeyUsed_ThrottlesWritesPerKey(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	repo := &markRecordingExternalKeyRepo{marked: make(chan string, 4)}
	svc := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{}, repo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now })

	svc.MarkExternalAPIKeyUsed(ctx, "key-1")
	require.Equal(t, "key-1", receiveMarked(t, repo.marked))

	svc.MarkExternalAPIKeyUsed(ctx, "key-1")
	svc.MarkExternalAPIKeyUsed(ctx, "key-2")
	require.Equal(t, "key-2", receiveMarked(t, repo.marked))

	now = now.Add(2 * time.Minute)
	svc.MarkExternalAPIKeyUsed(ctx, "key-1")
	require.Equal(t, "key-1", receiveMarked(t, repo.marked))
	assert.Empty(t, repo.marked)
}

func TestMarkExternalAPIKeyUsed_ConcurrentCallsWriteOnce(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	repo := &markRecordingExternalKeyRepo{marked: make(chan string, 64)}
	svc := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{}, repo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now })

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.MarkExternalAPIKeyUsed(context.Background(), "key-1")
		}()
	}
	wg.Wait()

	require.Equal(t, "key-1", receiveMarked(t, repo.marked))
	assert.Never(t, func() bool { return len(repo.marked) > 0 }, 100*time.Millisecond, 10*time.Millisecond)
}

func TestMarkExternalAPIKeyUsed_FailedWriteFreesSlot(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	repo := &markRecordingExternalKeyRepo{marked: make(chan string, 4), err: errors.New("db down")}
	svc := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{}, repo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now })

	svc.MarkExternalAPIKeyUsed(context.Background(), "key-1")
	require.Equal(t, "key-1", receiveMarked(t, repo.marked))

	require.Eventually(t, func() bool {
		svc.MarkExternalAPIKeyUsed(context.Background(), "key-1")
		select {
		case <-repo.marked:
			return true
		default:
			return false
		}
	}, 2*time.Second, 10*time.Millisecond)
}

func receiveMarked(t *testing.T, marked <-chan string) string {
	t.Helper()
	select {
	case id := <-marked:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("external key was not marked used")
		return ""
	}
}
