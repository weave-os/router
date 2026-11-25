package analytics

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

const usageDateLayout = time.DateOnly

type usageResponse struct {
	Rows       []usageRowResponse `json:"rows"`
	NextCursor *string            `json:"next_cursor"`
}

type usageRowResponse struct {
	Date                 string  `json:"date"`
	RouterUserID         *string `json:"router_user_id"`
	Email                *string `json:"email"`
	DisplayName          *string `json:"display_name"`
	APIKeyID             *string `json:"api_key_id"`
	APIKeyName           *string `json:"api_key_name"`
	ServedModel          *string `json:"served_model"`
	ServedProvider       *string `json:"served_provider"`
	RequestedModel       *string `json:"requested_model"`
	Requests             int64   `json:"requests"`
	FailedRequests       int64   `json:"failed_requests"`
	SubscriptionRequests int64   `json:"subscription_requests"`
	InputTokens          int64   `json:"input_tokens"`
	OutputTokens         int64   `json:"output_tokens"`
	CacheReadTokens      int64   `json:"cache_read_tokens"`
	CacheWriteTokens     int64   `json:"cache_write_tokens"`
	CostUSDMicros        int64   `json:"cost_usd_micros"`
	APIRateCostUSDMicros int64   `json:"api_rate_cost_usd_micros"`
}

func UsageHandler(svc *analytics.UsageService) gin.HandlerFunc {
	return func(c *gin.Context) {
		query, err := parseUsageQuery(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
			return
		}

		page, err := svc.Usage(c.Request.Context(), query)
		if err != nil {
			observability.FromGin(c).Error("Usage aggregation failed", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "usage_failed"})
			return
		}

		c.JSON(http.StatusOK, usageResponse{Rows: mapUsageRows(page.Rows), NextCursor: optionalString(page.NextCursor)})
	}
}

func parseUsageQuery(c *gin.Context) (analytics.UsageQuery, error) {
	orgID := strings.TrimSpace(c.Query("organization_id"))
	if orgID == "" {
		return analytics.UsageQuery{}, errors.New("organization_id is required")
	}

	start, err := parseUsageDate(c, "start")
	if err != nil {
		return analytics.UsageQuery{}, err
	}
	end, err := parseUsageDate(c, "end")
	if err != nil {
		return analytics.UsageQuery{}, err
	}
	if err := analytics.ValidateUsageWindow(start, end); err != nil {
		return analytics.UsageQuery{}, err
	}

	groups, err := parseUsageGroups(c.Query("group_by"))
	if err != nil {
		return analytics.UsageQuery{}, err
	}

	cursor, err := analytics.ParseUsageCursor(strings.TrimSpace(c.Query("cursor")))
	if err != nil {
		return analytics.UsageQuery{}, err
	}

	limit := analytics.UsageDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return analytics.UsageQuery{}, errors.New("limit must be a positive integer")
		}
		if parsed > analytics.UsageMaxLimit {
			return analytics.UsageQuery{}, errors.New("limit exceeds " + strconv.Itoa(analytics.UsageMaxLimit))
		}
		limit = parsed
	}

	return analytics.UsageQuery{
		OrganizationID: orgID,
		Start:          start,
		End:            end,
		Groups:         groups,
		Emails:         splitCommaList(c.Query("emails")),
		APIKeyIDs:      splitCommaList(c.Query("api_key_ids")),
		Cursor:         cursor,
		Limit:          limit,
	}, nil
}

func parseUsageDate(c *gin.Context, name string) (time.Time, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return time.Time{}, errors.New(name + " is required (YYYY-MM-DD)")
	}
	parsed, err := time.ParseInLocation(usageDateLayout, raw, time.UTC)
	if err != nil {
		return time.Time{}, errors.New(name + " must be a YYYY-MM-DD UTC date")
	}
	return parsed, nil
}

func parseUsageGroups(raw string) (map[analytics.UsageGroup]struct{}, error) {
	groups := make(map[analytics.UsageGroup]struct{}, len(analytics.UsageGroups))
	if strings.TrimSpace(raw) == "" {
		for _, g := range analytics.UsageGroups {
			groups[g] = struct{}{}
		}
		return groups, nil
	}
	valid := make(map[analytics.UsageGroup]struct{}, len(analytics.UsageGroups))
	for _, g := range analytics.UsageGroups {
		valid[g] = struct{}{}
	}
	for _, part := range splitCommaList(raw) {
		g := analytics.UsageGroup(part)
		if _, ok := valid[g]; !ok {
			return nil, errors.New("group_by must be a comma list of user, api_key, served_model, requested_model")
		}
		groups[g] = struct{}{}
	}
	return groups, nil
}

func splitCommaList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func mapUsageRows(rows []analytics.UsageRow) []usageRowResponse {
	out := make([]usageRowResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, usageRowResponse{
			Date:                 r.Date,
			RouterUserID:         r.RouterUserID,
			Email:                r.Email,
			DisplayName:          r.DisplayName,
			APIKeyID:             r.APIKeyID,
			APIKeyName:           r.APIKeyName,
			ServedModel:          r.ServedModel,
			ServedProvider:       r.ServedProvider,
			RequestedModel:       r.RequestedModel,
			Requests:             r.Requests,
			FailedRequests:       r.FailedRequests,
			SubscriptionRequests: r.SubscriptionRequests,
			InputTokens:          r.InputTokens,
			OutputTokens:         r.OutputTokens,
			CacheReadTokens:      r.CacheReadTokens,
			CacheWriteTokens:     r.CacheWriteTokens,
			CostUSDMicros:        r.CostUSDMicros,
			APIRateCostUSDMicros: r.APIRateCostUSDMicros,
		})
	}
	return out
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
