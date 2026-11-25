package policyshadow

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
)

type RoutingModelProfiles interface {
	ProfileKey(ctx context.Context, installationID string, model router.RoutingModel) (profileKey string, assigned bool, err error)
}

type RecipeRecorder struct {
	Store       telemetry.PolicyShadowStore
	Profiles    RoutingModelProfiles
	Replayer    router.RecipeShadowReplayer
	ShadowModel router.RoutingModel
	Now         func() time.Time
}

type RecipeTurn struct {
	InstallationID    string
	ServingStrategy   router.Strategy
	ServingProfileKey string
	Served            router.Decision
	Request           router.Request
}

func (r RecipeRecorder) Fire(ctx context.Context, turn RecipeTurn) {
	if r.Store == nil || r.Profiles == nil || r.Replayer == nil || r.ShadowModel == "" || turn.InstallationID == "" {
		return
	}
	servedFacts, scored := r.Replayer.ServedFacts(turn.Served)
	if !scored {
		return
	}
	log := observability.FromContext(ctx).With("installation_id", turn.InstallationID, "shadow_routing_model", r.ShadowModel)
	observability.SafeGoContext(requestcontext.WithoutValues(ctx), log, decisionTimeout, "recipeShadowDecision", func(shadowCtx context.Context) {
		profileKey, assigned, err := r.Profiles.ProfileKey(shadowCtx, turn.InstallationID, r.ShadowModel)
		if err != nil {
			log.Warn("Recipe shadow profile lookup failed", "err", err)
			return
		}
		if !assigned || profileKey == turn.ServingProfileKey {
			return
		}
		started := r.Now()
		pick, replayErr := r.Replayer.Replay(shadowCtx, profileKey, turn.Served, turn.Request)
		event := recipeShadowEvent(turn, servedFacts, r.ShadowModel, profileKey)
		event.ShadowLatencyMs = r.Now().Sub(started).Milliseconds()
		if replayErr != nil {
			event.ShadowError = truncateError(replayErr.Error())
		} else {
			applyRecipePick(&event, pick)
		}
		if insertErr := r.Store.InsertPolicyShadowDecision(shadowCtx, event); insertErr != nil {
			log.Warn("Recipe shadow decision insert failed", "err", insertErr)
		}
	})
}

func recipeShadowEvent(turn RecipeTurn, served router.RecipeShadowServed, shadowModel router.RoutingModel, shadowProfileKey string) telemetry.PolicyShadowDecision {
	event := telemetry.PolicyShadowDecision{
		InstallationID:           turn.InstallationID,
		OrganizationID:           turn.Request.OrganizationID,
		RolloutID:                turn.Request.RolloutID,
		ClientApp:                turn.Request.ClientApp,
		TrainingAllowed:          turn.Request.TrainingAllowed,
		ServingStrategy:          string(turn.ServingStrategy),
		ServingModel:             turn.Served.Model,
		ServingProvider:          turn.Served.Provider,
		ShadowStrategy:           string(turn.ServingStrategy),
		ShadowRoutingModel:       string(shadowModel),
		ServingProfileKey:        turn.ServingProfileKey,
		ShadowProfileKey:         shadowProfileKey,
		ClassifierPredictedLabel: served.PredictedLabel,
		ServingPolicyGroup:       served.PolicyGroup,
		ServingRosterArmID:       served.RosterArmID,
	}
	applyServingMetadata(&event, turn.Served.Metadata)
	return event
}

func applyRecipePick(event *telemetry.PolicyShadowDecision, pick router.RecipeShadowPick) {
	event.ShadowModel = pick.Model
	event.ShadowProvider = pick.Provider
	event.ShadowPolicyArtifactSHA256 = pick.PolicySHA256
	event.ShadowPolicyGroup = pick.PolicyGroup
	event.ShadowRosterArmID = pick.RosterArmID
	event.ModelsAgree = pick.Model == event.ServingModel
}
