package main

import (
	"log/slog"
	"strings"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/config"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newSubscriberAllowance(logger *slog.Logger, pool *pgxpool.Pool, billingSvc *billing.Service) (*entitlement.Service, *billing.Service) {
	if billingSvc == nil || !strings.EqualFold(config.GetOr("ROUTER_SUBSCRIBER_ALLOWANCE_ENABLED", "false"), "true") {
		return nil, billingSvc
	}
	svc := entitlement.NewService(
		postgres.NewSubscriberEntitlementRepo(pool),
		postgres.NewSubscriberAllowanceRepo(pool),
	)
	logger.Info("Individual subscriber allowance enforcement enabled")
	return svc, billingSvc.WithSubscriberAllowance(svc)
}
