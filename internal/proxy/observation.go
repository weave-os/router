package proxy

import (
	"context"
	"encoding/json"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/timing"
)

func applyServingSpanAttrs(ctx context.Context, attributes *telemetry.AttrBuilder) {
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	if !managed {
		return
	}
	attributes.String("serving.target", identity.Target).
		String("serving.activation_id", identity.ActivationID).
		String("serving.release_id", identity.ReleaseID).
		String("serving.binding_id", identity.BindingID).
		String("serving.profile_key", identity.ProfileKey).
		String("serving.profile_name", identity.ProfileName).
		String("serving.profile_revision", identity.ProfileRevision).
		String("routing.plan", identity.Plan).
		Int64("routing.entitlement_version", identity.EntitlementVersion).
		Int64("serving.binding_generation", identity.BindingGeneration)
}

type observationContext struct {
	CandidateModels          []string
	ChosenScore              *float64
	Strategy                 string
	RouteID                  string
	PolicyRouteKey           string
	PolicyArtifactID         string
	PolicyArtifactSHA256     string
	RosterVersion            string
	ClassifierArtifactID     string
	ClassifierArtifactSHA256 string
	ClassifierPredictedLabel string
	ClassifierClassOrder     []string
	ClassifierProbabilities  []byte
	SelectionPolicyReleaseID string
	SelectionPolicySHA256    string
	SelectionHeadGeneration  int64
	SelectionTrace           []byte
	SidecarSchemaVersion     string
	TrainingAllowed          bool
	CaptureMode              string
	RolloutID                string
	DebugRef                 string
	TTFTMs                   *int64
	FirstOutputMs            *int64
	CandidateScores          []byte
	Propensity               *float64
	FreshDecisionModel       string
	FreshClassifierMargin    *float64
	FreshPolicyGroup         string
	FreshCandidateScores     []byte
}

func buildObservationContext(ports continuity.StrategyPorts, ctx context.Context, strategy router.Strategy, decision, fresh router.Decision, captureMode ContentCaptureMode) observationContext {
	debugEnabled, _ := ctx.Value(PolicyDebugEnabledContextKey{}).(bool)
	obs := observationContext{
		TrainingAllowed: policyTrainingAllowedForRequest(ctx),
		CaptureMode:     captureMode.String(),
		RolloutID:       ClientIdentityFrom(ctx).RolloutID,
	}
	if rolloutID, ok := ctx.Value(PolicyRolloutIDContextKey{}).(string); ok && rolloutID != "" {
		obs.RolloutID = rolloutID
	}
	if decision.Metadata != nil || fresh.Model != "" {
		obs.Strategy = string(strategy)
	}
	if fresh.Model != "" {
		obs.FreshDecisionModel = fresh.Model
	}
	if fresh.Metadata != nil {
		obs.FreshClassifierMargin, obs.FreshPolicyGroup = ports.Facts.ClassifierTelemetryOf(fresh.Metadata).Margin, ports.Facts.ClassifierTelemetryOf(fresh.Metadata).PolicyGroup
	}
	if md := fresh.Metadata; md != nil && len(md.CandidateScores) > 0 {
		if b, err := json.Marshal(md.CandidateScores); err == nil {
			obs.FreshCandidateScores = b
		} else {
			observability.FromContext(ctx).Debug("Failed to marshal fresh_candidate_scores for telemetry", "err", err)
		}
	}
	if md := decision.Metadata; md != nil {
		classifier := ports.Facts.ClassifierTelemetryOf(md)
		if len(md.CandidateModels) > 0 {
			obs.CandidateModels = append([]string(nil), md.CandidateModels...)
		}
		score := float64(md.ChosenScore)
		obs.ChosenScore = &score
		if md.Strategy != "" {
			obs.Strategy = md.Strategy
		}
		obs.RouteID = md.RouteID
		obs.PolicyRouteKey = md.PolicyRouteKey
		obs.PolicyArtifactID = md.PolicyArtifactID
		obs.PolicyArtifactSHA256 = md.PolicyArtifactSHA256
		obs.RosterVersion = md.RosterVersion
		obs.ClassifierArtifactID, obs.ClassifierArtifactSHA256 = classifier.ArtifactID, classifier.ArtifactSHA256
		obs.ClassifierPredictedLabel = classifier.PredictedLabel
		obs.ClassifierClassOrder = append([]string(nil), classifier.ClassOrder...)
		if len(classifier.Probabilities) > 0 {
			if probabilities, err := json.Marshal(classifier.Probabilities); err == nil {
				obs.ClassifierProbabilities = probabilities
			} else {
				observability.FromContext(ctx).Debug("Failed to marshal classifier probabilities for telemetry", "err", err)
			}
		}
		obs.SelectionPolicyReleaseID = md.SelectionPolicyReleaseID
		obs.SelectionPolicySHA256 = md.SelectionPolicySHA256
		obs.SelectionHeadGeneration = md.SelectionHeadGeneration
		if classifier.SelectionTrace != nil {
			if selectionTrace, err := json.Marshal(classifier.SelectionTrace); err == nil {
				obs.SelectionTrace = selectionTrace
			} else {
				observability.FromContext(ctx).Debug("Failed to marshal selection trace for telemetry", "err", err)
			}
		}
		obs.SidecarSchemaVersion = md.SidecarSchemaVersion
		if debugEnabled {
			obs.DebugRef = md.DebugRef
		}
		if md.Propensity > 0 {
			prop := float64(md.Propensity)
			obs.Propensity = &prop
		}
		if len(md.CandidateScores) > 0 {
			if b, err := json.Marshal(md.CandidateScores); err == nil {
				obs.CandidateScores = b
			} else {
				observability.FromContext(ctx).Debug("Failed to marshal candidate_scores for telemetry", "err", err)
			}
		}
	}
	if md := fresh.Metadata; md != nil {
		classifier := ports.Facts.ClassifierTelemetryOf(md)
		if obs.RouteID == "" {
			obs.RouteID = md.RouteID
		}
		if obs.PolicyRouteKey == "" {
			obs.PolicyRouteKey = md.PolicyRouteKey
		}
		if obs.PolicyArtifactID == "" {
			obs.PolicyArtifactID = md.PolicyArtifactID
		}
		if obs.PolicyArtifactSHA256 == "" {
			obs.PolicyArtifactSHA256 = md.PolicyArtifactSHA256
		}
		if obs.RosterVersion == "" {
			obs.RosterVersion = md.RosterVersion
		}
		if obs.ClassifierArtifactID == "" {
			obs.ClassifierArtifactID = classifier.ArtifactID
		}
		if obs.ClassifierArtifactSHA256 == "" {
			obs.ClassifierArtifactSHA256 = classifier.ArtifactSHA256
		}
		if obs.ClassifierPredictedLabel == "" {
			obs.ClassifierPredictedLabel = classifier.PredictedLabel
		}
		if len(obs.ClassifierClassOrder) == 0 {
			obs.ClassifierClassOrder = append([]string(nil), classifier.ClassOrder...)
		}
		if len(obs.ClassifierProbabilities) == 0 && len(classifier.Probabilities) > 0 {
			if probabilities, err := json.Marshal(classifier.Probabilities); err == nil {
				obs.ClassifierProbabilities = probabilities
			} else {
				observability.FromContext(ctx).Debug("Failed to marshal fresh classifier probabilities for telemetry", "err", err)
			}
		}
		if obs.SelectionPolicyReleaseID == "" {
			obs.SelectionPolicyReleaseID = md.SelectionPolicyReleaseID
		}
		if obs.SelectionPolicySHA256 == "" {
			obs.SelectionPolicySHA256 = md.SelectionPolicySHA256
		}
		if obs.SelectionHeadGeneration == 0 {
			obs.SelectionHeadGeneration = md.SelectionHeadGeneration
		}
		if len(obs.SelectionTrace) == 0 && classifier.SelectionTrace != nil {
			if selectionTrace, err := json.Marshal(classifier.SelectionTrace); err == nil {
				obs.SelectionTrace = selectionTrace
			} else {
				observability.FromContext(ctx).Debug("Failed to marshal fresh selection trace for telemetry", "err", err)
			}
		}
		if obs.SidecarSchemaVersion == "" {
			obs.SidecarSchemaVersion = md.SidecarSchemaVersion
		}
		if debugEnabled && obs.DebugRef == "" {
			obs.DebugRef = md.DebugRef
		}
	}
	if t := timing.TimingFrom(ctx); t != nil {
		if ms := t.Ms(&t.UpstreamRequestNanos, &t.UpstreamFirstByteNanos); ms > 0 {
			obs.TTFTMs = &ms
		}
		if ms := t.Ms(&t.UpstreamRequestNanos, &t.UpstreamFirstOutputNanos); ms > 0 {
			obs.FirstOutputMs = &ms
		}
	}
	return obs
}

func (o observationContext) applySpanAttrs(b *telemetry.AttrBuilder) {
	if o.FreshClassifierMargin != nil {
		b.Float64("routing.fresh_classifier_margin", *o.FreshClassifierMargin)
	}
	if o.FreshPolicyGroup != "" {
		b.String("routing.fresh_policy_group", o.FreshPolicyGroup)
	}
	if o.ChosenScore != nil {
		b.Float64("routing.chosen_score", *o.ChosenScore)
	}
	if o.Propensity != nil {
		b.Float64("routing.propensity", *o.Propensity)
	}
	if len(o.CandidateScores) > 0 {
		b.String("routing.candidate_scores", string(o.CandidateScores))
	}
	if o.Strategy != "" {
		b.String("routing.strategy", o.Strategy)
	}
	if o.RouteID != "" {
		b.String("routing.route_id", o.RouteID)
	}
	if o.PolicyRouteKey != "" {
		b.String("routing.policy_route_key", o.PolicyRouteKey)
	}
	if o.PolicyArtifactID != "" {
		b.String("routing.policy_artifact_id", o.PolicyArtifactID)
	}
	if o.PolicyArtifactSHA256 != "" {
		b.String("routing.policy_artifact_sha256", o.PolicyArtifactSHA256)
	}
	if o.RosterVersion != "" {
		b.String("routing.roster_version", o.RosterVersion)
	}
	if o.ClassifierArtifactID != "" {
		b.String("routing.classifier_artifact_id", o.ClassifierArtifactID)
	}
	if o.ClassifierArtifactSHA256 != "" {
		b.String("routing.classifier_artifact_sha256", o.ClassifierArtifactSHA256)
	}
	if o.ClassifierPredictedLabel != "" {
		b.String("routing.classifier_predicted_label", o.ClassifierPredictedLabel)
	}
	if len(o.ClassifierProbabilities) > 0 {
		b.String("routing.classifier_probabilities", string(o.ClassifierProbabilities))
	}
	if len(o.ClassifierClassOrder) > 0 {
		if classOrder, err := json.Marshal(o.ClassifierClassOrder); err == nil {
			b.String("routing.classifier_class_order", string(classOrder))
		}
	}
	if o.SelectionPolicyReleaseID != "" {
		b.String("routing.selection_policy_release_id", o.SelectionPolicyReleaseID)
	}
	if o.SelectionPolicySHA256 != "" {
		b.String("routing.selection_policy_sha256", o.SelectionPolicySHA256)
	}
	if o.SelectionHeadGeneration > 0 {
		b.Int64("routing.selection_head_generation", o.SelectionHeadGeneration)
	}
	if len(o.SelectionTrace) > 0 {
		b.String("routing.selection_trace", string(o.SelectionTrace))
	}
	if o.SidecarSchemaVersion != "" {
		b.String("routing.sidecar_schema_version", o.SidecarSchemaVersion)
	}
	b.Bool("routing.training_allowed", o.TrainingAllowed)
	b.String("routing.capture_mode", o.CaptureMode)
	if o.RolloutID != "" {
		b.String("routing.rollout_id", o.RolloutID)
	}
	if o.DebugRef != "" {
		b.String("routing.debug_ref", o.DebugRef)
	}
	if o.TTFTMs != nil {
		b.Int64("latency.ttft_ms", *o.TTFTMs)
	}
	if o.FirstOutputMs != nil {
		b.Int64("latency.first_output_ms", *o.FirstOutputMs)
	}
}
