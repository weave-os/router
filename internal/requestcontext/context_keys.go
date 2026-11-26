package requestcontext

import "context"

type (
	APIKeyIDContextKey                                   struct{}
	APIKeyPaidFallbackOnSubscriptionRefusalContextKey    struct{}
	AgentShadowEvalContextKey                            struct{}
	ClusterModelListsContextKey                          struct{}
	ExternalIDContextKey                                 struct{}
	InstallationAllowedModelsContextKey                  struct{}
	InstallationCaptureModeContextKey                    struct{}
	InstallationExcludedModelsContextKey                 struct{}
	InstallationExcludedProvidersContextKey              struct{}
	InstallationFastModeModelsContextKey                 struct{}
	InstallationHideTerminalSurfacesContextKey           struct{}
	InstallationIDContextKey                             struct{}
	InstallationPickedModelCreditFallbackContextKey      struct{}
	InstallationPreferredModelsContextKey                struct{}
	InstallationServingReleaseHeadersContextKey          struct{}
	InstallationRoutingKnobsContextKey                   struct{}
	InstallationShowModelSelectionReasoningContextKey    struct{}
	InstallationSubscriptionModelsWhenActiveContextKey   struct{}
	InstallationSubscriptionModelsWhenInactiveContextKey struct{}
	InstallationSubscriptionRoutingDisabledContextKey    struct{}
	InstallationSubscriptionStateModelsEnabledContextKey struct{}
	OpenAIAccountIDContextKey                            struct{}
	InstallationTrialCaptureContextKey                   struct{}
	InstallationUsageBypassContextKey                    struct{}
	PolicyDebugEnabledContextKey                         struct{}
	PolicyRolloutIDContextKey                            struct{}
	PolicyRoutingIntentContextKey                        struct{}
	PolicyShadowStrategyContextKey                       struct{}
	PolicyTrainingAllowedContextKey                      struct{}
	RequestAllowedModelsContextKey                       struct{}
)

type AgentShadowEvaluation struct {
	Model     string
	RolloutID string
	StateID   string
}

func AgentShadowEvalFromContext(ctx context.Context) (AgentShadowEvaluation, bool) {
	value, ok := ctx.Value(AgentShadowEvalContextKey{}).(AgentShadowEvaluation)
	return value, ok && value.Model != "" && value.RolloutID != "" && value.StateID != ""
}
