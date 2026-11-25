package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	UsageDefaultLimit  = 1000
	UsageMaxLimit      = 10000
	UsageMaxWindowDays = 92
)

var (
	ErrUsageWindowInvalid = errors.New("start and end must be UTC dates (YYYY-MM-DD) with start <= end")
	ErrUsageWindowTooWide = fmt.Errorf("start and end must be at most %d days apart", UsageMaxWindowDays)
)

type UsageGroup string

const (
	UsageGroupUser           UsageGroup = "user"
	UsageGroupAPIKey         UsageGroup = "api_key"
	UsageGroupServedModel    UsageGroup = "served_model"
	UsageGroupRequestedModel UsageGroup = "requested_model"
)

var UsageGroups = []UsageGroup{
	UsageGroupUser,
	UsageGroupAPIKey,
	UsageGroupServedModel,
	UsageGroupRequestedModel,
}

type UsageQuery struct {
	OrganizationID string
	Start          time.Time
	End            time.Time
	Groups         map[UsageGroup]struct{}
	Emails         []string
	APIKeyIDs      []string
	Cursor         UsageCursor
	Limit          int
}

type UsageRow struct {
	Date                 string
	RouterUserID         *string
	Email                *string
	DisplayName          *string
	APIKeyID             *string
	APIKeyName           *string
	ServedModel          *string
	ServedProvider       *string
	RequestedModel       *string
	Requests             int64
	FailedRequests       int64
	SubscriptionRequests int64
	InputTokens          int64
	OutputTokens         int64
	CacheReadTokens      int64
	CacheWriteTokens     int64
	CostUSDMicros        int64
	APIRateCostUSDMicros int64
}

type UsageRepository interface {
	GetUsageRows(ctx context.Context, q UsageQuery) ([]UsageRow, error)
}

type UsagePage struct {
	Rows       []UsageRow
	NextCursor string
}

func ValidateUsageWindow(start, end time.Time) error {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return ErrUsageWindowInvalid
	}
	days := int(end.Sub(start).Hours()/24) + 1
	if days > UsageMaxWindowDays {
		return ErrUsageWindowTooWide
	}
	return nil
}
