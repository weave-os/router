// Package switchpolicy evaluates destination-specific model agreement without I/O.
// It is exercised by the offline replay command; serving integration is separate.
package switchpolicy

import (
	"errors"
	"fmt"
	"math"

	"weave-os/router/internal/router/escalation"
)

type Version string

const VersionAgreementV1 Version = "agreement_v1"

type Config struct {
	MinimumConfidence float64 `json:"minimum_confidence"`
	UpgradeVotes      int     `json:"upgrade_votes"`
	DowngradeVotes    int     `json:"downgrade_votes"`
}

func (c Config) Validate() error {
	if math.IsNaN(c.MinimumConfidence) || math.IsInf(c.MinimumConfidence, 0) || c.MinimumConfidence < 0 || c.MinimumConfidence > 1 {
		return errors.New("minimum confidence must be finite and in [0,1]")
	}
	if c.UpgradeVotes < 1 || c.DowngradeVotes < 1 {
		return errors.New("upgrade and downgrade vote requirements must be positive")
	}
	return nil
}

type Target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type Selection struct {
	Target Target           `json:"target"`
	Group  escalation.Group `json:"group"`
}

func (s Selection) validate() error {
	if s.Target.Provider == "" || s.Target.Model == "" || escalation.Rank(s.Group) < 0 {
		return errors.New("selection needs a provider, model, and canonical quality group")
	}
	return nil
}

type Override string

const (
	OverrideNone                Override = ""
	OverrideManual              Override = "manual"
	OverrideIncumbentIneligible Override = "incumbent_ineligible"
	OverrideRecovery            Override = "recovery"
	OverrideQualityFloor        Override = "quality_floor"
)

type Direction string

const (
	DirectionInitial   Direction = "initial"
	DirectionUnchanged Direction = "unchanged"
	DirectionUpgrade   Direction = "upgrade"
	DirectionDowngrade Direction = "downgrade"
	DirectionLateral   Direction = "lateral"
)

type Reason string

const (
	ReasonInitial    Reason = "initial"
	ReasonIncumbent  Reason = "incumbent_confirmed"
	ReasonConfidence Reason = "insufficient_confidence"
	ReasonIneligible Reason = "proposal_ineligible"
	ReasonAgreement  Reason = "waiting_for_agreement"
	ReasonSwitch     Reason = "agreement_reached"
	ReasonOverride   Reason = "explicit_override"
	ReasonDuplicate  Reason = "duplicate_observation"
)

type State struct {
	Current         Selection `json:"current"`
	Challenger      Selection `json:"challenger"`
	Votes           int       `json:"votes"`
	Revision        uint64    `json:"revision"`
	LastObservation string    `json:"last_observation"`
	Config          Config    `json:"config"`
}

type Observation struct {
	ID               string    `json:"id"`
	ExpectedRevision uint64    `json:"expected_revision"`
	Proposal         Selection `json:"proposal"`
	Confidence       float64   `json:"confidence"`
	Eligible         bool      `json:"eligible"`
	Override         Override  `json:"override,omitempty"`
}

type Decision struct {
	Version       Version   `json:"version"`
	BaseRevision  uint64    `json:"base_revision"`
	Direction     Direction `json:"direction"`
	Reason        Reason    `json:"reason"`
	Override      Override  `json:"override,omitempty"`
	RequiredVotes int       `json:"required_votes"`
	Votes         int       `json:"votes"`
	Switched      bool      `json:"switched"`
	Next          State     `json:"next"`
}

var ErrStaleRevision = errors.New("switch policy state revision changed")

// Evaluate returns candidate state. A durable store must atomically compare
// BaseRevision and commit Next; Evaluate alone cannot serialize database writes.
func Evaluate(config Config, state State, observation Observation) (Decision, error) {
	if err := config.Validate(); err != nil {
		return Decision{}, err
	}
	if err := observation.Proposal.validate(); err != nil {
		return Decision{}, err
	}
	if observation.ID == "" {
		return Decision{}, errors.New("observation id is required")
	}
	if math.IsNaN(observation.Confidence) || math.IsInf(observation.Confidence, 0) || observation.Confidence < 0 || observation.Confidence > 1 {
		return Decision{}, errors.New("observation confidence must be finite and in [0,1]")
	}
	switch observation.Override {
	case OverrideNone, OverrideManual, OverrideIncumbentIneligible, OverrideRecovery, OverrideQualityFloor:
	default:
		return Decision{}, fmt.Errorf("unknown switch override %q", observation.Override)
	}
	if state.Current.Target != (Target{}) {
		if err := state.Current.validate(); err != nil {
			return Decision{}, fmt.Errorf("current state: %w", err)
		}
	}
	if state.Votes < 0 || state.Votes > 0 && state.Challenger == (Selection{}) {
		return Decision{}, errors.New("invalid challenger vote state")
	}
	if state.Challenger != (Selection{}) {
		if err := state.Challenger.validate(); err != nil {
			return Decision{}, fmt.Errorf("challenger state: %w", err)
		}
	}
	decision := Decision{Version: VersionAgreementV1, BaseRevision: state.Revision, Direction: DirectionUnchanged, Next: state}
	if observation.ID == state.LastObservation {
		decision.Reason = ReasonDuplicate
		return decision, nil
	}
	if observation.ExpectedRevision != state.Revision {
		return Decision{}, ErrStaleRevision
	}
	if state.Revision == math.MaxUint64 {
		return Decision{}, errors.New("switch policy revision exhausted")
	}
	decision.Next.Revision++
	decision.Next.LastObservation = observation.ID
	decision.Next.Config = config
	decision.Next.Challenger = Selection{}
	decision.Next.Votes = 0
	if state.Config != config {
		state.Challenger = Selection{}
		state.Votes = 0
	}
	if !observation.Eligible {
		if state.Current.Target == (Target{}) || observation.Override != OverrideNone {
			return Decision{}, errors.New("no eligible proposal for initial selection or override")
		}
		decision.Reason = ReasonIneligible
		return decision, nil
	}
	if state.Current.Target == (Target{}) {
		decision.Direction = DirectionInitial
		decision.Reason = ReasonInitial
		decision.Next.Current = observation.Proposal
		decision.Override = observation.Override
		return decision, nil
	}
	if state.Current.Target == observation.Proposal.Target {
		decision.Direction = DirectionUnchanged
		decision.Reason = ReasonIncumbent
		// A classifier label for the same model cannot silently reclassify its
		// accepted band and turn the next downgrade into a cheaper upgrade.
		if observation.Override == OverrideQualityFloor {
			decision.Next.Current.Group = escalation.Higher(state.Current.Group, observation.Proposal.Group)
		} else if observation.Override != OverrideNone {
			decision.Next.Current = observation.Proposal
		}
		decision.Override = observation.Override
		return decision, nil
	}
	decision.RequiredVotes = config.DowngradeVotes
	switch current, proposed := escalation.Rank(state.Current.Group), escalation.Rank(observation.Proposal.Group); {
	case proposed > current:
		decision.Direction = DirectionUpgrade
		decision.RequiredVotes = config.UpgradeVotes
	case proposed < current:
		decision.Direction = DirectionDowngrade
	default:
		decision.Direction = DirectionLateral
	}
	if observation.Override != OverrideNone {
		if observation.Override == OverrideQualityFloor && decision.Direction != DirectionUpgrade {
			return Decision{}, errors.New("quality floor override must raise the incumbent quality group")
		}
		decision.Override = observation.Override
		decision.Reason = ReasonOverride
		decision.Next.Current = observation.Proposal
		decision.Switched = true
		return decision, nil
	}
	if observation.Confidence < config.MinimumConfidence {
		decision.Reason = ReasonConfidence
		return decision, nil
	}
	decision.Votes = 1
	if state.Challenger == observation.Proposal {
		decision.Votes = state.Votes
		if decision.Votes < decision.RequiredVotes {
			decision.Votes++
		}
	}
	if decision.Votes >= decision.RequiredVotes {
		decision.Reason = ReasonSwitch
		decision.Next.Current = observation.Proposal
		decision.Switched = true
		return decision, nil
	}
	decision.Reason = ReasonAgreement
	decision.Next.Challenger = observation.Proposal
	decision.Next.Votes = decision.Votes
	return decision, nil
}

// Commit checks a candidate against the latest value. Database integration must
// enforce the equivalent condition in its update, not read then call Commit.
func Commit(current State, decision Decision) (State, error) {
	if current.Revision != decision.BaseRevision {
		return current, ErrStaleRevision
	}
	return decision.Next, nil
}
