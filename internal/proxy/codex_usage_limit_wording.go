package proxy

import (
	"errors"
	"strconv"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
)

const codexUsageLimitBadge = "hit the Codex usage limit"

type codexUsageLimit struct {
	upstream *providers.UpstreamErrorResponse
	resetIn  time.Duration
}

func codexUsageLimitOf(a dispatch.TracedAttempt) (codexUsageLimit, bool) {
	if !a.Subscription || a.Target.Provider != providers.ProviderOpenAI {
		return codexUsageLimit{}, false
	}
	upstream := findUpstreamError(a.Err, func(candidate *providers.UpstreamErrorResponse) bool {
		_, exhausted := credentials.CodexQuotaExhaustion(candidate)
		return exhausted
	})
	if upstream == nil {
		return codexUsageLimit{}, false
	}
	limit := codexUsageLimit{upstream: upstream}
	if resetAt, _ := credentials.CodexQuotaExhaustion(upstream); !resetAt.IsZero() && !a.At.IsZero() && resetAt.After(a.At) {
		limit.resetIn = resetAt.Sub(a.At)
	}
	return limit, true
}

func findUpstreamError(err error, match func(*providers.UpstreamErrorResponse) bool) *providers.UpstreamErrorResponse {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) {
		return nil
	}
	if match(upstream) {
		return upstream
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range joined.Unwrap() {
			if found := findUpstreamError(inner, match); found != nil {
				return found
			}
		}
		return nil
	}
	return findUpstreamError(errors.Unwrap(err), match)
}

func (l codexUsageLimit) badge() string {
	if l.resetIn <= 0 {
		return codexUsageLimitBadge
	}
	return codexUsageLimitBadge + " (resets in " + shortDuration(l.resetIn) + ")"
}

func (l codexUsageLimit) wording() failureWording {
	if l.resetIn <= 0 {
		return failureWording{l.badge(), "Codex usage limit reached"}
	}
	return failureWording{l.badge(), "Codex usage limit reached (resets in " + shortDuration(l.resetIn) + ")"}
}

func shortDuration(d time.Duration) string {
	minutes := int(d / time.Minute)
	if minutes < 1 {
		return "under 1m"
	}
	if minutes < 60 {
		return strconv.Itoa(minutes) + "m"
	}
	if minutes%60 == 0 {
		return strconv.Itoa(minutes/60) + "h"
	}
	return strconv.Itoa(minutes/60) + "h " + strconv.Itoa(minutes%60) + "m"
}
