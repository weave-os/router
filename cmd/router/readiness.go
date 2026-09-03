package main

import (
	"context"
	"fmt"

	"workweave/router/internal/api/admin"
)

type databasePinger interface {
	Ping(context.Context) error
}

type readinessChecker struct {
	database databasePinger
	hmm      admin.HealthChecker
}

func newReadinessChecker(database databasePinger, hmm admin.HealthChecker) admin.HealthChecker {
	return readinessChecker{database: database, hmm: hmm}
}

func (c readinessChecker) CheckHealth(ctx context.Context) error {
	if err := c.database.Ping(ctx); err != nil {
		return fmt.Errorf("postgres readiness check failed: %w", err)
	}
	if c.hmm == nil {
		return nil
	}
	if err := c.hmm.CheckHealth(ctx); err != nil {
		return fmt.Errorf("HMM readiness check failed: %w", err)
	}
	return nil
}
