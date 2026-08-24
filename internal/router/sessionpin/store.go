// Package sessionpin defines the inner-ring contract for session-sticky
// routing pins (pure types + interface); the Postgres adapter lives in internal/postgres.
package sessionpin

import (
	"context"
	"time"

	"workweave/router/internal/router"

	"github.com/google/uuid"
)

// SessionKeyLen is the sha256-truncated session key length. 16 bytes makes
// collisions astronomically rare at per-(api-key, session) granularity.
const SessionKeyLen = 16

// DefaultRole is the only role emitted in Stage 1; column exists so the
// turn-type detector can land non-breakingly.
const DefaultRole = "default"

// Pin is one row in the session-pin table, mirroring a routing decision so a
// hit can rehydrate router.Decision without re-running the scorer.
//
// Last*Tokens / LastTurnEndedAt hold the previous turn's usage, written by
// Store.UpdateUsage; Upsert leaves them untouched so a start-of-turn refresh
// can't clobber them with zeros.
//
// ConsecutiveUpstreamErrors counts consecutive non-retryable upstream errors
// (4xx other than 408/429); IncrementUpstreamErrors bumps it and the turn loop
// evicts at the two-strike threshold. Upsert preserves it on a same-model
// refresh, resets it on a model switch.
type Pin struct {
	SessionKey     [SessionKeyLen]byte
	Role           string
	InstallationID uuid.UUID
	Provider       string
	Model          string
	// PairedProvider/PairedModel are the scorer's runner-up pick, refreshed by
	// Upsert on a genuine scorer re-run, preserved on a same-model refresh, and
	// cleared on a model change without a fresh pair (force-model, loop-break,
	// eviction) so the stored pair never goes stale. Empty for non-scorer pins
	// or single-candidate routing; a later per-turn policy swaps between the
	// pair without re-scoring.
	PairedProvider string
	PairedModel    string
	Reason         string
	// Strategy is the routing strategy that produced the pin. During rollout,
	// non-beta stable strategies may accept an empty legacy value; beta never does.
	Strategy router.Strategy
	// PolicyGroup is the HMM complexity cluster the pinned decision came from
	// (RoutingMetadata.PolicyGroup). Compared to the fresh decision's group to
	// distinguish a within-cluster reroute from a genuine cluster change.
	PolicyGroup               string
	TurnCount                 int
	PinnedUntil               time.Time
	FirstPinnedAt             time.Time
	LastSeenAt                time.Time
	LastInputTokens           int
	LastCachedReadTokens      int
	LastCachedWriteTokens     int
	LastOutputTokens          int
	LastTurnEndedAt           time.Time
	ConsecutiveUpstreamErrors int
	// LastServedModel is the model that served the previous turn (written by
	// UpdateUsage, untouched by Upsert). Comparing it to the new target model
	// detects a mid-session switch, so stale Anthropic thinking-block
	// signatures that would 400 on the new model get stripped.
	LastServedModel string
	// HasEverSwitched latches true (in UpdateUsage) the first time the session
	// serves a model different from LastServedModel, and stays set for the
	// pin's life. The emit path ORs it into ModelSwitched so stale-signed
	// thinking blocks from an earlier cross-model excursion are stripped on
	// every later turn, not just the one the switch happened on.
	HasEverSwitched bool
	// ConsecutiveOverloadErrors counts consecutive 529-exhausted turns on the
	// pinned provider. Distinct from ConsecutiveUpstreamErrors: a 529 is
	// retryable in-turn, so it never trips that counter.
	ConsecutiveOverloadErrors int
	// DisabledProviders are providers struck out for this pin's session
	// after repeated 529 exhaustion (see DisableProvider). Only grows for
	// the life of the row; Upsert never touches it.
	DisabledProviders []string
}

// Usage captures the previous turn's upstream token accounting.
type Usage struct {
	// Strategy is the routing strategy whose pin may receive this usage. The
	// update is ignored when the row has since been replaced by another strategy.
	Strategy          router.Strategy
	InputTokens       int
	CachedReadTokens  int
	CachedWriteTokens int
	OutputTokens      int
	EndedAt           time.Time
	// ServedModel is the model that served the turn this usage came from.
	ServedModel string
	// ServedProvider is the provider binding that served the turn. UpdateUsage
	// persists it into Provider so the next policy sees actual cache affinity.
	ServedProvider string
	// PriorServedModel is optional prior-turn evidence known by the caller.
	// UpdateUsage uses it to latch HasEverSwitched when the stored row is new
	// or has no last_served_model yet.
	PriorServedModel string
	// SessionEverSwitched carries an already-latched sibling-row verdict so a
	// fresh role row preserves switch history even when its model is unchanged.
	SessionEverSwitched bool
}

// Store is the I/O surface for session pins. Get returns (zero, false, nil)
// when no row exists. In-place mutations are no-ops on an evicted, missing, or
// different-strategy pin so a late request cannot mutate a replacement row.
//
// IncrementUpstreamErrors atomically bumps the error counter and returns the
// new count so the turn loop can two-strike-evict without a cross-pod
// read-modify-write race; it returns (0, nil) for a missing pin.
type Store interface {
	Get(ctx context.Context, sessionKey [SessionKeyLen]byte, role string) (Pin, bool, error)
	// Consume atomically removes and returns one unexpired pin. It is used for
	// one-shot continuations, where a Get followed by an expiry write could let
	// two concurrent requests reuse the same pin.
	Consume(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (Pin, bool, error)
	Upsert(ctx context.Context, p Pin) error
	UpdateUsage(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, usage Usage) error
	IncrementUpstreamErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error)
	ResetUpstreamErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error
	// IncrementOverloadErrors atomically bumps ConsecutiveOverloadErrors and
	// returns the new count, mirroring IncrementUpstreamErrors but for
	// client-visible 529 exhaustion instead of non-retryable 4xx.
	IncrementOverloadErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error)
	// ResetOverloadErrors clears ConsecutiveOverloadErrors after a successful
	// turn, mirroring ResetUpstreamErrors.
	ResetOverloadErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error
	// DisableProvider appends provider to DisabledProviders (deduped) and
	// resets ConsecutiveOverloadErrors in the same write.
	DisableProvider(ctx context.Context, sessionKey [SessionKeyLen]byte, role, provider string, expectedStrategy router.Strategy) error
	SweepExpired(ctx context.Context) error
}
