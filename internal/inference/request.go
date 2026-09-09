// Package inference defines the contracts between feature orchestration,
// policy resolution, and upstream execution.
package inference

import "weave-os/router/internal/router"

// Purpose identifies why an operation may perform inference or related I/O.
// It is semantic and stable across ingress protocols and provider bindings.
type Purpose string

const (
	PurposeAnthropicMessages         Purpose = "anthropic_messages"
	PurposeOpenAIChatCompletions     Purpose = "openai_chat_completions"
	PurposeOpenAIResponses           Purpose = "openai_responses"
	PurposeGeminiGenerateContent     Purpose = "gemini_generate_content"
	PurposeHandoverSummary           Purpose = "handover_summary"
	PurposePrecompactionSummary      Purpose = "precompaction_summary"
	PurposeCompactionHandoverSummary Purpose = "compaction_handover_summary"
	PurposeTitleGeneration           Purpose = "title_generation"
	PurposeClassifier                Purpose = "classifier"
	PurposeProbe                     Purpose = "probe"
	PurposeSubAgentDispatch          Purpose = "sub_agent_dispatch"
	PurposeClientCompaction          Purpose = "client_compaction"
	PurposeAgentShadowEvaluation     Purpose = "agent_shadow_evaluation"
	PurposeCountTokens               Purpose = "count_tokens"
	PurposeUpstreamModelListing      Purpose = "upstream_model_listing"
	PurposePolicySidecarDecision     Purpose = "policy_sidecar_decision"
	PurposePolicySidecarPreview      Purpose = "policy_sidecar_preview"
	PurposePolicySidecarOutcome      Purpose = "policy_sidecar_outcome"
	PurposePolicySidecarFeedback     Purpose = "policy_sidecar_feedback"
	PurposeClusterEmbedding          Purpose = "cluster_embedding"
	PurposeSemanticCacheEmbedding    Purpose = "semantic_cache_embedding"
	PurposeNativeWebSearch           Purpose = "native_web_search"
)

// DispatchClass separates provider inference from passthrough, control-plane,
// local-support, and tool execution.
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

// SelectionStrategy identifies which authority chooses an operation target.
type SelectionStrategy string

const (
	SelectionStrategyRouter              SelectionStrategy = "router"
	SelectionStrategyFixedCatalog        SelectionStrategy = "fixed_catalog"
	SelectionStrategyDeploymentHardPin   SelectionStrategy = "deployment_hard_pin"
	SelectionStrategyClientAuthoritative SelectionStrategy = "client_authoritative"
	SelectionStrategyPassthrough         SelectionStrategy = "passthrough"
	SelectionStrategyNone                SelectionStrategy = "none"
)

// OverrideSource identifies an authority that may influence resolution.
type OverrideSource string

const (
	OverrideSourceRequest             OverrideSource = "request"
	OverrideSourceSession             OverrideSource = "session"
	OverrideSourceInstallation        OverrideSource = "installation"
	OverrideSourceDeployment          OverrideSource = "deployment"
	OverrideSourcePolicyDefault       OverrideSource = "policy_default"
	OverrideSourceClientAuthoritative OverrideSource = "client_authoritative"
)

// Constraint identifies a hard condition that resolution and fallback must preserve.
type Constraint string

const (
	ConstraintCatalogBinding     Constraint = "catalog_binding"
	ConstraintCapability         Constraint = "capability"
	ConstraintContextWindow      Constraint = "context_window"
	ConstraintCredentialScope    Constraint = "credential_scope"
	ConstraintModelExclusions    Constraint = "model_exclusions"
	ConstraintProviderExclusions Constraint = "provider_exclusions"
	ConstraintRequestFormat      Constraint = "request_format"
	ConstraintSpend              Constraint = "spend"
	ConstraintTenant             Constraint = "tenant"
)

// SoftPreference identifies a ranking signal that may influence selection but
// can never override a hard constraint.
type SoftPreference string

const (
	SoftPreferenceQualityPrice         SoftPreference = "quality_price"
	SoftPreferencePreferredModels      SoftPreference = "preferred_models"
	SoftPreferenceCacheAffinity        SoftPreference = "cache_affinity"
	SoftPreferenceSubscriptionCapacity SoftPreference = "subscription_capacity"
)

// BudgetSource identifies which layer supplies an operation's final budget.
type BudgetSource string

const (
	BudgetSourceRequest    BudgetSource = "request"
	BudgetSourcePolicy     BudgetSource = "policy"
	BudgetSourceDeployment BudgetSource = "deployment"
	BudgetSourceLocal      BudgetSource = "local"
)

// PolicyID is the stable review identity of an inference policy.
type PolicyID string

// PolicyRevision identifies a reviewed version of one policy entry.
type PolicyRevision string

// BudgetSpec is the bounded execution envelope declared by a policy. Zero
// numeric limits mean the named source supplies the request-time value.
type BudgetSpec struct {
	Source          BudgetSource `json:"source"`
	MaxAttempts     int          `json:"max_attempts,omitempty"`
	TimeoutMillis   int64        `json:"timeout_millis,omitempty"`
	MaxOutputTokens int          `json:"max_output_tokens,omitempty"`
	MaxSpendUSD     float64      `json:"max_spend_usd,omitempty"`
}

// Target is one immutable catalog/provider binding authorized for execution.
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

// TargetOverride is a typed request for one catalog-backed target.
type TargetOverride struct {
	Source    OverrideSource
	CatalogID string
	Provider  string
	Effort    string
}

// BudgetOverride supplies request- or deployment-time limits for policy
// entries whose BudgetSpec declares the same source.
type BudgetOverride struct {
	Source          BudgetSource `json:"source"`
	MaxAttempts     int          `json:"max_attempts,omitempty"`
	TimeoutMillis   int64        `json:"timeout_millis,omitempty"`
	MaxOutputTokens int          `json:"max_output_tokens,omitempty"`
	MaxSpendUSD     float64      `json:"max_spend_usd,omitempty"`
}

// PlanProvenance records which policy mechanism authorized a target.
type PlanProvenance struct {
	SelectionStrategy SelectionStrategy
	OverrideSource    OverrideSource
	ArmID             string
	RosterID          string
}

// InvocationRequest is the complete input to one logical inference operation.
// Policy resolution consumes RouterRequest, Overrides, and Budget before an
// executor receives the immutable plan. Body remains in the client wire format
// so target-aware translation stays inside the execution boundary. Transport
// concerns (client headers, streaming sinks) belong to the executor adapter,
// not this contract.
type InvocationRequest struct {
	Purpose       Purpose
	RequestID     string
	Body          []byte
	RouterRequest router.Request
	Overrides     []TargetOverride
	Budget        *BudgetOverride
}
