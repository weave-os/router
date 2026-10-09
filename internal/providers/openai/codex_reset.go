package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

var _ subscriptions.CodexResetClient = (*Client)(nil)

type codexResetType string
type codexResetStatus string

const (
	codexRateLimitReset codexResetType   = "codex_rate_limits"
	codexResetAvailable codexResetStatus = "available"
)

// CodexQuotaExhausted checks account-wide included quota, never purchased credits.
func (c *Client) CodexQuotaExhausted(ctx context.Context, lease subscriptions.Lease) (bool, error) {
	ctx = requestcontext.WithCredentials(ctx, &requestcontext.Credentials{
		APIKey: []byte(lease.AccessToken), AccountID: []byte(lease.ProviderAccount),
		OAuth: true, Source: requestcontext.SourceCodexSubscription,
	})
	err := c.checkCodexIncludedQuota(ctx, "")
	var upstream *providers.UpstreamErrorResponse
	if errors.As(err, &upstream) && upstream.Status == http.StatusTooManyRequests {
		return true, nil
	}
	return false, err
}

// CodexResetCredits lists only credits the provider says can be redeemed.
func (c *Client) CodexResetCredits(ctx context.Context, lease subscriptions.Lease) ([]subscriptions.ResetCredit, error) {
	body, err := c.codexResetRequest(ctx, lease, http.MethodGet, "", nil)
	if err != nil {
		return nil, err
	}
	var inventory struct {
		Credits *[]struct {
			ID        string           `json:"id"`
			Type      codexResetType   `json:"reset_type"`
			Status    codexResetStatus `json:"status"`
			ExpiresAt *time.Time       `json:"expires_at"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(body, &inventory); err != nil || inventory.Credits == nil {
		return nil, errors.New("invalid Codex reset credit inventory")
	}
	var credits []subscriptions.ResetCredit
	for _, credit := range *inventory.Credits {
		if credit.Type != codexRateLimitReset || credit.Status != codexResetAvailable || credit.ID == "" {
			continue
		}
		available := subscriptions.ResetCredit{ID: credit.ID}
		if credit.ExpiresAt != nil {
			available.ExpiresAt = *credit.ExpiresAt
		}
		credits = append(credits, available)
	}
	return credits, nil
}

// ConsumeCodexReset uses the same specific-credit and idempotency contract as /usage.
func (c *Client) ConsumeCodexReset(ctx context.Context, lease subscriptions.Lease, creditID, requestID string) (subscriptions.ResetOutcome, error) {
	if creditID == "" || requestID == "" {
		return "", errors.New("Codex reset requires a credit and redemption request ID")
	}
	payload, err := json.Marshal(struct {
		CreditID  string `json:"credit_id"`
		RequestID string `json:"redeem_request_id"`
	}{CreditID: creditID, RequestID: requestID})
	if err != nil {
		return "", err
	}
	body, err := c.codexResetRequest(ctx, lease, http.MethodPost, "/consume", payload)
	if err != nil {
		return "", err
	}
	var redemption struct {
		Code subscriptions.ResetOutcome `json:"code"`
	}
	if err := json.Unmarshal(body, &redemption); err != nil {
		return "", errors.New("invalid Codex reset redemption response")
	}
	switch redemption.Code {
	case subscriptions.ResetApplied, subscriptions.ResetNothingToReset, subscriptions.ResetNoCredit, subscriptions.ResetAlreadyRedeemed:
		return redemption.Code, nil
	default:
		return "", errors.New("unknown Codex reset redemption outcome")
	}
}

func (c *Client) codexResetRequest(ctx context.Context, lease subscriptions.Lease, method, suffix string, payload []byte) ([]byte, error) {
	if lease.AccessToken == "" || lease.ProviderAccount == "" {
		return nil, errors.New("Codex reset requires authenticated account credentials")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	endpoint := strings.TrimSuffix(strings.TrimRight(c.codexBaseURL, "/"), "/codex") + "/wham/rate-limit-reset-credits" + suffix
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+lease.AccessToken)
	req.Header.Set(requestcontext.ChatGPTAccountIDHeader, lease.ProviderAccount)
	req.Header.Set(codexOriginatorHeader, codexOriginatorValue)
	req.Header.Set(codexUserAgentHeader, codexUserAgentValue)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Codex reset request rejected: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
