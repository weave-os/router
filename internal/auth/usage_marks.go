package auth

import (
	"context"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/simplelru"
	"weave-os/router/internal/observability"
)

type usageMarks struct {
	mu      sync.Mutex
	entries *simplelru.LRU[string, *time.Time]
}

func (m *usageMarks) claim(id string, now, until time.Time) *time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries, _ = simplelru.NewLRU[string, *time.Time](8192, nil)
	}
	if old, ok := m.entries.Get(id); ok && (old.IsZero() || now.Before(*old)) {
		return nil
	}
	m.entries.Add(id, &until)
	return &until
}

func (m *usageMarks) claimKey(key *APIKey, now time.Time) *time.Time {
	if key.LastUsedAt != nil && now.Before(key.LastUsedAt.Add(time.Minute)) {
		return nil
	}
	return m.claim(key.ID, now, now.Add(time.Minute))
}

func (m *usageMarks) release(id string, claim *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.entries.Peek(id); ok && current == claim {
		m.entries.Remove(id)
	}
}

func (m *usageMarks) releaseOnPanic(id string, claim *time.Time) {
	if p := recover(); p != nil {
		m.release(id, claim)
		panic(p)
	}
}

func (s *Service) fireMarkFirstRequestServed(ctx context.Context, installationID string, installation *Installation) {
	if installation != nil && installation.FirstRequestServedAt != nil {
		return
	}
	claim := s.installationUsageMarks.claim(installationID, s.now(), time.Time{})
	if claim == nil {
		return
	}
	log := observability.Get().With("installation_id", installationID)
	observability.SafeGoContext(ctx, log, 2*time.Second, "fireMarkFirstRequestServed", func(ctx context.Context) {
		defer s.installationUsageMarks.releaseOnPanic(installationID, claim)
		if err := s.installations.MarkFirstRequestServed(ctx, installationID); err != nil {
			s.installationUsageMarks.release(installationID, claim)
			log.Warn("Failed to mark installation first request served", "err", err)
		}
	})
}
