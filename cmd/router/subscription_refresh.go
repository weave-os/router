package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/config"
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

func buildSubscriptionRuntime(enabled bool, authSvc *auth.Service, accounts auth.SubscriptionAccountRepository, logger *slog.Logger) (*subscriptions.Runtime, admin.SubscriptionLoginFlow) {
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
	runtime := subscriptions.NewRuntime(authSvc, oauth, time.Now).WithEarlyRefreshPolicy(policy)
	logger.Info("Server-side subscription account pools enabled")
	return runtime, oauth
}
