package requestcontext

import "context"

type AgentShadowEvalContextKey struct{}

type AgentShadowEvaluation struct {
	Model     string
	RolloutID string
	StateID   string
}

func AgentShadowEvalFromContext(ctx context.Context) (AgentShadowEvaluation, bool) {
	value, ok := ctx.Value(AgentShadowEvalContextKey{}).(AgentShadowEvaluation)
	return value, ok && value.Model != "" && value.RolloutID != "" && value.StateID != ""
}

type RequestAllowedModelsContextKey struct{}

type ExternalAPIKeysContextKey struct{}

type ManagedSubscriptionEnrollmentUnavailableContextKey struct{}

type ManagedSubscriptionProvidersContextKey struct{}

type APIKeyIDContextKey struct{}

type AnthropicSubscriptionContextKey struct{}

type ClusterModelListsContextKey struct{}

type ExternalIDContextKey struct{}

type InstallationAllowedModelsContextKey struct{}

type InstallationExcludedModelsContextKey struct{}

type InstallationExcludedProvidersContextKey struct{}

type InstallationFastModeModelsContextKey struct{}

type InstallationHideTerminalSurfacesContextKey struct{}

type InstallationPickedModelCreditFallbackContextKey struct{}

type InstallationPreferredModelsContextKey struct{}

type InstallationRoutingKnobsContextKey struct{}

type InstallationShowModelSelectionReasoningContextKey struct{}

type InstallationSubscriptionModelsWhenActiveContextKey struct{}

type InstallationSubscriptionModelsWhenInactiveContextKey struct{}

type InstallationSubscriptionRoutingDisabledContextKey struct{}

type InstallationSubscriptionStateModelsEnabledContextKey struct{}

type InstallationTrialCaptureContextKey struct{}

type InstallationUsageBypassContextKey struct{}

type OpenAIAccountIDContextKey struct{}

type OpenAISubscriptionContextKey struct{}

type PolicyDebugEnabledContextKey struct{}

type PolicyRolloutIDContextKey struct{}

type PolicyRoutingIntentContextKey struct{}

type PolicyShadowStrategyContextKey struct{}

type PolicyTrainingAllowedContextKey struct{}

type APIKeyPaidFallbackOnSubscriptionRefusalContextKey struct{}

type InstallationIDContextKey struct{}

type InstallationCaptureModeContextKey struct{}
