package usage

import (
	"net/http"
	"strings"
	"time"
)

// UnifiedRejection is what an Anthropic subscription 429 states about which
// quota window refused the request.
type UnifiedRejection struct {
	// Present is true when the response carried anthropic-ratelimit-unified-*
	// headers. Their silence about a window then means that window did not
	// refuse the request.
	Present bool
	// ResetAt is the latest reset among the windows the response marks
	// rejected. Zero when no rejected window states a reset in the future.
	ResetAt time.Time
}

var unifiedWindows = [...]string{"5h", "7d", "weekly"}

// ParseUnifiedRejection reads which windows an Anthropic subscription 429
// rejects and when each refills. A window is rejected when its status is
// "rejected" or its utilization is 100%. A window reported "allowed" or
// "allowed_warning" below 100% did not refuse the request, so its reset says
// nothing about when the account recovers: the weekly reset is days away
// while the five-hour window that refused the request refills within hours.
func ParseUnifiedRejection(h http.Header, now time.Time) UnifiedRejection {
	var out UnifiedRejection
	windowHeaders := false
	for _, which := range unifiedWindows {
		prefix := "anthropic-ratelimit-unified-" + which + "-"
		status := strings.TrimSpace(h.Get(prefix + "status"))
		utilization, hasUtilization := parseFloat(h.Get(prefix + "utilization"))
		if status == "" && !hasUtilization && h.Get(prefix+"reset") == "" {
			continue
		}
		windowHeaders = true
		if status != "rejected" && !(hasUtilization && utilization >= exhaustedFraction) {
			continue
		}
		out.ResetAt = laterReset(out.ResetAt, h.Get(prefix+"reset"), now)
	}
	overall := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-status"))
	out.Present = windowHeaders || overall != ""
	if !windowHeaders && overall == "rejected" {
		out.ResetAt = laterReset(out.ResetAt, h.Get("anthropic-ratelimit-unified-reset"), now)
	}
	return out
}

// ParseStandardRateLimitReset returns the latest refill time among the
// anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens} buckets
// that are empty, which is what an API-key 429 carries. Zero when none is
// empty or none states a future reset.
func ParseStandardRateLimitReset(h http.Header, now time.Time) time.Time {
	var latest time.Time
	for _, bucket := range [...]string{"requests", "tokens", "input-tokens", "output-tokens"} {
		prefix := "anthropic-ratelimit-" + bucket + "-"
		if remaining, ok := parseFloat(h.Get(prefix + "remaining")); !ok || remaining > 0 {
			continue
		}
		latest = laterReset(latest, h.Get(prefix+"reset"), now)
	}
	return latest
}

func laterReset(current time.Time, header string, now time.Time) time.Time {
	if resetAt, ok := parseResetTime(header); ok && resetAt.After(now) && resetAt.After(current) {
		return resetAt
	}
	return current
}
