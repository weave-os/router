package policyregistry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cacheAdmissionStore struct {
	calls   atomic.Int64
	enter   chan struct{}
	release chan struct{}
	err     error
}

func (s *cacheAdmissionStore) Admit(ctx context.Context, installation, key, session string, _ AdmissionDecision) (AdmissionScope, SessionReleaseBinding, error) {
	call := s.calls.Add(1)
	if s.enter != nil {
		s.enter <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return AdmissionScope{}, SessionReleaseBinding{}, ctx.Err()
		}
	}
	return AdmissionScope{InstallationID: installation, CredentialIdentity: key, Persistent: session != ""}, SessionReleaseBinding{BindingGeneration: call}, s.err
}
func TestAdmissionCacheHitsExpiryInvalidationAndCapacity(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &cacheAdmissionStore{}
	cache, err := NewAdmissionDecisionCache(store, 2, 30*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	admit := func(installation, key, session string, want int64) {
		t.Helper()
		_, binding, err := cache.Admit(context.Background(), installation, key, session, nil)
		if err != nil || binding.BindingGeneration != want {
			t.Fatalf("binding=%+v err=%v want generation %d", binding, err, want)
		}
	}
	admit("org", "key", "session", 1)
	admit("org", "key", "session", 1)
	now = now.Add(30 * time.Second)
	admit("org", "key", "session", 2)
	admit("other", "key", "session", 3)
	cache.InvalidateInstallation("org")
	admit("other", "key", "session", 3)
	admit("org", "key", "session", 4)
	admit("org", "rotated-key", "session", 5)
	admit("other", "key", "session", 6)
	cache.InvalidateAll()
	admit("other", "key", "session", 7)
	admit("other", "key", "", 8)
	admit("other", "key", "", 9)
}
func TestAdmissionCacheConcurrentMissesConverge(t *testing.T) {
	store := &cacheAdmissionStore{enter: make(chan struct{}, 100), release: make(chan struct{})}
	cache, err := NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var requests sync.WaitGroup
	for range 50 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			_, binding, err := cache.Admit(context.Background(), "org", "key", "session", nil)
			if err != nil || binding.BindingGeneration != 1 {
				t.Errorf("binding=%+v err=%v", binding, err)
			}
		}()
	}
	<-store.enter
	close(store.release)
	requests.Wait()
	if store.calls.Load() != 1 {
		t.Fatalf("primary admissions=%d", store.calls.Load())
	}
}
func TestAdmissionCacheInvalidationDuringMissDoesNotRepopulate(t *testing.T) {
	store := &cacheAdmissionStore{enter: make(chan struct{}, 1), release: make(chan struct{})}
	cache, err := NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, err := cache.Admit(context.Background(), "org", "key", "session", nil)
		if err != nil {
			t.Error(err)
		}
	}()
	<-store.enter
	cache.InvalidateInstallation("org")
	close(store.release)
	<-done
	_, binding, err := cache.Admit(context.Background(), "org", "key", "session", nil)
	if err != nil || binding.BindingGeneration != 2 {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
}

func TestAdmissionCacheInstallationInvalidationDoesNotCancelUnrelatedMiss(t *testing.T) {
	store := &cacheAdmissionStore{enter: make(chan struct{}, 2), release: make(chan struct{})}
	cache, err := NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var requests sync.WaitGroup
	for _, installation := range []string{"org-a", "org-b"} {
		installation := installation
		requests.Add(1)
		go func() {
			defer requests.Done()
			if _, _, err := cache.Admit(context.Background(), installation, "key", "session", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	<-store.enter
	<-store.enter
	cache.InvalidateInstallation("org-a")
	close(store.release)
	requests.Wait()
	if _, _, err := cache.Admit(context.Background(), "org-b", "key", "session", nil); err != nil {
		t.Fatal(err)
	}
	if store.calls.Load() != 2 {
		t.Fatalf("unrelated admission missed cache after invalidation: calls=%d", store.calls.Load())
	}
}
func TestAdmissionCacheDoesNotCacheFailures(t *testing.T) {
	store := &cacheAdmissionStore{err: errors.New("primary unavailable")}
	cache, err := NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := cache.Admit(context.Background(), "org", "key", "session", nil); !errors.Is(err, store.err) {
			t.Fatal(err)
		}
	}
	if store.calls.Load() != 2 {
		t.Fatal("failure cached")
	}
}

func TestAdmissionCacheCallerCancellationDoesNotCancelSharedBinding(t *testing.T) {
	store := &cacheAdmissionStore{enter: make(chan struct{}, 1), release: make(chan struct{})}
	cache, err := NewAdmissionDecisionCache(store, 10, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, _, err := cache.Admit(ctx, "org", "key", "session", nil); first <- err }()
	<-store.enter
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(store.release)
	_, binding, err := cache.Admit(context.Background(), "org", "key", "session", nil)
	if err != nil || binding.BindingGeneration != 1 || store.calls.Load() != 1 {
		t.Fatalf("shared binding=%+v err=%v calls=%d", binding, err, store.calls.Load())
	}
}
