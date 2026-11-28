package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/config"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	anthropicProvider "weave-os/router/internal/providers/anthropic"
	openaiProvider "weave-os/router/internal/providers/openai"
	"weave-os/router/internal/subscriptions"
)

func subscriptionRefreshPolicy(rawIDs, rawAge string) (subscriptions.EarlyRefreshPolicy, error) {
	if strings.TrimSpace(rawIDs) == "" && strings.TrimSpace(rawAge) == "" {
		return subscriptions.EarlyRefreshPolicy{}, nil
	}
	age, err := time.ParseDuration(strings.TrimSpace(rawAge))
	if err != nil {
		return subscriptions.EarlyRefreshPolicy{}, fmt.Errorf("invalid ROUTER_SUBSCRIPTION_EARLY_REFRESH_MAX_AGE: %w", err)
	}
	var ids []string
	for _, raw := range strings.Split(rawIDs, ",") {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return subscriptions.EarlyRefreshPolicy{}, fmt.Errorf("invalid early-refresh account ID: %w", err)
		}
		ids = append(ids, id.String())
	}
	return subscriptions.NewEarlyRefreshPolicy(ids, age)
}

const (
	defaultSubscriptionKeepWarmMaxAge = 7 * 24 * time.Hour
	subscriptionKeepWarmInterval      = 15 * time.Minute
	subscriptionKeepWarmBatchLimit    = 10
	subscriptionKeepWarmSeatSpacing   = 2 * time.Second
	subscriptionKeepWarmTimeout       = 5 * time.Minute
)

func subscriptionKeepWarmPolicy(rawAge string) (subscriptions.KeepWarmPolicy, error) {
	policy := subscriptions.KeepWarmPolicy{MaxAge: defaultSubscriptionKeepWarmMaxAge, BatchLimit: subscriptionKeepWarmBatchLimit,
		SeatSpacing: subscriptionKeepWarmSeatSpacing}
	if raw := strings.TrimSpace(rawAge); raw != "" {
		age, err := time.ParseDuration(raw)
		if err != nil {
			return subscriptions.KeepWarmPolicy{}, fmt.Errorf("invalid ROUTER_SUBSCRIPTION_KEEP_WARM_MAX_AGE: %w", err)
		}
		if age < time.Hour {
			return subscriptions.KeepWarmPolicy{}, fmt.Errorf("ROUTER_SUBSCRIPTION_KEEP_WARM_MAX_AGE must be at least 1h, got %s", age)
		}
		policy.MaxAge = age
	}
	return policy, nil
}

func jitteredKeepWarmDelay(low, span time.Duration) time.Duration {
	offset, err := rand.Int(rand.Reader, big.NewInt(int64(span)+1))
	if err != nil {
		return low + span/2
	}
	return low + time.Duration(offset.Int64())
}

func runSubscriptionKeepWarm(ctx context.Context, runtime *subscriptions.Runtime, policy subscriptions.KeepWarmPolicy, logger *slog.Logger) {
	delay := jitteredKeepWarmDelay(0, subscriptionKeepWarmInterval)
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		sweepCtx, cancel := context.WithTimeout(ctx, subscriptionKeepWarmTimeout)
		result, err := runtime.KeepWarm(sweepCtx, policy)
		cancel()
		switch {
		case err != nil:
			logger.Error("Subscription keep-warm sweep failed", "due", result.Due, "refreshed", result.Refreshed, "failed", result.Failed, "err", err)
		case result.Due > 0:
			logger.Info("Subscription keep-warm sweep finished", "due", result.Due, "refreshed", result.Refreshed, "failed", result.Failed)
		}
		delay = jitteredKeepWarmDelay(subscriptionKeepWarmInterval*3/4, subscriptionKeepWarmInterval/2)
	}
}

type providerQuotaProber struct {
	codex  *openaiProvider.Client
	claude *anthropicProvider.Client
}

func (p providerQuotaProber) ProbeQuota(ctx context.Context, provider subscriptions.Provider, accessToken, accountID string) (subscriptions.QuotaProbe, error) {
	switch {
	case provider == subscriptions.ProviderCodex && p.codex != nil:
		err := p.codex.ProbeCodexQuota(ctx, accessToken, accountID)
		if err == nil {
			return subscriptions.QuotaProbe{Available: true}, nil
		}
		if resetAt, exhausted := credentials.CodexQuotaExhaustion(err); exhausted {
			return subscriptions.QuotaProbe{ResetAt: resetAt}, nil
		}
		return subscriptions.QuotaProbe{}, err
	case provider == subscriptions.ProviderClaude && p.claude != nil && p.claude.SupportsSubscriptions():
		quota, err := p.claude.SubscriptionQuota(ctx, []byte(accessToken))
		return subscriptions.QuotaProbe{Available: quota.Available, ResetAt: quota.ResetAt}, err
	default:
		return subscriptions.QuotaProbe{}, fmt.Errorf("no quota check for %s subscriptions", provider)
	}
}

func subscriptionQuotaProber(codex, claude providers.Client) subscriptions.QuotaProber {
	prober := providerQuotaProber{}
	prober.codex, _ = codex.(*openaiProvider.Client)
	prober.claude, _ = claude.(*anthropicProvider.Client)
	return prober
}

func buildSubscriptionRuntime(enabled bool, authSvc *auth.Service, accounts auth.SubscriptionAccountRepository, prober subscriptions.QuotaProber, logger *slog.Logger) (*subscriptions.Runtime, admin.SubscriptionLoginFlow) {
	if !enabled {
		logger.Info("Server-side subscription account pools disabled")
		return nil, nil
	}
	authSvc.WithSubscriptionAccounts(accounts)
	codexTokenURL := ""
	if issuer := strings.TrimRight(config.GetOr("WEAVE_CODEX_OAUTH_ISSUER", ""), "/"); issuer != "" {
		codexTokenURL = issuer + "/oauth/token"
	}
	oauth := subscriptions.NewOAuthClient(&http.Client{Timeout: subscriptions.RefreshHTTPTimeout}, codexTokenURL,
		config.GetOr("WEAVE_ANTHROPIC_OAUTH_TOKEN", ""), time.Now)
	authSvc.WithCodexEnrollmentVerifier(oauth)
	policy, err := subscriptionRefreshPolicy(os.Getenv("ROUTER_SUBSCRIPTION_EARLY_REFRESH_ACCOUNT_IDS"), os.Getenv("ROUTER_SUBSCRIPTION_EARLY_REFRESH_MAX_AGE"))
	if err != nil {
		panic(err)
	}
	runtime := subscriptions.NewRuntime(authSvc, oauth, time.Now).WithEarlyRefreshPolicy(policy).WithQuotaProber(prober)
	logger.Info("Server-side subscription account pools enabled")
	return runtime, oauth
}

func startSubscriptionKeepWarm(ctx context.Context, runtime *subscriptions.Runtime, logger *slog.Logger) func(context.Context) {
	if runtime == nil {
		return func(context.Context) {}
	}
	policy, err := subscriptionKeepWarmPolicy(config.GetOr("ROUTER_SUBSCRIPTION_KEEP_WARM_MAX_AGE", ""))
	if err != nil {
		panic(err)
	}
	logger.Info("Subscription keep-warm enabled", "max_age", policy.MaxAge)
	done := make(chan struct{})
	safeGo(logger, "subscription-keep-warm", func() {
		defer close(done)
		runSubscriptionKeepWarm(ctx, runtime, policy, logger)
	})
	return func(shutdownCtx context.Context) {
		select {
		case <-done:
		case <-shutdownCtx.Done():
			logger.Warn("Subscription keep-warm sweep still running at shutdown")
		}
	}
}
