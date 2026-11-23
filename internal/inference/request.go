package inference

import "weave-os/router/internal/router"

type Purpose string

const (
	PurposeAnthropicMessages        Purpose = "anthropic_messages"
	PurposeOpenAIChatCompletions    Purpose = "openai_chat_completions"
	PurposeOpenAIResponses          Purpose = "openai_responses"
	PurposeGeminiGenerateContent    Purpose = "gemini_generate_content"
	PurposeEscalationJudge          Purpose = "escalation_judge"
	PurposeHandoverSummary          Purpose = "handover_summary"
	PurposePrecompactionSummary     Purpose = "precompaction_summary"
	PurposeTitleGeneration          Purpose = "title_generation"
	PurposeClassifier               Purpose = "classifier"
	PurposeProbe                    Purpose = "probe"
	PurposeSubAgentDispatch         Purpose = "sub_agent_dispatch"
	PurposeClientCompaction         Purpose = "client_compaction"
	PurposeAgentShadowEvaluation    Purpose = "agent_shadow_evaluation"
	PurposeCountTokens              Purpose = "count_tokens"
	PurposeUpstreamModelListing     Purpose = "upstream_model_listing"
	PurposePolicySidecarDecision    Purpose = "policy_sidecar_decision"
	PurposePolicySidecarPreview     Purpose = "policy_sidecar_preview"
	PurposePolicySidecarOutcome     Purpose = "policy_sidecar_outcome"
	PurposePolicySidecarFeedback    Purpose = "policy_sidecar_feedback"
	PurposeTaskDomainClassification Purpose = "task_domain_classification"
	PurposeClusterEmbedding         Purpose = "cluster_embedding"
	PurposeSemanticCacheEmbedding   Purpose = "semantic_cache_embedding"
	PurposeNativeWebSearch          Purpose = "native_web_search"
)

type DispatchClass string

const (
	DispatchClassMainInference       DispatchClass = "main_inference"
	DispatchClassAuxiliaryInference  DispatchClass = "auxiliary_inference"
	DispatchClassClientAuthoritative DispatchClass = "client_authoritative"
	DispatchClassMetadataPassthrough DispatchClass = "metadata_passthrough"
	DispatchClassControlPlane        DispatchClass = "control_plane"
	DispatchClassLocalSupport        DispatchClass = "local_support"
	DispatchClassWebSearchTool       DispatchClass = "web_search_tool"
)

type SelectionStrategy string

const (
	SelectionStrategyRouter              SelectionStrategy = "router"
	SelectionStrategyFixedCatalog        SelectionStrategy = "fixed_catalog"
	SelectionStrategyDeploymentHardPin   SelectionStrategy = "deployment_hard_pin"
	SelectionStrategyClientAuthoritative SelectionStrategy = "client_authoritative"
	SelectionStrategyPassthrough         SelectionStrategy = "passthrough"
	SelectionStrategyNone                SelectionStrategy = "none"
)

type OverrideSource string

const (
	OverrideSourceRequest             OverrideSource = "request"
	OverrideSourceSession             OverrideSource = "session"
	OverrideSourceInstallation        OverrideSource = "installation"
	OverrideSourceDeployment          OverrideSource = "deployment"
	OverrideSourcePolicyDefault       OverrideSource = "policy_default"
	OverrideSourceClientAuthoritative OverrideSource = "client_authoritative"
)

type Constraint string

const (
	ConstraintCatalogBinding     Constraint = "catalog_binding"
	ConstraintContextWindow      Constraint = "context_window"
	ConstraintModelExclusions    Constraint = "model_exclusions"
	ConstraintProviderExclusions Constraint = "provider_exclusions"
	ConstraintSpend              Constraint = "spend"
)

type SoftPreference string

const (
	SoftPreferenceCapability           SoftPreference = "capability"
	SoftPreferenceQualityPrice         SoftPreference = "quality_price"
	SoftPreferencePreferredModels      SoftPreference = "preferred_models"
	SoftPreferenceCacheAffinity        SoftPreference = "cache_affinity"
	SoftPreferenceSubscriptionCapacity SoftPreference = "subscription_capacity"
)

type BudgetSource string

const (
	BudgetSourceRequest    BudgetSource = "request"
	BudgetSourcePolicy     BudgetSource = "policy"
	BudgetSourceDeployment BudgetSource = "deployment"
	BudgetSourceLocal      BudgetSource = "local"
)

type PolicyID string

type PolicyRevision string

type BudgetSpec struct {
	Source          BudgetSource `json:"source"`
	MaxAttempts     int          `json:"max_attempts,omitempty"`
	TimeoutMillis   int64        `json:"timeout_millis,omitempty"`
	MaxOutputTokens int          `json:"max_output_tokens,omitempty"`
	MaxSpendUSD     float64      `json:"max_spend_usd,omitempty"`
}

type Target struct {
	ArmID                        string
	CatalogID                    string
	Provider                     string
	UpstreamID                   string
	BindingIndex                 int
	Endpoint                     string
	ModelRevision                string
	ReasoningConfigurationSHA256 string
	ToolConfigurationSHA256      string
	Effort                       string
}

type TargetOverride struct {
	Source    OverrideSource
	CatalogID string
	Provider  string
	Effort    string
}

type BudgetOverride struct {
	Source          BudgetSource `json:"source"`
	MaxAttempts     int          `json:"max_attempts,omitempty"`
	TimeoutMillis   int64        `json:"timeout_millis,omitempty"`
	MaxOutputTokens int          `json:"max_output_tokens,omitempty"`
	MaxSpendUSD     float64      `json:"max_spend_usd,omitempty"`
}

type PlanProvenance struct {
	SelectionStrategy SelectionStrategy
	OverrideSource    OverrideSource
	ArmID             string
	RosterID          string
}

type InvocationRequest struct {
	Purpose       Purpose
	RequestID     string
	Body          []byte
	RouterRequest router.Request
	Overrides     []TargetOverride
	Budget        *BudgetOverride
}
