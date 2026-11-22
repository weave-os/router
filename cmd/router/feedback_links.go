package main

import (
	"log/slog"
	"strconv"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/telemetry"
)

func feedbackLinksFromEnv(repo feedback.FeedbackRepository, emitter telemetry.Emitter, logger *slog.Logger) *feedback.Links {
	feedbackSigner := feedback.NewSigner(config.GetOr("ROUTER_FEEDBACK_LINK_SECRET", ""), feedbackLinkTTL())
	feedbackBaseURL := config.GetOr("ROUTER_FEEDBACK_BASE_URL", "")
	switch {
	case feedbackSigner != nil && feedbackBaseURL != "":
		logger.Info("Feedback link enabled", "base_url", feedbackBaseURL)
	case feedbackSigner != nil:
		logger.Warn("Feedback link endpoints mounted but ROUTER_FEEDBACK_BASE_URL is unset; responses will not carry a feedback link header")
	default:
		logger.Info("Feedback link disabled (set ROUTER_FEEDBACK_LINK_SECRET and ROUTER_FEEDBACK_BASE_URL to enable)")
	}
	return feedback.NewLinks(repo, feedbackSigner, feedbackBaseURL, emitter, time.Now)
}

func feedbackLinkTTL() time.Duration {
	const defaultSec = 30 * 24 * 60 * 60
	raw := config.GetOr("ROUTER_FEEDBACK_LINK_TTL_SEC", "")
	if raw == "" {
		return defaultSec * time.Second
	}
	sec, err := strconv.Atoi(raw)
	if err != nil || sec < 0 {
		observability.Get().Warn("Invalid env var; using default", "key", "ROUTER_FEEDBACK_LINK_TTL_SEC", "value", raw, "default", defaultSec)
		return defaultSec * time.Second
	}
	return time.Duration(sec) * time.Second
}
