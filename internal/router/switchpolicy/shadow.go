package switchpolicy

import (
	"context"
	"errors"
	"slices"
	"time"
)

// ShadowKey is isolated from the live session-pin namespace.
type ShadowKey struct {
	InstallationID string
	SessionKey     []byte
	Role           string
}

type ShadowGap string

const (
	ShadowGapNone          ShadowGap = ""
	ShadowGapUnsupported   ShadowGap = "unsupported_turn"
	ShadowGapFailure       ShadowGap = "failed_or_rescued"
	ShadowGapMissing       ShadowGap = "missing_proposal"
	ShadowGapOutOfOrder    ShadowGap = "out_of_order"
	ShadowGapPolicyChanged ShadowGap = "policy_changed"
)

type ShadowSnapshot struct {
	Revision       int64     `json:"-"`
	LastStartedAt  time.Time `json:"last_started_at"`
	LastRequestID  string    `json:"last_request_id"`
	PolicyIdentity string    `json:"policy_identity"`
	SegmentID      string    `json:"segment_id"`
	OneUp          State     `json:"one_up"`
	TwoUp          State     `json:"two_up"`
}

type ShadowObservation struct {
	RequestID      string
	StartedAt      time.Time
	PolicyIdentity string
	Proposal       Selection
	Confidence     float64
	Eligible       []Target
	LivePin        Selection
	Gap            ShadowGap
}

type ShadowStore interface {
	GetAgreementShadow(context.Context, ShadowKey) (ShadowSnapshot, bool, error)
	CompareAndSwapAgreementShadow(context.Context, ShadowKey, int64, ShadowSnapshot) (bool, error)
}

type ShadowOutcome struct {
	Gap                ShadowGap
	InvalidatedSegment string
	SegmentID          string
	SeededFromLivePin  bool
	StartedSegment     bool
	Duplicate          bool
	OneUp              Decision
	TwoUp              Decision
}

// EvaluateShadow computes both candidate states without I/O. Older completions
// invalidate the segment instead of manufacturing consecutive evidence.
func EvaluateShadow(prior ShadowSnapshot, found bool, observation ShadowObservation) (ShadowSnapshot, ShadowOutcome, error) {
	if observation.RequestID == "" || observation.StartedAt.IsZero() {
		return ShadowSnapshot{}, ShadowOutcome{}, errors.New("agreement shadow request identity and time are required")
	}
	if found && prior.LastRequestID == observation.RequestID {
		return prior, ShadowOutcome{Duplicate: true, SegmentID: prior.SegmentID}, nil
	}
	next := prior
	outcome := ShadowOutcome{}
	gap := observation.Gap
	if found && !observation.StartedAt.After(prior.LastStartedAt) {
		gap = ShadowGapOutOfOrder
	}
	if gap != ShadowGapNone {
		outcome.Gap = gap
		outcome.InvalidatedSegment = prior.SegmentID
		next.OneUp, next.TwoUp = State{}, State{}
		next.SegmentID, next.PolicyIdentity = "", ""
	} else {
		if observation.PolicyIdentity == "" {
			return ShadowSnapshot{}, ShadowOutcome{}, errors.New("agreement shadow policy identity is required")
		}
		if prior.PolicyIdentity != "" && prior.PolicyIdentity != observation.PolicyIdentity {
			outcome.Gap = ShadowGapPolicyChanged
			outcome.InvalidatedSegment = prior.SegmentID
			next.OneUp, next.TwoUp = State{}, State{}
			next.SegmentID = ""
		}
		if next.SegmentID == "" {
			outcome.StartedSegment = true
			next.SegmentID = observation.RequestID
			if observation.LivePin.validate() == nil && slices.Contains(observation.Eligible, observation.LivePin.Target) {
				next.OneUp.Current, next.TwoUp.Current = observation.LivePin, observation.LivePin
				outcome.SeededFromLivePin = true
			}
		}
		next.PolicyIdentity = observation.PolicyIdentity
		outcome.SegmentID = next.SegmentID
		configs := [2]Config{{MinimumConfidence: .5, UpgradeVotes: 1, DowngradeVotes: 5}, {MinimumConfidence: .5, UpgradeVotes: 2, DowngradeVotes: 5}}
		states := [2]*State{&next.OneUp, &next.TwoUp}
		decisions := [2]*Decision{&outcome.OneUp, &outcome.TwoUp}
		for i, state := range states {
			override := OverrideNone
			if state.Current.Target != (Target{}) && !slices.Contains(observation.Eligible, state.Current.Target) {
				override = OverrideIncumbentIneligible
			}
			decision, err := Evaluate(configs[i], *state, Observation{
				ID: observation.RequestID, ExpectedRevision: state.Revision,
				Proposal: observation.Proposal, Confidence: observation.Confidence,
				Eligible: slices.Contains(observation.Eligible, observation.Proposal.Target), Override: override,
			})
			if err != nil {
				return ShadowSnapshot{}, ShadowOutcome{}, err
			}
			*state = decision.Next
			*decisions[i] = decision
		}
	}
	if observation.StartedAt.After(next.LastStartedAt) {
		next.LastStartedAt = observation.StartedAt
	}
	next.LastRequestID = observation.RequestID
	next.Revision = prior.Revision + 1
	return next, outcome, nil
}
