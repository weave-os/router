package auth

import (
	"context"
	"sync"
	"time"

	"weave-os/router/internal/observability"
)

// externalKeyMarkInterval bounds last_used_at writes to one per key per
// interval per replica; the dashboard shows the value at day granularity.
const externalKeyMarkInterval = time.Minute

// externalKeyMarks holds each key's last claimed write slot. Entries older than
// the interval are dropped on every claim, so only recently used keys stay.
type externalKeyMarks struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (m *externalKeyMarks) claim(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if last, ok := m.last[id]; ok && now.Sub(last) < externalKeyMarkInterval {
		return false
	}
	for key, last := range m.last {
		if now.Sub(last) >= externalKeyMarkInterval {
			delete(m.last, key)
		}
	}
	if m.last == nil {
		m.last = make(map[string]time.Time)
	}
	m.last[id] = now
	return true
}

// release frees a slot whose write failed, unless a newer claim replaced it.
func (m *externalKeyMarks) release(id string, claimedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last[id].Equal(claimedAt) {
		delete(m.last, id)
	}
}

// MarkExternalAPIKeyUsed stamps a customer provider key's last_used_at after it
// served a request. Best-effort and off the request path, like fireMarkUsed.
func (s *Service) MarkExternalAPIKeyUsed(ctx context.Context, id string) {
	if s.externalKeys == nil {
		return
	}
	now := s.now()
	if !s.externalKeyMarks.claim(id, now) {
		return
	}
	log := observability.FromContext(ctx).With("external_api_key_id", id)
	observability.SafeGoContext(ctx, log, 2*time.Second, "markExternalAPIKeyUsed", func(ctx context.Context) {
		if err := s.externalKeys.MarkUsed(ctx, id); err != nil {
			s.externalKeyMarks.release(id, now)
			log.Warn("Failed to mark external api key used", "err", err)
		}
	})
}
