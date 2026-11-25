package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SubscriptionQuota struct {
	Available bool
	ResetAt   time.Time
}

type includedUsageWindow struct {
	Utilization *float64   `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
}

func (c *Client) SubscriptionQuota(ctx context.Context, token []byte) (SubscriptionQuota, error) {
	endpoint := strings.TrimSuffix(strings.TrimRight(c.baseURL, "/"), "/v1") + "/api/oauth/usage"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return SubscriptionQuota{}, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("anthropic-beta", oauthBetaToken)
	req.Header.Set("User-Agent", "weave-router/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return SubscriptionQuota{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return SubscriptionQuota{}, fmt.Errorf("subscription usage returned status %d", resp.StatusCode)
	}
	var windows struct {
		FiveHour *includedUsageWindow `json:"five_hour"`
		SevenDay *includedUsageWindow `json:"seven_day"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&windows); err != nil {
		return SubscriptionQuota{}, fmt.Errorf("invalid subscription usage: %w", err)
	}
	quota := SubscriptionQuota{Available: true}
	for _, window := range []*includedUsageWindow{windows.FiveHour, windows.SevenDay} {
		if window == nil || window.Utilization == nil || *window.Utilization < 0 {
			return SubscriptionQuota{}, errors.New("subscription usage reported an incomplete included window")
		}
		if *window.Utilization < 100 {
			continue
		}
		quota.Available = false
		if window.ResetsAt != nil && window.ResetsAt.After(quota.ResetAt) {
			quota.ResetAt = window.ResetsAt.UTC()
		}
	}
	return quota, nil
}
