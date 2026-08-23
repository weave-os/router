package proxy

import (
	"context"
	"time"

	"workweave/router/internal/observability"
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
}

// StruggleEscalationRoster picks the next untried arm in the same cluster.
// SidewaysTarget returns the next-ranked arm skipping currentModel. check(model)
// should validate the candidate is dispatchable (available, binding exists).
type StruggleEscalationRoster interface {
	SidewaysTarget(ctx context.Context, policyGroup, currentModel string, exclude map[string]struct{}, check func(model string) bool) (string, error)
}

// Struggle escalation action taxonomy.
const (
	struggleActionSideways       = "sideways"
	struggleActionHoldout        = "holdout"
	struggleActionDisabled       = "disabled"
	struggleActionUserForced     = "user_forced"
	struggleActionNoTarget       = "no_sideways_target"
	struggleActionNoEligibleArms = "no_eligible_arms"
)

// handleStruggleEscalation arms a sideways move for a grinding session
// (turns >= 30, wall >= 10m). Must run before routing so runTurnLoop picks
// up the sticky pin on the same turn; idempotent via durable once-per-session budget.
func (s *Service) handleStruggleEscalation(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
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
	if !found {
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
	reasons := struggleReasons(pin.TurnCount+1, wall)
	if len(reasons) == 0 || reasons[0] != struggleReasonEarlyStr {
		return // not yet struggling, or only "late" (not armed)
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
		DeterministicHoldout(sessionKey, s.struggleEscalationHoldoutPct)

	action := struggleActionSideways
	var escalationTarget string
	switch {
	case !s.struggleEscalationEnabled:
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
		target, err := s.struggleEscalationRoster.SidewaysTarget(
			ctx, pin.PolicyGroup, pin.Model,
			nil,
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
				upsertErr := s.pinStore.Upsert(context.Background(), sessionpin.Pin{
					SessionKey:      sessionKey,
					Role:            role,
					InstallationID:  installationID,
					Provider:        m.Providers[0].Provider,
					Model:           target,
					Reason:          translate.ReasonStruggleEscalation,
					TurnCount:       1,
					PinnedUntil:     time.Now().Add(pinSessionTTL),
					PolicyGroup:     pin.PolicyGroup,
					LastServedModel: pin.LastServedModel,
				})
				if upsertErr != nil {
					log.Error("struggle-escalation: pin upsert failed", "err", upsertErr)
					return // no durable row — next turn gets another chance
				}
			}
		}
	}

	log.Info("router.struggle_escalation",
		"struggling_model", strugglingModel,
		"action", action,
		"escalation_target", escalationTarget,
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
			TurnCount:           int32(pin.TurnCount + 1), // +1: stored is completed turns; event records the in-flight count
			WallSeconds:         int64(wall.Seconds()),
			SessionEverSwitched: pin.HasEverSwitched,
		}
		if err := s.struggleEscalationStore.InsertStruggleEscalationEvent(context.Background(), event); err != nil {
			log.Error("struggle-escalation: event insert failed", "err", err)
		}
	}
}
