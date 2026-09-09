package main

import (
	"context"
	"fmt"

	"weave-os/router/internal/api/admin"
)

type databasePinger interface {
	Ping(context.Context) error
}

type readinessChecker struct {
	database      databasePinger
	hmm           admin.HealthChecker
	recoveryReady bool
}

func newReadinessChecker(database databasePinger, hmm admin.HealthChecker) admin.HealthChecker {
	return readinessChecker{database: database, hmm: hmm}
}

func (c readinessChecker) CheckHealth(ctx context.Context) error {
	_, err := c.CheckReadiness(ctx)
	return err
}

// CheckReadiness reports degraded serving separately from primary policy health.
func (c readinessChecker) CheckReadiness(ctx context.Context) (bool, error) {
	if err := c.database.Ping(ctx); err != nil {
		return false, fmt.Errorf("postgres readiness check failed: %w", err)
	}
	if c.hmm == nil {
		return false, nil
	}
	if err := c.hmm.CheckHealth(ctx); err != nil {
		if c.recoveryReady && ctx.Err() == nil {
			return true, nil
		}
		return false, fmt.Errorf("HMM readiness check failed: %w", err)
	}
	return false, nil
}

type unavailablePolicyHealth struct{}

func (unavailablePolicyHealth) CheckHealth(context.Context) error {
	return fmt.Errorf("primary policy is not configured")
}
