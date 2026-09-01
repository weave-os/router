package proxy

import (
	"context"
	"time"

	"workweave/router/internal/observability"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
)

// StruggleEscalationStore persists struggle escalation events; Count enforces
// the once-per-(session, role) budget across replicas.
type StruggleEscalationStore interface {
	InsertStruggleEscalationEvent(ctx context.Context, p StruggleEscalationEvent) error
	CountStruggleEscalationEvents(ctx context.Context, sessionKey []byte, role string) (count int64, err error)
}

// StruggleEscalationEvent mirrors one router.struggle_escalation_events row.
type StruggleEscalationEvent struct {
	InstallationID      string
	SessionKey          []byte
	Role                string
	StrugglingModel     string
	Action              string
	EscalationTarget    string
	TurnCount           int32
	WallSeconds         int64
	SessionEverSwitched bool
	// ArmingMode is what crossed first: the turn/wall thresholds
	// (struggleArmingTurnWall) or the behavioral signals (struggleArmingEvidence).
	ArmingMode string
	// EvidenceReasons are the spiral signal classes present at arming time.
	EvidenceReasons []string
}

// StruggleEscalationRoster picks the arm a struggling session moves to.
// EscalationTarget prefers the cheapest cluster above policyGroup, falling back
// to a sideways arm in policyGroup. check(model) validates dispatchability.
type StruggleEscalationRoster interface {
	EscalationTarget(ctx context.Context, policyGroup, currentModel string, exclude map[string]struct{}, check func(model string) bool) (target, cluster string, err error)
}

// Struggle escalation action taxonomy.
const (
	struggleActionUpCluster      = "up_cluster"
	struggleActionSideways       = "sideways"
	struggleActionHoldout        = "holdout"
	struggleActionDisabled       = "disabled"
	struggleActionUserForced     = "user_forced"
	struggleActionNoTarget       = "no_sideways_target"
	struggleActionNoEligibleArms = "no_eligible_arms"
)

// Struggle arming modes: which detector crossed for this escalation.
const (
	struggleArmingTurnWall = "turn_wall"
	struggleArmingEvidence = "evidence"
)

// struggleEvidenceMinTurns guards against imported-history replay: a client
// can present 15+ stalled tool calls on turn one, where a re-pin buys nothing.
const struggleEvidenceMinTurns = 8

// handleStruggleEscalation arms an up-cluster (or sideways) move for a
// grinding session via the turn/wall thresholds or, when enabled, behavioral
// evidence. Must run before routing; idempotent via once-per-session budget.
func (s *Service) handleStruggleEscalation(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	evidence []spiralReason,
) {
	log := observability.FromContext(ctx)

	if s.pinStore == nil {
		return
	}
	pin, found, err := s.pinStore.Get(ctx, sessionKey, role)
	if err != nil {
		log.Error("struggle-escalation: pin lookup failed", "err", err)
		return
	}
	if !found || !pinMatchesEffectiveStrategy(ctx, pin) {
		return
	}

	if pin.Reason == translate.ReasonStruggleEscalation {
		return // already rescued
	}

	userForced := pin.Reason == translate.ReasonUserForceModel

	strugglingModel := pin.Model
	if strugglingModel == "" {
		strugglingModel = "unknown"
	}

	var wall time.Duration
	if !pin.FirstPinnedAt.IsZero() {
		wall = time.Since(pin.FirstPinnedAt)
	}

	// +1: the stored count is completed turns; this in-flight turn is the next.
	turnCount := pin.TurnCount + 1
	reasons := struggleReasons(turnCount, wall)
	timerArmed := len(reasons) > 0 && reasons[0] == struggleReasonEarlyStr

	// Evidence arming fires ahead of the timer, so it must not also fire
	// behind it: any timer reason at all (including the unarmed "late") means
	// the session is the timer's case, whatever signals are present.
	evidenceArmed := len(reasons) == 0 &&
		len(evidence) > 0 &&
		turnCount >= struggleEvidenceMinTurns &&
		s.ResolveStruggleEvidenceArming(ctx)

	if !timerArmed && !evidenceArmed {
		return // not yet struggling, or only "late" (not armed)
	}
	armingMode := struggleArmingTurnWall
	if evidenceArmed {
		armingMode = struggleArmingEvidence
	}

	// Once-per-session budget.
	if s.struggleEscalationStore != nil && installationID != uuid.Nil {
		count, err := s.struggleEscalationStore.CountStruggleEscalationEvents(ctx, sessionKey[:], role)
		if err != nil {
			log.Error("struggle-escalation: budget lookup failed", "err", err)
		} else if count > 0 {
			return
		}
	}

	holdout := s.struggleEscalationStore != nil && installationID != uuid.Nil &&
		DeterministicHoldout(sessionKey, s.ResolveStruggleEscalationHoldoutPct(ctx))

	action := struggleActionSideways
	var escalationTarget, escalationCluster string
	switch {
	case !s.ResolveStruggleEscalationEnabled(ctx):
		action = struggleActionDisabled
	case userForced:
		action = struggleActionUserForced
	case holdout:
		action = struggleActionHoldout
	case pin.PolicyGroup == "":
		action = struggleActionNoTarget
	case s.struggleEscalationRoster == nil:
		action = struggleActionNoTarget
	default:
		target, targetCluster, err := s.struggleEscalationRoster.EscalationTarget(
			ctx, pin.PolicyGroup, pin.Model,
			s.excludedModelsForRequest(ctx),
			func(model string) bool {
				if s.availableModels != nil {
					if _, ok := s.availableModels[model]; !ok {
						return false
					}
				}
				return true
			},
		)
		if err != nil {
			log.Error("struggle-escalation: roster lookup failed", "err", err)
			action = struggleActionNoEligibleArms
		} else if target == "" {
			action = struggleActionNoEligibleArms
		} else {
			m, mok := catalog.ByID(target)
			if !mok || len(m.Providers) == 0 {
				action = struggleActionNoEligibleArms
			} else if installationID == uuid.Nil {
				action = struggleActionNoEligibleArms
			} else {
				escalationTarget = target
				escalationCluster = targetCluster
				if targetCluster != pin.PolicyGroup {
					action = struggleActionUpCluster
				}
				upsertErr := s.pinStore.Upsert(context.Background(), sessionpin.Pin{
					SessionKey:      sessionKey,
					Role:            role,
					InstallationID:  installationID,
					Provider:        m.Providers[0].Provider,
					Model:           target,
					Reason:          translate.ReasonStruggleEscalation,
					Strategy:        router.StrategyFromContext(ctx),
					TurnCount:       1,
					PinnedUntil:     time.Now().Add(pinSessionTTL),
					PolicyGroup:     targetCluster,
					LastServedModel: pin.LastServedModel,
				})
				if upsertErr != nil {
					log.Error("struggle-escalation: pin upsert failed", "err", upsertErr)
					return // no durable row — next turn gets another chance
				}
			}
		}
	}

	evidenceReasons := spiralReasonStrings(evidence)
	log.Info("router.struggle_escalation",
		"struggling_model", strugglingModel,
		"action", action,
		"arming_mode", armingMode,
		"evidence_reasons", evidenceReasons,
		"escalation_target", escalationTarget,
		"escalation_cluster", escalationCluster,
		"user_forced", userForced,
		"turn_count", pin.TurnCount,
		"wall_seconds", int64(wall.Seconds()),
		"session_ever_switched", pin.HasEverSwitched,
		"policy_group", pin.PolicyGroup,
		"session_key_prefix", shortSessionKey(sessionKey),
		"role", role,
	)

	if s.struggleEscalationStore != nil && installationID != uuid.Nil {
		event := StruggleEscalationEvent{
			InstallationID:      installationID.String(),
			SessionKey:          sessionKey[:],
			Role:                role,
			StrugglingModel:     strugglingModel,
			Action:              action,
			EscalationTarget:    escalationTarget,
			TurnCount:           int32(turnCount),
			WallSeconds:         int64(wall.Seconds()),
			SessionEverSwitched: pin.HasEverSwitched,
			ArmingMode:          armingMode,
			EvidenceReasons:     evidenceReasons,
		}
		if err := s.struggleEscalationStore.InsertStruggleEscalationEvent(context.Background(), event); err != nil {
			log.Error("struggle-escalation: event insert failed", "err", err)
		}
	}
}
