package proxy

import (
	"context"
	"slices"
	"strings"
	"sync"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

// turnFailures collects the models that failed during one request and were
// handed to another target, so completion can keep them out of the session
// for the rest of the agent turn.
type turnFailures struct {
	mu     sync.Mutex
	models []string
}

type turnFailuresContextKey struct{}

func withTurnFailures(ctx context.Context) context.Context {
	if turnFailuresFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, turnFailuresContextKey{}, &turnFailures{})
}

func turnFailuresFromContext(ctx context.Context) *turnFailures {
	failures, _ := ctx.Value(turnFailuresContextKey{}).(*turnFailures)
	return failures
}

func recordTurnFailure(ctx context.Context, model string) {
	failures := turnFailuresFromContext(ctx)
	if failures == nil || model == "" {
		return
	}
	failures.mu.Lock()
	defer failures.mu.Unlock()
	if !slices.Contains(failures.models, model) {
		failures.models = append(failures.models, model)
	}
}

// strikeRescuedModelsForTurn keeps every model that failed and was rescued by
// another one in this request out of the session until the next user prompt,
// so tool-result requests of the same agent turn do not swap back to it.
func (s *Service) strikeRescuedModelsForTurn(ctx context.Context, proxyErr error, served router.Decision, hardPinned bool, sessionKey [sessionpin.SessionKeyLen]byte, role, pinRole string) {
	failures := turnFailuresFromContext(ctx)
	store, ok := s.pinStore.(sessionpin.TurnStrikeStore)
	if !ok || proxyErr != nil || hardPinned || sessionKey == ([sessionpin.SessionKeyLen]byte{}) ||
		strings.HasPrefix(served.Reason, translate.ReasonUserForceModel) {
		return
	}
	failures.mu.Lock()
	models := slices.Clone(failures.models)
	failures.mu.Unlock()
	log := observability.FromContext(ctx)
	for _, model := range models {
		if model == served.Model {
			continue
		}
		for _, strikeRole := range demotionRoles(role, pinRole) {
			// The request ctx may already be canceled once the stream has ended.
			if err := store.StrikeTurnModel(context.Background(), sessionKey, strikeRole, model); err != nil {
				log.Error("agent-turn model strike failed", "err", err, "role", strikeRole, "model", model)
			}
		}
		log.Info("Rescued model kept out for the rest of the agent turn", "model", model, "served_model", served.Model)
	}
}

func (s *Service) clearTurnStrikes(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, pinRole string) {
	store, ok := s.pinStore.(sessionpin.TurnStrikeStore)
	if !ok || sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return
	}
	for _, role := range demotionRoles(pinRole, pinRole) {
		if err := store.ClearTurnStrikes(context.Background(), sessionKey, role); err != nil {
			observability.FromContext(ctx).Error("agent-turn strike clear failed", "err", err, "role", role)
		}
	}
}
