package policy

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

var knownPurposes = []Purpose{
	PurposeAnthropicMessages,
	PurposeOpenAIChatCompletions,
	PurposeOpenAIResponses,
	PurposeGeminiGenerateContent,
	PurposeHandoverSummary,
	PurposePrecompactionSummary,
	PurposeCompactionHandoverSummary,
	PurposeTitleGeneration,
	PurposeClassifier,
	PurposeProbe,
	PurposeSubAgentDispatch,
	PurposeClientCompaction,
	PurposeAgentShadowEvaluation,
	PurposeCountTokens,
	PurposeUpstreamModelListing,
	PurposePolicySidecarDecision,
	PurposePolicySidecarPreview,
	PurposePolicySidecarOutcome,
	PurposePolicySidecarFeedback,
	PurposeClusterEmbedding,
	PurposeSemanticCacheEmbedding,
	PurposeNativeWebSearch,
}

// KnownPurposes returns every purpose the registry must cover.
func KnownPurposes() []Purpose {
	return append([]Purpose(nil), knownPurposes...)
}

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

// CandidateSource identifies where the selectable target set originates.
type CandidateSource string

const (
	CandidateSourceRoutableCatalog CandidateSource = "routable_catalog"
	CandidateSourceFixedCatalog    CandidateSource = "fixed_catalog"
	CandidateSourceDeployment      CandidateSource = "deployment"
	CandidateSourceRequest         CandidateSource = "request"
	CandidateSourceLocal           CandidateSource = "local"
	CandidateSourceNone            CandidateSource = "none"
)

// FallbackKind identifies the only fallback family a policy permits.
type FallbackKind string

const (
	FallbackKindPlanAlternatives FallbackKind = "plan_alternatives"
	FallbackKindBinding          FallbackKind = "binding"
	FallbackKindLocalRecovery    FallbackKind = "local_recovery"
	FallbackKindFullHistory      FallbackKind = "full_history"
	FallbackKindRoutedDispatch   FallbackKind = "routed_dispatch"
	FallbackKindNone             FallbackKind = "none"
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

// MigrationStatus records which execution boundary currently owns a purpose.
type MigrationStatus string

const (
	MigrationStatusLegacyDirect  MigrationStatus = "legacy_direct"
	MigrationStatusCompatAdapter MigrationStatus = "compat_adapter"
	MigrationStatusExecutor      MigrationStatus = "executor"
	MigrationStatusNonInference  MigrationStatus = "non_inference"
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

// BudgetSource identifies which layer supplies an operation's final budget.
type BudgetSource string

const (
	BudgetSourceRequest    BudgetSource = "request"
	BudgetSourcePolicy     BudgetSource = "policy"
	BudgetSourceDeployment BudgetSource = "deployment"
	BudgetSourceLocal      BudgetSource = "local"
)
