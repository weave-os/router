package auth

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
)

// externalKeyMarkInterval bounds last_used_at writes to one per key per
// interval per replica; the dashboard shows the value at day granularity.
const externalKeyMarkInterval = time.Minute

// MarkExternalAPIKeyUsed stamps a customer provider key's last_used_at after it
// served a request. Best-effort and off the request path, like fireMarkUsed.
func (s *Service) MarkExternalAPIKeyUsed(ctx context.Context, id string) {
	if id == "" || s.externalKeys == nil {
		return
	}
	now := s.now()
	if last, ok := s.externalKeyLastMarked.Load(id); ok && now.Sub(last.(time.Time)) < externalKeyMarkInterval {
		return
	}
	s.externalKeyLastMarked.Store(id, now)
	log := observability.FromContext(ctx).With("external_api_key_id", id)
	observability.SafeGoContext(ctx, log, 2*time.Second, "markExternalAPIKeyUsed", func(ctx context.Context) {
		if err := s.externalKeys.MarkUsed(ctx, id); err != nil {
			s.externalKeyLastMarked.Delete(id)
			log.Warn("Failed to mark external api key used", "err", err)
		}
	})
}
