package proxy

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/armid"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/switchpolicy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/google/uuid"
)

func agreementShadowEnabled(ctx context.Context) bool {
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	return managed && policyregistry.ServingTarget(identity.Target) == policyregistry.TargetInternal && identity.StateNamespace != "" && identity.ReleaseID != "" && identity.BindingID != "" && flags.IntOr(ctx, flags.KeyAgreementShadowEpoch, 0) > 0
}

func applyAgreementShadowTelemetry(ports continuity.StrategyPorts, ctx context.Context, p *turnrecord.InsertTelemetryParams, res turnLoopResult) {
	if !agreementShadowEnabled(ctx) {
		return
	}
	if !conversationContextTurn(turntype.TurnType(p.TurnType)) || len(p.SessionKey) == 0 {
		return
	}
	role := p.Role
	if role == "" {
		role = sessionpin.DefaultRole
	}
	epoch := flags.IntOr(ctx, flags.KeyAgreementShadowEpoch, 0)
	stateKey := append([]byte(nil), requestcontext.ServingStateKey(ctx, p.SessionKey)...)
	stateKey = binary.BigEndian.AppendUint64(stateKey, uint64(epoch))
	shadow := &turnrecord.AgreementShadowInput{
		Epoch:       epoch,
		Key:         switchpolicy.ShadowKey{InstallationID: p.InstallationID, SessionKey: stateKey, Role: role},
		Observation: switchpolicy.ShadowObservation{RequestID: p.RequestID, StartedAt: p.Timestamp, Gap: switchpolicy.ShadowGapUnsupported},
	}
	p.AgreementShadow = shadow
	if !res.AuthoritativePerTurn || !isHMMDecision(ports, res.Fresh) || res.HardPinned || res.ForcedPinDropped || res.PolicyFallback || res.CallerModelPassthrough || res.UsageBypass || floor.RoutingApplied(res.Fresh) {
		return
	}
	md, classifier := res.Fresh.Metadata, ports.Facts.ClassifierTelemetryOf(res.Fresh.Metadata)
	if md == nil || classifier.SelectionTrace == nil || classifier.ArtifactSHA256 == "" || md.SelectionPolicySHA256 == "" {
		shadow.Observation.Gap = switchpolicy.ShadowGapMissing
		return
	}
	trace := classifier.SelectionTrace
	if trace.ForcedGroup != "" || trace.ClusterOverridesPresent || (trace.OverrideReason != router.SelectionOverrideNone && trace.OverrideReason != router.SelectionOverrideSlidingThreshold) {
		return
	}
	selectedBase, _ := armid.SplitEffort(trace.SelectedArm)
	selectedModel := armid.CatalogIDForRoster(selectedBase)
	if selectedModel != res.Fresh.Model || md.CandidateProviders[selectedModel] != res.Fresh.Provider {
		shadow.Observation.Gap = switchpolicy.ShadowGapMissing
		return
	}
	group := escalation.Group(classifier.PredictedLabel)
	confidence, scored := classifier.Probabilities[string(group)]
	if escalation.Rank(group) < 0 || !scored || math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		shadow.Observation.Gap = switchpolicy.ShadowGapMissing
		return
	}
	for _, arm := range trace.EffectiveOrders[string(group)] {
		base, _ := armid.SplitEffort(arm)
		if !slices.Contains(trace.CandidateRosterIDs, base) {
			continue
		}
		model := armid.CatalogIDForRoster(base)
		provider := md.CandidateProviders[model]
		if provider == "" {
			continue
		}
		if trace.SelectedGroup == string(group) && (res.Fresh.Model != model || res.Fresh.Provider != provider) {
			shadow.Observation.Gap = switchpolicy.ShadowGapMissing
			return
		}
		if trace.SelectedGroup != string(group) && trace.OverrideReason != router.SelectionOverrideSlidingThreshold {
			shadow.Observation.Gap = switchpolicy.ShadowGapMissing
			return
		}
		shadow.Observation.Proposal = switchpolicy.Selection{Target: switchpolicy.Target{Provider: provider, Model: model}, Group: group}
		shadow.Observation.Confidence = confidence
		identityStrategy := router.Strategy(md.Strategy)
		if identityStrategy == router.StrategyHMMEmbedding {
			identityStrategy = router.StrategyHMM
		}
		shadow.Observation.PolicyIdentity = string(switchpolicy.VersionAgreementV1) + ":" + string(identityStrategy) + ":" + classifier.ArtifactSHA256 + ":" + md.SelectionPolicySHA256 + ":" + md.RosterVersion
		shadow.Observation.LivePin = switchpolicy.Selection{Target: switchpolicy.Target{Provider: res.PinProvider, Model: res.PinModel}, Group: escalation.Group(res.PinPolicyGroup)}
		for model, provider := range md.CandidateProviders {
			shadow.Observation.Eligible = append(shadow.Observation.Eligible, switchpolicy.Target{Provider: provider, Model: model})
		}
		shadow.Observation.Gap = switchpolicy.ShadowGapNone
		return
	}
	shadow.Observation.Gap = switchpolicy.ShadowGapMissing
}

func (s *Service) recordAgreementShadow(ctx context.Context, log *slog.Logger, p turnrecord.InsertTelemetryParams) {
	if p.AgreementShadow == nil {
		return
	}
	store, ok := s.telemetry.(switchpolicy.ShadowStore)
	if !ok {
		log.Warn("Agreement shadow store is unavailable", "installation_id", p.InstallationID)
		return
	}
	shadow := *p.AgreementShadow
	if agreementShadowTransportFailed(p) {
		shadow.Observation.Gap = switchpolicy.ShadowGapFailure
	}
	outcome, err := advanceAgreementShadow(ctx, store, shadow.Key, shadow.Observation)
	if err != nil {
		log.Warn("Agreement shadow update failed", "installation_id", p.InstallationID, "shadow_session_key", hex.EncodeToString(shadow.Key.SessionKey), "role", shadow.Key.Role, "err", err)
		return
	}
	log = log.With("installation_id", p.InstallationID, "shadow_session_key", hex.EncodeToString(shadow.Key.SessionKey), "role", shadow.Key.Role, "experiment_epoch", shadow.Epoch,
		"segment_id", outcome.SegmentID, "invalidated_segment", outcome.InvalidatedSegment, "gap", outcome.Gap,
		"live_model", p.DecisionModel, "live_provider", p.DecisionProvider, "live_error_class", p.ErrorClass, "started_segment", outcome.StartedSegment, "seeded_from_live_pin", outcome.SeededFromLivePin,
		"policy_identity", shadow.Observation.PolicyIdentity, "serving_release_id", p.ServingReleaseID, "serving_binding_id", p.ServingBindingID,
		"request_started_at", shadow.Observation.StartedAt)
	if outcome.Gap != switchpolicy.ShadowGapNone || outcome.Duplicate {
		log.Info("router.agreement_shadow.gap", "duplicate", outcome.Duplicate)
	}
	for _, decision := range []switchpolicy.Decision{outcome.OneUp, outcome.TwoUp} {
		if decision.Version == "" {
			continue
		}
		log.Info("router.agreement_shadow", "policy_version", decision.Version, "upgrade_votes_required", decision.Next.Config.UpgradeVotes, "downgrade_votes_required", decision.Next.Config.DowngradeVotes,
			"minimum_confidence", decision.Next.Config.MinimumConfidence,
			"model", decision.Next.Current.Target.Model, "provider", decision.Next.Current.Target.Provider,
			"group", decision.Next.Current.Group, "direction", decision.Direction, "state_revision", decision.Next.Revision,
			"proposal_model", shadow.Observation.Proposal.Target.Model, "proposal_provider", shadow.Observation.Proposal.Target.Provider,
			"proposal_group", shadow.Observation.Proposal.Group, "confidence", shadow.Observation.Confidence,
			"votes", decision.Votes, "reason", decision.Reason, "override", decision.Override, "shadow_switched", decision.Switched,
			"differs_from_live", decision.Next.Current.Target.Model != p.DecisionModel || decision.Next.Current.Target.Provider != p.DecisionProvider)
	}
}

func agreementShadowTransportFailed(p turnrecord.InsertTelemetryParams) bool {
	if p.UpstreamStatusCode != http.StatusOK || (p.FailoverAttempted != nil && *p.FailoverAttempted) || (p.FailoverUsed != nil && *p.FailoverUsed) {
		return true
	}
	switch p.ErrorClass {
	case "", turnrecord.TurnErrorMaxTokens, turnrecord.TurnErrorRefusal, turnrecord.TurnErrorInvalidToolArgs:
		return false
	default:
		return true
	}
}

func (s *Service) recordAgreementShadowCommandBoundary(ctx context.Context, installationID uuid.UUID, threadKey [sessionpin.SessionKeyLen]byte, commandID string) {
	if !agreementShadowEnabled(ctx) || installationID == uuid.Nil || s.pinStore == nil {
		return
	}
	boundaryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	startedAt := s.clock.Now()
	log := observability.FromContext(ctx).With("command_outcome_id", commandID)
	for _, role := range forceModelClearRoles() {
		p := turnrecord.InsertTelemetryParams{InstallationID: installationID.String(), RequestID: commandID, Timestamp: startedAt,
			SessionKey: threadKey[:], Role: role, TurnType: string(turntype.MainLoop), UpstreamStatusCode: http.StatusOK}
		applyAgreementShadowTelemetry(s.strategyPorts, ctx, &p, turnLoopResult{})
		s.recordAgreementShadow(boundaryCtx, log, p)
	}
}

var errAgreementShadowConflict = errors.New("agreement shadow state changed repeatedly")

func advanceAgreementShadow(ctx context.Context, store switchpolicy.ShadowStore, key switchpolicy.ShadowKey, observation switchpolicy.ShadowObservation) (switchpolicy.ShadowOutcome, error) {
	if key.InstallationID == "" || len(key.SessionKey) == 0 || key.Role == "" {
		return switchpolicy.ShadowOutcome{}, errors.New("agreement shadow namespace is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		prior, found, err := store.GetAgreementShadow(ctx, key)
		if err != nil {
			return switchpolicy.ShadowOutcome{}, err
		}
		next, outcome, err := switchpolicy.EvaluateShadow(prior, found, observation)
		if err != nil || outcome.Duplicate {
			return outcome, err
		}
		committed, err := store.CompareAndSwapAgreementShadow(ctx, key, prior.Revision, next)
		if err != nil {
			return switchpolicy.ShadowOutcome{}, err
		}
		if committed {
			return outcome, nil
		}
	}
	return switchpolicy.ShadowOutcome{}, errAgreementShadowConflict
}
