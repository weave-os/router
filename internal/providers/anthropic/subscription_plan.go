package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
)

type organizationType string

const (
	organizationPro        organizationType = "claude_pro"
	organizationMax        organizationType = "claude_max"
	organizationTeam       organizationType = "claude_team"
	organizationEnterprise organizationType = "claude_enterprise"
)

// SubscriptionPlan uses the authenticated profile's organization_type exclusively.
// Quota windows, overage and organization display names do not identify a plan.
func (c *Client) SubscriptionPlan(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
	if !c.SupportsSubscriptions() {
		return providers.SubscriptionPlanUnknown, nil
	}
	endpoint := strings.TrimSuffix(strings.TrimRight(c.baseURL, "/"), "/v1") + "/api/oauth/profile"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providers.SubscriptionPlanUnknown, err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("User-Agent", "weave-router/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return providers.SubscriptionPlanUnknown, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return providers.SubscriptionPlanUnknown, fmt.Errorf("subscription profile returned status %d", resp.StatusCode)
	}
	var profile struct {
		Organization struct {
			Type organizationType `json:"organization_type"`
		} `json:"organization"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&profile); err != nil {
		return providers.SubscriptionPlanUnknown, fmt.Errorf("invalid subscription profile: %w", err)
	}
	switch profile.Organization.Type {
	case organizationPro:
		return providers.SubscriptionPlanPro, nil
	case organizationMax:
		return providers.SubscriptionPlanMax, nil
	case organizationTeam:
		return providers.SubscriptionPlanTeam, nil
	case organizationEnterprise:
		return providers.SubscriptionPlanEnterprise, nil
	default:
		return providers.SubscriptionPlanUnknown, nil
	}
}
