package policyshadow

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
)

const decisionTimeout = 3 * time.Second

type Recorder struct {
	Store telemetry.PolicyShadowStore
	Now   func() time.Time
	Route func(context.Context, router.Strategy, router.Request) (router.Decision, error)
}

func (r Recorder) Fire(ctx context.Context, installationID string, servingStrategy, shadowStrategy router.Strategy, serving router.Decision, req router.Request) {
	if shadowStrategy == "" || shadowStrategy == servingStrategy {
		return
	}

	shadowReq := req
	shadowReq.ShadowMode = true
	shadowReq.TrainingAllowed = false
	shadowReq.DebugEnabled = false
	base := telemetry.PolicyShadowDecision{
		InstallationID:  installationID,
		OrganizationID:  req.OrganizationID,
		RolloutID:       req.RolloutID,
		ClientApp:       req.ClientApp,
		TrainingAllowed: req.TrainingAllowed,
		ServingStrategy: string(servingStrategy),
		ServingModel:    serving.Model,
		ServingProvider: serving.Provider,
		ShadowStrategy:  string(shadowStrategy),
		RequestedModel:  req.RequestedModel,
	}
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed {
		base.ServingTarget, base.ServingReleaseID = identity.Target, identity.ReleaseID
	}
	applyServingMetadata(&base, serving.Metadata)

	log := observability.FromContext(ctx).With(
		"installation_id", base.InstallationID,
		"serving_strategy", servingStrategy,
		"shadow_strategy", shadowStrategy,
	)
	observability.SafeGoContext(requestcontext.WithoutValues(ctx), log, decisionTimeout, "policyShadowDecision", func(shadowCtx context.Context) {
		started := r.Now()
		shadow, err := r.Route(shadowCtx, shadowStrategy, shadowReq)
		event := base
		event.ShadowLatencyMs = r.Now().Sub(started).Milliseconds()
		if err != nil {
			event.ShadowError = truncateError(err.Error())
		} else {
			event.ShadowModel = shadow.Model
			event.ShadowProvider = shadow.Provider
			event.ModelsAgree = shadow.Model == serving.Model
			applyShadowMetadata(&event, shadow.Metadata)
		}

		log.Info("router.policy_shadow",
			"serving_model", event.ServingModel,
			"shadow_model", event.ShadowModel,
			"models_agree", event.ModelsAgree,
			"shadow_latency_ms", event.ShadowLatencyMs,
			"shadow_error", event.ShadowError,
		)
		if r.Store != nil && event.InstallationID != "" {
			if insertErr := r.Store.InsertPolicyShadowDecision(shadowCtx, event); insertErr != nil {
				log.Warn("Policy shadow decision insert failed", "err", insertErr)
			}
		}
	})
}

func applyServingMetadata(event *telemetry.PolicyShadowDecision, metadata *router.RoutingMetadata) {
	if metadata == nil {
		return
	}
	event.ServingRouteID = metadata.RouteID
	event.ServingPolicyRouteKey = metadata.PolicyRouteKey
	event.ServingPolicyArtifactID = metadata.PolicyArtifactID
	event.ServingPolicyArtifactSHA256 = metadata.PolicyArtifactSHA256
}

func applyShadowMetadata(event *telemetry.PolicyShadowDecision, metadata *router.RoutingMetadata) {
	if metadata == nil {
		return
	}
	event.ShadowRouteID = metadata.RouteID
	event.ShadowPolicyRouteKey = metadata.PolicyRouteKey
	event.ShadowPolicyArtifactID = metadata.PolicyArtifactID
	event.ShadowPolicyArtifactSHA256 = metadata.PolicyArtifactSHA256
}

func truncateError(value string) string {
	const maxBytes = 1000
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}
