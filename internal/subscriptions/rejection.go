package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
)

// A provider rejection is ambiguous on its own (rotated token race, transient
// edge 401, Cloudflare 403), so an account only needs a reconnect after
// strikes sustained over credentialRejectionMinSpan, or back-to-back explicit
// invalid_grant rejections.
const (
	credentialRejectionWindow      = 24 * time.Hour
	credentialRejectionMinSpan     = 6 * time.Hour
	credentialRejectionStrikeLimit = 3
	oauthErrorInvalidGrant         = "invalid_grant"
)

var credentialRejectionBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

type credentialRejectionStore interface {
	RecordSubscriptionCredentialRejection(context.Context, auth.SubscriptionOwner, string, auth.SubscriptionCredentialRejectionInput) (auth.SubscriptionCredentialRejection, error)
}

func reconnectWarranted(rejection auth.SubscriptionCredentialRejection, reason string, now time.Time) bool {
	if rejection.Count >= 2 && reason == oauthErrorInvalidGrant && rejection.PreviousReason == oauthErrorInvalidGrant {
		return true
	}
	return rejection.Count >= credentialRejectionStrikeLimit && now.Sub(rejection.FirstRejectedAt) >= credentialRejectionMinSpan
}

func credentialRejectionCooldown(strikes int) time.Duration {
	index := min(strikes-1, len(credentialRejectionBackoff)-1)
	return credentialRejectionBackoff[index]
}

func refreshRejectionReason(err error) string {
	var oauthErr *OAuthRefreshError
	if errors.As(err, &oauthErr) {
		if oauthErr.Code != "" {
			return oauthErr.Code
		}
		return fmt.Sprintf("refresh_http_%d", oauthErr.Status)
	}
	return "refresh_rejected"
}

// credentialRejectionCooldownError reports a terminal-looking rejection that
// was absorbed as a strike. It is deliberately not Terminal so the pool cools
// the account down instead of marking it reconnect-required.
type credentialRejectionCooldownError struct {
	err   error
	until time.Time
}

func (e *credentialRejectionCooldownError) Error() string {
	return e.err.Error() + " (cooling down until " + e.until.UTC().Format(time.RFC3339) + ")"
}
func (e *credentialRejectionCooldownError) Unwrap() error  { return e.err }
func (e *credentialRejectionCooldownError) Terminal() bool { return false }

// RejectCredential records an upstream 401/403 for a leased account. The
// cached access token is dropped so the next lease refreshes, and the account
// cools down unless its strike history warrants a reconnect, reported as true.
func (r *Runtime) RejectCredential(ctx context.Context, owner auth.SubscriptionOwner, provider Provider, accountID string, upstreamStatus int) (bool, error) {
	store, ok := r.store.(credentialRejectionStore)
	if !ok {
		return true, r.ReconnectRequired(ctx, owner, provider, accountID)
	}
	physicalOwner, err := r.admit(ctx, accountID, owner)
	if err != nil {
		return false, err
	}
	r.manager.InvalidateAccessToken("account:"+accountID, provider, accountID)
	now := r.clock()
	reason := fmt.Sprintf("upstream_http_%d", upstreamStatus)
	rejection, err := store.RecordSubscriptionCredentialRejection(ctx, physicalOwner, accountID, auth.SubscriptionCredentialRejectionInput{
		Reason: reason, RejectedAt: now, Window: credentialRejectionWindow, InvalidateAccessToken: true, UpstreamStatus: upstreamStatus, RequestID: observability.RequestIDFromContext(ctx),
	})
	if err != nil {
		return false, err
	}
	log := observability.FromContext(ctx).With("owner_id", physicalOwner.LogKey(), "account_id", accountID, "provider", provider,
		"upstream_status", upstreamStatus, "strikes", rejection.Count, "first_rejected_at", rejection.FirstRejectedAt)
	if reconnectWarranted(rejection, reason, now) {
		disconnected, err := r.updateAccountHealth(ctx, physicalOwner, accountID, auth.SubscriptionAccountStateReconnectRequired, false, nil)
		if err != nil {
			return false, err
		}
		r.manager.ReconnectRequired("account:"+accountID, provider, accountID)
		if disconnected {
			log.Warn("Subscription account needs reconnect after repeated credential rejections",
				"subscription_event", auth.SubscriptionCredentialEventReconnectRequired, "repeated_disconnect", rejection.RepeatedDisconnect)
		}
		return true, nil
	}
	until := now.Add(credentialRejectionCooldown(rejection.Count))
	log.Warn("Subscription account credential rejected; cooling down", "cooldown_until", until)
	r.manager.Cooldown("account:"+accountID, provider, accountID, until)
	return false, r.store.UpdateSubscriptionAccountCooldown(ctx, physicalOwner, accountID, until)
}

func sanitizeOAuthErrorCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return ""
		}
	}
	return code
}
