package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

type codexQuotaWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	ResetAt     int64    `json:"reset_at"`
}
type codexIncludedQuota struct {
	Allowed      *bool             `json:"allowed"`
	LimitReached *bool             `json:"limit_reached"`
	Primary      *codexQuotaWindow `json:"primary_window"`
	Secondary    *codexQuotaWindow `json:"secondary_window"`
}

// Usage is checked before inference because exhausted subscriptions can accept
// inference by spending purchased credits rather than returning a quota error.
func (c *Client) checkCodexIncludedQuota(ctx context.Context, model string) error {
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(strings.TrimRight(c.codexBaseURL, "/"), "/codex")+"/wham/usage", nil)
	if err != nil {
		return codexQuotaUnavailable()
	}
	creds := codexSubscriptionCreds(ctx)
	req.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
	req.Header.Set(requestcontext.ChatGPTAccountIDHeader, string(creds.AccountID))
	resp, err := c.http.Do(req)
	if err != nil {
		if parentCtx.Err() != nil {
			return parentCtx.Err()
		}
		return codexQuotaUnavailable()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return codexQuotaUnavailable()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return codexQuotaUnavailable()
	}
	var status struct {
		RateLimit  *codexIncludedQuota `json:"rate_limit"`
		Additional []struct {
			Model     string              `json:"normal_model_slug"`
			RateLimit *codexIncludedQuota `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if json.Unmarshal(body, &status) != nil {
		return codexQuotaUnavailable()
	}
	if err = validateCodexIncludedQuota(status.RateLimit); err != nil {
		return err
	}
	for _, additional := range status.Additional {
		if additional.Model == model {
			if err = validateCodexIncludedQuota(additional.RateLimit); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCodexIncludedQuota(quota *codexIncludedQuota) error {
	if quota == nil || quota.Allowed == nil || quota.LimitReached == nil {
		return codexQuotaUnavailable()
	}
	exhausted := !*quota.Allowed || *quota.LimitReached
	resetAt := int64(0)
	validWindow := false
	for _, window := range []*codexQuotaWindow{quota.Primary, quota.Secondary} {
		if window == nil {
			continue
		}
		if window.UsedPercent == nil || *window.UsedPercent < 0 || *window.UsedPercent > 100 {
			return codexQuotaUnavailable()
		}
		validWindow = true
		if *window.UsedPercent >= 100 {
			exhausted = true
			if window.ResetAt > resetAt {
				resetAt = window.ResetAt
			}
		}
	}
	if exhausted {
		body, _ := json.Marshal(codexQuotaError{Error: codexQuotaErrorDetails{Type: codexQuotaErrorExhausted, ResetsAt: resetAt}})
		return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: body}
	}
	if !validWindow {
		return codexQuotaUnavailable()
	}
	return nil
}
func codexQuotaUnavailable() error {
	return &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":{"type":"api_error","message":"Included Codex quota could not be verified."}}`)}
}

type codexQuotaErrorType string

const codexQuotaErrorExhausted codexQuotaErrorType = "usage_limit_reached"

type codexQuotaErrorDetails struct {
	Type     codexQuotaErrorType `json:"type"`
	ResetsAt int64               `json:"resets_at,omitempty"`
}
type codexQuotaError struct {
	Error codexQuotaErrorDetails `json:"error"`
}
