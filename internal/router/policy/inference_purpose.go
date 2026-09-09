package policy

import "weave-os/router/internal/inference"

type Purpose = inference.Purpose

const (
	PurposeAnthropicMessages         = inference.PurposeAnthropicMessages
	PurposeOpenAIChatCompletions     = inference.PurposeOpenAIChatCompletions
	PurposeOpenAIResponses           = inference.PurposeOpenAIResponses
	PurposeGeminiGenerateContent     = inference.PurposeGeminiGenerateContent
	PurposeHandoverSummary           = inference.PurposeHandoverSummary
	PurposePrecompactionSummary      = inference.PurposePrecompactionSummary
	PurposeCompactionHandoverSummary = inference.PurposeCompactionHandoverSummary
	PurposeTitleGeneration           = inference.PurposeTitleGeneration
	PurposeClassifier                = inference.PurposeClassifier
	PurposeProbe                     = inference.PurposeProbe
	PurposeSubAgentDispatch          = inference.PurposeSubAgentDispatch
	PurposeClientCompaction          = inference.PurposeClientCompaction
	PurposeAgentShadowEvaluation     = inference.PurposeAgentShadowEvaluation
	PurposeCountTokens               = inference.PurposeCountTokens
	PurposeUpstreamModelListing      = inference.PurposeUpstreamModelListing
	PurposePolicySidecarDecision     = inference.PurposePolicySidecarDecision
	PurposePolicySidecarPreview      = inference.PurposePolicySidecarPreview
	PurposePolicySidecarOutcome      = inference.PurposePolicySidecarOutcome
	PurposePolicySidecarFeedback     = inference.PurposePolicySidecarFeedback
	PurposeClusterEmbedding          = inference.PurposeClusterEmbedding
	PurposeSemanticCacheEmbedding    = inference.PurposeSemanticCacheEmbedding
	PurposeNativeWebSearch           = inference.PurposeNativeWebSearch
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

type DispatchClass = inference.DispatchClass

const (
	DispatchClassMainInference       = inference.DispatchClassMainInference
	DispatchClassAuxiliaryInference  = inference.DispatchClassAuxiliaryInference
	DispatchClassClientAuthoritative = inference.DispatchClassClientAuthoritative
	DispatchClassMetadataPassthrough = inference.DispatchClassMetadataPassthrough
	DispatchClassControlPlane        = inference.DispatchClassControlPlane
	DispatchClassLocalSupport        = inference.DispatchClassLocalSupport
	DispatchClassWebSearchTool       = inference.DispatchClassWebSearchTool
)

type SelectionStrategy = inference.SelectionStrategy

const (
	SelectionStrategyRouter              = inference.SelectionStrategyRouter
	SelectionStrategyFixedCatalog        = inference.SelectionStrategyFixedCatalog
	SelectionStrategyDeploymentHardPin   = inference.SelectionStrategyDeploymentHardPin
	SelectionStrategyClientAuthoritative = inference.SelectionStrategyClientAuthoritative
	SelectionStrategyPassthrough         = inference.SelectionStrategyPassthrough
	SelectionStrategyNone                = inference.SelectionStrategyNone
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

type OverrideSource = inference.OverrideSource

const (
	OverrideSourceRequest             = inference.OverrideSourceRequest
	OverrideSourceSession             = inference.OverrideSourceSession
	OverrideSourceInstallation        = inference.OverrideSourceInstallation
	OverrideSourceDeployment          = inference.OverrideSourceDeployment
	OverrideSourcePolicyDefault       = inference.OverrideSourcePolicyDefault
	OverrideSourceClientAuthoritative = inference.OverrideSourceClientAuthoritative
)

// MigrationStatus records which execution boundary currently owns a purpose.
type MigrationStatus string

const (
	MigrationStatusLegacyDirect  MigrationStatus = "legacy_direct"
	MigrationStatusCompatAdapter MigrationStatus = "compat_adapter"
	MigrationStatusExecutor      MigrationStatus = "executor"
	MigrationStatusNonInference  MigrationStatus = "non_inference"
)

type Constraint = inference.Constraint

const (
	ConstraintCatalogBinding     = inference.ConstraintCatalogBinding
	ConstraintContextWindow      = inference.ConstraintContextWindow
	ConstraintModelExclusions    = inference.ConstraintModelExclusions
	ConstraintProviderExclusions = inference.ConstraintProviderExclusions
	ConstraintSpend              = inference.ConstraintSpend
)

type SoftPreference = inference.SoftPreference

const (
	SoftPreferenceCapability           = inference.SoftPreferenceCapability
	SoftPreferenceQualityPrice         = inference.SoftPreferenceQualityPrice
	SoftPreferencePreferredModels      = inference.SoftPreferencePreferredModels
	SoftPreferenceCacheAffinity        = inference.SoftPreferenceCacheAffinity
	SoftPreferenceSubscriptionCapacity = inference.SoftPreferenceSubscriptionCapacity
)

type BudgetSource = inference.BudgetSource

const (
	BudgetSourceRequest    = inference.BudgetSourceRequest
	BudgetSourcePolicy     = inference.BudgetSourcePolicy
	BudgetSourceDeployment = inference.BudgetSourceDeployment
	BudgetSourceLocal      = inference.BudgetSourceLocal
)

type PolicyID = inference.PolicyID

type PolicyRevision = inference.PolicyRevision

type BudgetSpec = inference.BudgetSpec

type TargetOverride = inference.TargetOverride

type BudgetOverride = inference.BudgetOverride

type PlanProvenance = inference.PlanProvenance
