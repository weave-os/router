package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
		log.Warn("Codex quota preflight request build failed", "err", err)
		return codexQuotaUnavailable()
	}
	creds := codexSubscriptionCreds(ctx)
	req.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
	req.Header.Set(requestcontext.ChatGPTAccountIDHeader, string(creds.AccountID))
	req.Header.Set(codexOriginatorHeader, codexOriginatorValue)
	req.Header.Set(codexUserAgentHeader, codexUserAgentValue)
	resp, err := c.http.Do(req)
	if err != nil {
		log.Warn("Codex quota preflight transport failed", "err", err)
		if parentCtx.Err() != nil {
			return parentCtx.Err()
		}
		return codexQuotaUnavailable()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warn("Codex quota preflight rejected", "upstream_status", resp.StatusCode)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			body, _ := json.Marshal(codexQuotaError{Error: codexQuotaErrorDetails{Type: codexQuotaErrorAuthentication, Message: "Codex subscription authentication was rejected. Reconnect the account."}})
			return &providers.UpstreamErrorResponse{Status: resp.StatusCode, Body: body}
		}
		return codexQuotaUnavailable()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		log.Warn("Codex quota preflight body read failed", "err", err)
		return codexQuotaUnavailable()
	}
	var quotaStatus struct {
		PlanType     string              `json:"plan_type"`
		RateLimit    *codexIncludedQuota `json:"rate_limit"`
		SpendControl *struct {
			Reached bool `json:"reached"`
		} `json:"spend_control"`
		RateLimitReachedType *struct {
			Type string `json:"type"`
		} `json:"rate_limit_reached_type"`
		AdditionalRateLimits []struct {
			Model     string              `json:"normal_model_slug"`
			RateLimit *codexIncludedQuota `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if err := json.Unmarshal(body, &quotaStatus); err != nil {
		log.Warn("Codex quota preflight decode failed", "err", err)
		return codexQuotaUnavailable()
	}
	// Usage-based and workspace plans report a plan without windowed limits;
	// their exhaustion arrives as spend_control or rate_limit_reached_type.
	planReported := quotaStatus.PlanType != ""
	limitReached := (quotaStatus.SpendControl != nil && quotaStatus.SpendControl.Reached) ||
		(quotaStatus.RateLimitReachedType != nil && codexReachedTypes[quotaStatus.RateLimitReachedType.Type])
	creditReason := codexCreditsDisabled
	if c.allowCodexExtraCredits {
		var metadata struct {
			Credits      json.RawMessage `json:"credits"`
			SpendControl json.RawMessage `json:"spend_control"`
			ReachedType  json.RawMessage `json:"rate_limit_reached_type"`
		}
		if err := json.Unmarshal(body, &metadata); err != nil {
			return codexQuotaUnavailable()
		}
		creditReason = codexCreditAdmissionReason(metadata.Credits, metadata.SpendControl, metadata.ReachedType)
	}
	allowCredits := creditReason == codexCreditsAvailable && quotaStatus.RateLimit != nil &&
		(quotaStatus.RateLimit.Primary != nil || quotaStatus.RateLimit.Secondary != nil)
	// Windows go first so an exhausted window keeps its reset time.
	if err = validateCodexIncludedQuota(quotaStatus.RateLimit, planReported || limitReached); err != nil && !(allowCredits && codexIncludedLimitReached(err)) {
		logCodexQuotaDenial(log, err, "plan_type", quotaStatus.PlanType, "has_rate_limit", quotaStatus.RateLimit != nil, "credit_reason", creditReason, "allow_credits", allowCredits)
		return err
	}
	if limitReached && !allowCredits {
		log.Info("Codex workspace limit preflight denied inference", "plan_type", quotaStatus.PlanType, "credit_reason", creditReason)
		return codexQuotaExhausted(0)
	}
	for _, modelRateLimit := range quotaStatus.AdditionalRateLimits {
		if modelRateLimit.Model == model {
			if err = validateCodexIncludedQuota(modelRateLimit.RateLimit, planReported); err != nil {
				logCodexQuotaDenial(log, err, "plan_type", quotaStatus.PlanType, "model", model, "has_rate_limit", modelRateLimit.RateLimit != nil)
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

// codexReachedTypes are the rate_limit_reached_type values that mean the plan
// cannot serve the turn; unknown values are ignored as Codex itself does.
var codexReachedTypes = map[string]bool{
	"rate_limit_reached":                   true,
	"workspace_owner_credits_depleted":     true,
	"workspace_member_credits_depleted":    true,
	"workspace_owner_usage_limit_reached":  true,
	"workspace_member_usage_limit_reached": true,
}

// logCodexQuotaDenial surfaces unverifiable quota at WARN: it fails every turn
// for the token, unlike exhaustion, which the caller records and routes around.
func logCodexQuotaDenial(log *slog.Logger, err error, attrs ...any) {
	var upstream *providers.UpstreamErrorResponse
	if errors.As(err, &upstream) && upstream.Status == http.StatusServiceUnavailable {
		log.Warn("Codex quota preflight could not verify included quota", attrs...)
		return
	}
	log.Info("Codex quota preflight denied inference", append(attrs, "err", err)...)
}

// windowsOptional accepts a quota without windows, or no quota at all, once the
// payload reported a plan: plans without windowed limits omit them.
func validateCodexIncludedQuota(quota *codexIncludedQuota, windowsOptional bool) error {
	if quota == nil {
		if windowsOptional {
			return nil
		}
		return codexQuotaUnavailable()
	}
	if quota.Allowed == nil || quota.LimitReached == nil {
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
		return codexQuotaExhausted(resetAt)
	}
	if !validWindow && !windowsOptional {
		return codexQuotaUnavailable()
	}
	return nil
}

func codexQuotaExhausted(resetAt int64) error {
	body, _ := json.Marshal(codexQuotaError{Error: codexQuotaErrorDetails{Type: codexQuotaErrorExhausted, ResetsAt: resetAt, Message: "Codex included quota is exhausted."}})
	return &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: body}
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
