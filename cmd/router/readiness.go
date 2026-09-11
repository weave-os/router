package main

import (
	"context"
	"fmt"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/router"
)

type databasePinger interface {
	Ping(context.Context) error
}

// strategyAvailability is the proxy registry's view of which strategies have
// a live router behind them.
type strategyAvailability interface {
	PolicyStrategyAvailable(router.Strategy) bool
}

type readinessChecker struct {
	database        databasePinger
	hmm             admin.HealthChecker
	strategies      strategyAvailability
	defaultStrategy router.Strategy
}

// newReadinessChecker gates /readyz on PostgreSQL, the HMM policy snapshot
// when one is wired, and the deployment default strategy being routable. A
// revision whose default strategy would 503 every request must never pass
// its startup probe.
func newReadinessChecker(database databasePinger, hmm admin.HealthChecker, strategies strategyAvailability, defaultStrategy router.Strategy) readinessChecker {
	return readinessChecker{database: database, hmm: hmm, strategies: strategies, defaultStrategy: defaultStrategy}
}

func (c readinessChecker) CheckHealth(ctx context.Context) error {
	if err := c.database.Ping(ctx); err != nil {
		return fmt.Errorf("postgres readiness check failed: %w", err)
	}
	if c.hmm != nil {
		if err := c.hmm.CheckHealth(ctx); err != nil {
			return fmt.Errorf("HMM readiness check failed: %w", err)
		}
	}
	return c.checkDefaultStrategy()
}

func (c readinessChecker) checkDefaultStrategy() error {
	if !c.strategies.PolicyStrategyAvailable(c.defaultStrategy) {
		return fmt.Errorf("default strategy %q has no router configured: %w", c.defaultStrategy, router.ErrStrategyUnavailable)
	}
	return nil
}
