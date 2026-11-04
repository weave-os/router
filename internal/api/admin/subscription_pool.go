package admin

import (
	"cmp"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// Quota headers the router persists per subscription-served turn:
// unified_limit_headers for Claude, codex_limit_headers for Codex.
const (
	claudeSessionUtilizationHeader = "anthropic-ratelimit-unified-5h-utilization"
	claudeSessionResetHeader       = "anthropic-ratelimit-unified-5h-reset"
	claudeWeeklyUtilizationHeader  = "anthropic-ratelimit-unified-7d-utilization"
	claudeWeeklyResetHeader        = "anthropic-ratelimit-unified-7d-reset"

	codexPrimaryUsedPercentHeader     = "x-codex-primary-used-percent"
	codexPrimaryWindowMinutesHeader   = "x-codex-primary-window-minutes"
	codexPrimaryResetAtHeader         = "x-codex-primary-reset-at"
	codexSecondaryUsedPercentHeader   = "x-codex-secondary-used-percent"
	codexSecondaryWindowMinutesHeader = "x-codex-secondary-window-minutes"
	codexSecondaryResetAtHeader       = "x-codex-secondary-reset-at"
)

const (
	subscriptionProviderClaude = "claude"
	subscriptionProviderCodex  = "codex"

	sessionLimitWindowMinutes = 5 * 60
	weeklyLimitWindowMinutes  = 7 * 24 * 60

	// Served counts span the weekly window they draw down.
	subscriptionPoolServedWindow = 7 * 24 * time.Hour
	// One day past the weekly window keeps a seat idle since its last reset visible.
	subscriptionPoolReadingLookback = 8 * 24 * time.Hour
)

type subscriptionLimitWindow struct {
	WindowMinutes int     `json:"window_minutes"`
	UsedFraction  float64 `json:"used_fraction"`
	ResetsAt      *string `json:"resets_at"`
}

type subscriptionPoolSeat struct {
	ID               string                    `json:"id"`
	Provider         string                    `json:"provider"`
	DisplayName      *string                   `json:"display_name"`
	OwnerName        *string                   `json:"owner_name"`
	Enabled          bool                      `json:"enabled"`
	State            string                    `json:"state"`
	CooldownUntil    *string                   `json:"cooldown_until"`
	ServedRequests   int64                     `json:"served_requests"`
	LimitsObservedAt *string                   `json:"limits_observed_at"`
	LimitWindows     []subscriptionLimitWindow `json:"limit_windows"`
}

type subscriptionPoolResponse struct {
	Enabled bool                   `json:"enabled"`
	Seats   []subscriptionPoolSeat `json:"seats"`
}

// SubscriptionPoolHandler serves GET /admin/v1/subscription-pool: every
// Claude/Codex seat in the pool with the limit windows its newest turn reported.
func SubscriptionPoolHandler(proxySvc *proxy.Service, poolsEnabled bool, now func() time.Time) gin.HandlerFunc {
	return func(c *gin.Context) {
		installationID, ok := dashboardScope(c)
		if !ok {
			return
		}
		if !poolsEnabled {
			c.JSON(http.StatusOK, subscriptionPoolResponse{Seats: []subscriptionPoolSeat{}})
			return
		}
		at := now()
		seats, err := proxySvc.MetricsSubscriptionPool(c.Request.Context(), installationID,
			at.Add(-subscriptionPoolServedWindow), at.Add(-subscriptionPoolReadingLookback))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscription pool."})
			return
		}
		response := subscriptionPoolResponse{Enabled: true, Seats: make([]subscriptionPoolSeat, 0, len(seats))}
		for _, seat := range seats {
			response.Seats = append(response.Seats, subscriptionPoolSeat{
				ID:               seat.ID,
				Provider:         seat.Provider,
				DisplayName:      seat.DisplayName,
				OwnerName:        seat.OwnerName,
				Enabled:          seat.Enabled,
				State:            seat.State,
				CooldownUntil:    formatOptionalTime(seat.CooldownUntil),
				ServedRequests:   seat.ServedRequests,
				LimitsObservedAt: formatOptionalTime(seat.LimitsObservedAt),
				LimitWindows:     subscriptionLimitWindows(seat.Provider, seat.ClaudeLimitHeaders, seat.CodexLimitHeaders),
			})
		}
		c.JSON(http.StatusOK, response)
	}
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

// subscriptionLimitWindows normalizes a seat's newest limit headers into
// windows, shortest first. Windows the provider did not report are omitted.
func subscriptionLimitWindows(provider string, claudeHeaders, codexHeaders map[string]string) []subscriptionLimitWindow {
	windows := make([]subscriptionLimitWindow, 0, 2)
	appendWindow := func(windowMinutes int, usedFraction float64, isReported bool, resetsAt *string) {
		if !isReported || windowMinutes == 0 {
			return
		}
		windows = append(windows, subscriptionLimitWindow{
			WindowMinutes: windowMinutes,
			UsedFraction:  math.Min(math.Max(usedFraction, 0), 1),
			ResetsAt:      resetsAt,
		})
	}
	switch provider {
	case subscriptionProviderClaude:
		sessionUsed, isSessionReported := headerFloat(claudeHeaders, claudeSessionUtilizationHeader)
		appendWindow(sessionLimitWindowMinutes, sessionUsed, isSessionReported, headerUnixTime(claudeHeaders, claudeSessionResetHeader))
		weeklyUsed, isWeeklyReported := headerFloat(claudeHeaders, claudeWeeklyUtilizationHeader)
		appendWindow(weeklyLimitWindowMinutes, weeklyUsed, isWeeklyReported, headerUnixTime(claudeHeaders, claudeWeeklyResetHeader))
	case subscriptionProviderCodex:
		primaryPercent, isPrimaryReported := headerFloat(codexHeaders, codexPrimaryUsedPercentHeader)
		appendWindow(headerWindowMinutes(codexHeaders, codexPrimaryWindowMinutesHeader, sessionLimitWindowMinutes),
			primaryPercent/100, isPrimaryReported, headerUnixTime(codexHeaders, codexPrimaryResetAtHeader))
		secondaryPercent, isSecondaryReported := headerFloat(codexHeaders, codexSecondaryUsedPercentHeader)
		appendWindow(headerWindowMinutes(codexHeaders, codexSecondaryWindowMinutesHeader, weeklyLimitWindowMinutes),
			secondaryPercent/100, isSecondaryReported, headerUnixTime(codexHeaders, codexSecondaryResetAtHeader))
	}
	slices.SortStableFunc(windows, func(left, right subscriptionLimitWindow) int {
		return cmp.Compare(left.WindowMinutes, right.WindowMinutes)
	})
	return windows
}

func headerFloat(headers map[string]string, name string) (float64, bool) {
	raw, isPresent := headers[name]
	if !isPresent {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// Codex reports a window the plan lacks as zero minutes; zero means no such window.
func headerWindowMinutes(headers map[string]string, name string, fallback int) int {
	raw, isPresent := headers[name]
	if !isPresent {
		return fallback
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes < 0 {
		return fallback
	}
	return minutes
}

func headerUnixTime(headers map[string]string, name string) *string {
	seconds, err := strconv.ParseInt(headers[name], 10, 64)
	if err != nil || seconds <= 0 {
		return nil
	}
	formatted := time.Unix(seconds, 0).UTC().Format(time.RFC3339)
	return &formatted
}
