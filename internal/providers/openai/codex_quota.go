package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/observability"
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
	log := observability.FromContext(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(strings.TrimRight(c.codexBaseURL, "/"), "/codex")+"/wham/usage", nil)
	if err != nil {
		log.Debug("Codex quota preflight request build failed", "err", err)
		return codexQuotaUnavailable()
	}
	creds := codexSubscriptionCreds(ctx)
	req.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
	req.Header.Set(requestcontext.ChatGPTAccountIDHeader, string(creds.AccountID))
	req.Header.Set(codexOriginatorHeader, codexOriginatorValue)
	req.Header.Set(codexUserAgentHeader, codexUserAgentValue)
	resp, err := c.http.Do(req)
	if err != nil {
		log.Debug("Codex quota preflight transport failed", "err", err)
		if parentCtx.Err() != nil {
			return parentCtx.Err()
		}
		return codexQuotaUnavailable()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Debug("Codex quota preflight rejected", "upstream_status", resp.StatusCode)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			body, _ := json.Marshal(codexQuotaError{Error: codexQuotaErrorDetails{Type: codexQuotaErrorAuthentication, Message: "Codex subscription authentication was rejected. Reconnect the account."}})
			return &providers.UpstreamErrorResponse{Status: resp.StatusCode, Body: body}
		}
		return codexQuotaUnavailable()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		log.Debug("Codex quota preflight body read failed", "err", err)
		return codexQuotaUnavailable()
	}
	var quotaStatus struct {
		RateLimit            *codexIncludedQuota `json:"rate_limit"`
		AdditionalRateLimits []struct {
			Model     string              `json:"normal_model_slug"`
			RateLimit *codexIncludedQuota `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if err := json.Unmarshal(body, &quotaStatus); err != nil {
		log.Debug("Codex quota preflight decode failed", "err", err)
		return codexQuotaUnavailable()
	}
	if err = validateCodexIncludedQuota(quotaStatus.RateLimit); err != nil {
		log.Debug("Codex included quota preflight denied inference", "err", err)
		return err
	}
	for _, modelRateLimit := range quotaStatus.AdditionalRateLimits {
		if modelRateLimit.Model == model {
			if err = validateCodexIncludedQuota(modelRateLimit.RateLimit); err != nil {
				log.Debug("Codex model quota preflight denied inference", "model", model, "err", err)
				var upstream *providers.UpstreamErrorResponse
				if errors.As(err, &upstream) && upstream.Status == http.StatusTooManyRequests {
					// Account cooldown applies to account-wide quota only. A model
					// limit remains retryable without marking the whole account spent.
					body, _ := json.Marshal(codexQuotaError{Error: codexQuotaErrorDetails{Type: codexQuotaErrorModelExhausted, Message: "Included Codex quota for the requested model is exhausted."}})
					return &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Body: body}
				}
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
		body, _ := json.Marshal(codexQuotaError{Error: codexQuotaErrorDetails{Type: codexQuotaErrorExhausted, ResetsAt: resetAt, Message: "Codex included quota is exhausted."}})
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

const (
	codexQuotaErrorExhausted      codexQuotaErrorType = "usage_limit_reached"
	codexQuotaErrorAuthentication codexQuotaErrorType = "authentication_error"
	codexQuotaErrorModelExhausted codexQuotaErrorType = "model_usage_limit_reached"
)

type codexQuotaErrorDetails struct {
	Type     codexQuotaErrorType `json:"type"`
	ResetsAt int64               `json:"resets_at,omitempty"`
	Message  string              `json:"message"`
}
type codexQuotaError struct {
	Error codexQuotaErrorDetails `json:"error"`
}
