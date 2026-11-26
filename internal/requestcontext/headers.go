package requestcontext

const (
	HeaderRouterDecision            = "x-router-decision"
	HeaderRouterProvider            = "x-router-provider"
	HeaderRouterModel               = "x-router-model"
	HeaderRouterContextWindow       = "x-router-context-window"
	HeaderRouterContextEstimate     = "x-router-context-estimate-tokens"
	HeaderRouterContextReserve      = "x-router-context-output-reserve-tokens"
	HeaderRouterContextEstimateKind = "x-router-context-estimate-kind"
	HeaderRouterContextVersion      = "x-router-context-version"
	HeaderRouterFallbackFrom        = "x-router-fallback-from"
	HeaderRouterFallbackAttempt     = "x-router-fallback-attempt"
	HeaderRouterCostUSD             = "x-router-cost-usd"
	HeaderRouterCostInputUSD        = "x-router-cost-input-usd"
	HeaderRouterCostOutputUSD       = "x-router-cost-output-usd"
	HeaderRouterCacheReadTokens     = "x-router-cache-read-tokens"
	HeaderRouterCacheCreationTokens = "x-router-cache-creation-tokens"
)

const (
	HeaderServingReleaseSHA = "X-Weave-Release-SHA"
	HeaderServingPolicySHA  = "X-Weave-Policy-SHA"
)

const (
	ForceModelHeader                 = "x-weave-force-model"
	AllowedModelsHeader              = "x-weave-allowed-models"
	ClassifierThreadHeader           = "X-Weave-Classifier-Thread"
	ClassifierThreadUnavailableToken = "weave-classifier-unavailable"
	AgentShadowModelHeader           = "x-weave-agent-shadow-model"
	AgentShadowRolloutHeader         = "x-weave-agent-shadow-rollout-id"
	AgentShadowStateHeader           = "x-weave-agent-shadow-state-id"
	CodexNativeModelPinHeader        = "X-Weave-Codex-Native-Model-Pin"
	RolloutIDHeader                  = "X-Weave-Rollout-Id"
)
