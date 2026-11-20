package sessionpin

import (
	"context"
	"sort"
	"time"

	"weave-os/router/internal/router"

	"github.com/google/uuid"
)

const SessionKeyLen = 16

const DefaultRole = "default"

type Pin struct {
	SessionKey                [SessionKeyLen]byte
	Role                      string
	InstallationID            uuid.UUID
	Provider                  string
	Model                     string
	Effort                    string
	ForcedThreadKey           string
	PairedProvider            string
	PairedModel               string
	Reason                    string
	Strategy                  router.Strategy
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
	LastOutputLimitAt         time.Time
	ConsecutiveUpstreamErrors int
	LastServedModel           string
	HasEverSwitched           bool
	ConsecutiveOverloadErrors int
	ConsecutiveDowngradeVotes int
	ConsecutiveUpgradeVotes   int
	DisabledProviders         []string
	DemotedModels             []string
	DemotionCooldowns         map[string]time.Time
	TurnStrikes               []string
	TokenizerCalibration      TokenizerCalibration
	ModelCaches               ModelCaches
}

type DemotionReason string

const DemotionReasonCommittedStreamFailure DemotionReason = "committed_stream_failure"

const DemotionReasonRescuedFailure DemotionReason = "rescued_failure"

const DemotionReasonResponseHeaderTimeout DemotionReason = "response_header_timeout"

const DemotionReasonUnrescuedStall DemotionReason = "unrescued_stall"

const DemotionReasonRateLimited DemotionReason = "rate_limited"

const DemotionReasonTransientFailure DemotionReason = "transient_failure"

func ActiveCooldowns(cooldowns map[string]time.Time, now time.Time) []string {
	active := make([]string, 0, len(cooldowns))
	for model, until := range cooldowns {
		if now.Before(until) {
			active = append(active, model)
		}
	}
	sort.Strings(active)
	return active
}

type Usage struct {
	Strategy            router.Strategy
	InputTokens         int
	CachedReadTokens    int
	CachedWriteTokens   int
	OutputTokens        int
	EndedAt             time.Time
	OutputLimitReached  bool
	ServedModel         string
	ServedProvider      string
	PriorServedModel    string
	SessionEverSwitched bool
	MessageCount        int
	CacheWriteTTL       time.Duration
}

type Store interface {
	Get(ctx context.Context, sessionKey [SessionKeyLen]byte, role string) (Pin, bool, error)
	Consume(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (Pin, bool, error)
	Upsert(ctx context.Context, p Pin) error
	UpdateUsage(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, usage Usage) error
	IncrementUpstreamErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error)
	ResetUpstreamErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error
	IncrementOverloadErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) (int, error)
	ResetOverloadErrors(ctx context.Context, sessionKey [SessionKeyLen]byte, role string, expectedStrategy router.Strategy) error
	DisableProvider(ctx context.Context, sessionKey [SessionKeyLen]byte, role, provider string, expectedStrategy router.Strategy) error
	ExpireAndDemoteModel(ctx context.Context, expired Pin, model string, reason DemotionReason) error
	SweepExpired(ctx context.Context) error
}

type CooldownStore interface {
	ExpireAndCoolDownModel(ctx context.Context, expired Pin, model string, until time.Time, reason DemotionReason) error
}

type TurnStrikeStore interface {
	StrikeTurnModel(ctx context.Context, sessionKey [SessionKeyLen]byte, role, model string) error
	ClearTurnStrikes(ctx context.Context, sessionKey [SessionKeyLen]byte, role string) error
}

type RecoveryProbeStore interface {
	AcquireRecoveryProbe(ctx context.Context, sessionKey [SessionKeyLen]byte, model string, token uuid.UUID, leaseFor time.Duration) (bool, error)
	ReleaseRecoveryProbe(ctx context.Context, sessionKey [SessionKeyLen]byte, model string, token uuid.UUID) error
	ClearDemotionCooldown(ctx context.Context, sessionKey [SessionKeyLen]byte, role, model string, observedUntil time.Time) error
}
