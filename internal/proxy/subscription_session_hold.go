package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
)

// subscriptionSessionHold keeps a session off an included model whose
// subscription pool just failed on provider capacity. The agent-turn strike
// clears at the next user prompt, which sent such sessions straight back to
// the overloaded pool every prompt. Per replica, in memory.
const subscriptionSessionHold = 5 * time.Minute

func sessionHoldOwner(ctx context.Context) string {
	session := ClientIdentityFrom(ctx).SessionID
	if session == "" {
		return ""
	}
	installation, _ := ctx.Value(InstallationIDContextKey{}).(string)
	return "session-hold:" + installation + "\x00" + session
}

func (s *Service) holdSessionOffSubscriptionModel(ctx context.Context, provider, model string) {
	owner := sessionHoldOwner(ctx)
	if owner == "" || !s.subscriptionPoolUnavailable(ctx, provider, model) {
		return
	}
	s.subscriptionModels.denyManaged(owner, "", "", model, s.clockNow().Add(subscriptionSessionHold))
	observability.FromContext(ctx).Info("Session held off subscription model after pool capacity failure", "model", model, "hold", subscriptionSessionHold)
}

// withoutHeldSubscriptionModels drops held models unless that would leave no
// included model to try.
func (s *Service) withoutHeldSubscriptionModels(ctx context.Context, models []string) []string {
	owner := sessionHoldOwner(ctx)
	if owner == "" {
		return models
	}
	now := s.clockNow()
	kept := make([]string, 0, len(models))
	for _, model := range models {
		if !s.subscriptionModels.managedDenied(owner, "", "", model, now) {
			kept = append(kept, model)
		}
	}
	if len(kept) == 0 {
		return models
	}
	return kept
}
