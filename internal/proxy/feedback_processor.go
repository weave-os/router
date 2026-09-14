package proxy

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/google/uuid"
)

// Router feedback delivery bounds. The send deadline is deliberately shorter
// than the lease so a slow reporter cannot still be running when another
// worker becomes eligible to claim the same row.
const (
	routerFeedbackLease        = 30 * time.Second
	routerFeedbackSendTimeout  = 10 * time.Second
	routerFeedbackFinishGrace  = 5 * time.Second
	routerFeedbackStoreTimeout = 5 * time.Second
	routerFeedbackIdlePoll     = 5 * time.Second
	routerFeedbackBaseBackoff  = 30 * time.Second
	routerFeedbackMaxBackoff   = time.Hour
)

// RunRouterFeedbackProcessor delivers accepted commands until cancellation.
// Expiring claims recover after restart; receivers must durably deduplicate feedback_id.
func (s *Service) RunRouterFeedbackProcessor(ctx context.Context, queue RouterFeedbackQueue) error {
	if queue == nil {
		return errors.New("proxy: router feedback processor requires a queue")
	}
	log := routerFeedbackProcessorLogger(ctx)
	for {
		claimed, err := s.processRouterFeedback(ctx, queue, log)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			log.Error("Router feedback delivery cycle failed", "err", err)
		case claimed:
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(routerFeedbackIdlePoll):
		}
	}
}

// ProcessRouterFeedback claims and delivers at most one due command, reporting
// whether one was claimed. Exposed for focused tests and operational scripts;
// the process-owned loop is RunRouterFeedbackProcessor.
func (s *Service) ProcessRouterFeedback(ctx context.Context, queue RouterFeedbackQueue) (bool, error) {
	return s.processRouterFeedback(ctx, queue, routerFeedbackProcessorLogger(ctx))
}

func routerFeedbackProcessorLogger(ctx context.Context) *slog.Logger {
	return observability.FromContext(ctx).With("worker", "router_feedback_processor")
}

func (s *Service) processRouterFeedback(ctx context.Context, queue RouterFeedbackQueue, log *slog.Logger) (bool, error) {
	if queue == nil {
		return false, errors.New("proxy: router feedback processor requires a queue")
	}
	token := uuid.NewString()
	claimCtx, cancelClaim := context.WithTimeout(ctx, routerFeedbackStoreTimeout)
	event, claimed, err := queue.ClaimRouterFeedback(claimCtx, token, routerFeedbackLease)
	cancelClaim()
	if err != nil {
		return false, fmt.Errorf("claim router feedback: %w", err)
	}
	if !claimed {
		return false, nil
	}
	log = log.With("feedback_id", event.ID, "strategy", event.Strategy, "attempts", event.Attempts, "pending_age_ms", time.Since(event.CreatedAt).Milliseconds())

	status, reason, err := s.deliverRouterFeedback(ctx, queue, event)
	if err != nil {
		backoff := routerFeedbackBackoff(event.Attempts)
		log.Warn("Router feedback delivery failed; retrying later", "retry_in_ms", backoff.Milliseconds(), "err", err)
		if finishErr := s.finishRouterFeedback(ctx, queue, event.ID, token, RouterFeedbackPending, err.Error(), time.Now().Add(backoff)); finishErr != nil {
			return true, fmt.Errorf("reschedule router feedback: %w", finishErr)
		}
		return true, nil
	}
	if status == RouterFeedbackDelivered {
		reason = ""
	}
	if finishErr := s.finishRouterFeedback(ctx, queue, event.ID, token, status, reason, time.Time{}); finishErr != nil {
		return true, fmt.Errorf("finish router feedback: %w", finishErr)
	}
	log.Info("Router feedback delivery settled", "delivery_status", status, "reason", reason)
	return true, nil
}

func (s *Service) finishRouterFeedback(ctx context.Context, queue RouterFeedbackQueue, id, token, status, lastError string, nextAttempt time.Time) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routerFeedbackFinishGrace)
	defer cancel()
	lastError = strings.ToValidUTF8(lastError, "?")
	if len(lastError) > 1024 {
		lastError = strings.ToValidUTF8(lastError[:1024], "")
	}
	return queue.FinishRouterFeedback(finishCtx, id, token, status, lastError, nextAttempt)
}

func (s *Service) deliverRouterFeedback(ctx context.Context, queue RouterFeedbackQueue, event RouterFeedbackEvent) (status, reason string, err error) {
	if !event.Attached() {
		return RouterFeedbackSkipped, "no target request", nil
	}
	if !event.TrainingAllowed {
		return RouterFeedbackSkipped, "training permission was not granted at acceptance", nil
	}
	strategy := router.Strategy(event.Strategy)
	registered, ok := s.strategies[strategy]
	switch {
	case strategy == "" || strategy == router.StrategyCluster:
		// The default scorer has no learner; there is nothing to credit.
		return RouterFeedbackSkipped, "strategy has no learning consumer", nil
	case !ok:
		// Registration is boot-time wiring. A strategy this process never
		// registered may be served by another router or a later release, so
		// the rating waits rather than being written off as undeliverable.
		return "", "", fmt.Errorf("no strategy registered for %q", strategy)
	case registered.router == nil:
		return "", "", fmt.Errorf("strategy %q reporter is unavailable", strategy)
	case registered.feedback == nil:
		return RouterFeedbackSkipped, "strategy has no learning consumer", nil
	case !feedbackCapabilityEnabled(registered):
		return RouterFeedbackSkipped, "strategy reports feedback disabled", nil
	case !registered.feedbackRetrySafe:
		return "", "", fmt.Errorf("strategy %q reporter contract to deduplicate feedback_id is unverified", strategy)
	}
	permissionCtx, cancelPermission := context.WithTimeout(ctx, routerFeedbackStoreTimeout)
	allowed, err := queue.RouterFeedbackTrainingAllowed(permissionCtx, event.InstallationID, event.ExternalID)
	cancelPermission()
	if err != nil {
		return "", "", fmt.Errorf("recheck feedback training permission: %w", err)
	}
	if !allowed {
		return RouterFeedbackSkipped, "installation training permission withdrawn", nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, routerFeedbackSendTimeout)
	defer cancel()
	if err := registered.feedback.ReportFeedback(sendCtx, routerFeedbackPayload(event)); err != nil {
		return "", "", fmt.Errorf("report feedback to strategy %q: %w", strategy, err)
	}
	return RouterFeedbackDelivered, "reported", nil
}

// feedbackCapabilityEnabled honors an explicitly disabled feedback capability.
// A reporter that declares no capabilities at all (an older sidecar answering
// an all-zero capability set) is not treated as a refusal: its ReportFeedback
// is the contract, and skipping would silently drop the rating.
func feedbackCapabilityEnabled(registered registeredStrategy) bool {
	capabilities := registered.capabilities
	if source, dynamic := registered.router.(policy.CapabilitySource); dynamic {
		capabilities = source.CurrentCapabilities()
	}
	return capabilities == (policy.Capabilities{}) || capabilities.ReportsFeedback
}

func routerFeedbackPayload(event RouterFeedbackEvent) map[string]interface{} {
	payload := map[string]interface{}{
		"feedback_id":       event.ID,
		"strategy":          event.Strategy,
		"feedback_key":      hex.EncodeToString(event.SessionKey),
		"feedback_role":     event.Role,
		"rating":            event.Rating,
		"feedback":          event.Feedback,
		"requested_model":   event.RequestedModel,
		"served_model":      event.ServedModel,
		"served_provider":   event.ServedProvider,
		"router_user_id":    event.RouterUserID,
		"client_app":        event.ClientApp,
		"client_session_id": event.SessionID,
		"source":            event.Source,
		"request_id":        event.RequestID,
		"route_id":          event.RouteID,
		"rollout_id":        event.RolloutID,
		"training_allowed":  event.TrainingAllowed,
	}
	if event.SuggestedLabel != "" {
		payload["suggested_label"] = event.SuggestedLabel
	}
	if event.ExternalID != "" {
		payload["organization_id"] = event.ExternalID
	}
	if event.InstallationID != "" {
		payload["installation_id"] = event.InstallationID
	}
	return payload
}

func routerFeedbackBackoff(attempts int) time.Duration {
	backoff := routerFeedbackMaxBackoff
	if attempts < 8 {
		if scaled := routerFeedbackBaseBackoff << attempts; scaled < routerFeedbackMaxBackoff {
			backoff = scaled
		}
	}
	return backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)))
}
