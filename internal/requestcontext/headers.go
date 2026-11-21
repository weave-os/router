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

const ForceModelHeader = "x-weave-force-model"

const AllowedModelsHeader = "x-weave-allowed-models"

const ClassifierThreadHeader = "X-Weave-Classifier-Thread"

const ClassifierThreadUnavailableToken = "weave-classifier-unavailable"

const AgentShadowModelHeader = "x-weave-agent-shadow-model"

const AgentShadowRolloutHeader = "x-weave-agent-shadow-rollout-id"

const AgentShadowStateHeader = "x-weave-agent-shadow-state-id"

const CodexNativeModelPinHeader = "X-Weave-Codex-Native-Model-Pin"

const RolloutIDHeader = "X-Weave-Rollout-Id"
