package apm

import (
	"context"
	"sync"
	"time"

	"weave-os/router/internal/router/policy"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type RecoveryOutcome string

const (
	RecoveryAuthorized  RecoveryOutcome = "authorized"
	RecoveryUnavailable RecoveryOutcome = "no_authorized_target"
	RecoveryServed      RecoveryOutcome = "served"
	RecoveryFailed      RecoveryOutcome = "failed"
)

var recoveryMetricsOnce sync.Once
var recoveryCounter metric.Int64Counter
var decisionMetricsOnce sync.Once
var decisionDuration metric.Float64Histogram

// RecordPolicyDecision includes rejected admission and failed classification in latency metrics.
func RecordPolicyDecision(ctx context.Context, failureReason policy.FailureReason, elapsed time.Duration, circuitOpen bool) {
	decisionMetricsOnce.Do(func() {
		decisionDuration, _ = otel.Meter("weave-os/router/policy").Float64Histogram("router.policy.decision.duration", metric.WithUnit("s"))
	})
	if decisionDuration != nil {
		decisionDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.String("policy.failure", string(failureReason)), attribute.Bool("policy.circuit_open", circuitOpen)))
	}
}

// RecordPolicyRecovery includes pre-dispatch failures. Identifiers and payloads
// belong in correlated logs, never these bounded metric dimensions.
func RecordPolicyRecovery(ctx context.Context, failureReason policy.FailureReason, outcome RecoveryOutcome) {
	recoveryMetricsOnce.Do(func() {
		recoveryCounter, _ = otel.Meter("weave-os/router/policy").Int64Counter("router.policy.recovery")
	})
	if recoveryCounter != nil {
		recoveryCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("policy.failure", string(failureReason)), attribute.String("recovery.outcome", string(outcome))))
	}
}
