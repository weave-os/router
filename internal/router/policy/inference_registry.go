package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"weave-os/router/internal/router/catalog"
)

const (
	// InferenceRegistrySchemaVersion is the stable static-projection schema.
	InferenceRegistrySchemaVersion = "inference_policy_registry_v2"
	inferencePolicyOwner           = "@steventohme"

	// HandoverSummaryDefaultModel is the reviewed default target of the
	// handover-summary policy. Haiku-class: summarization is cheap.
	HandoverSummaryDefaultModel = "claude-haiku-4-5"
	// PrecompactionDefaultModel is the reviewed Sonnet-class summarizer the
	// compaction cascade uses when the session has no warm Anthropic pin; the
	// summary is the only record of the elided history, so it is worth a
	// mid-tier model.
	PrecompactionDefaultModel = "claude-sonnet-4-6"
	// PrecompactionLargeWindowModel is the big-context Anthropic-family
	// summarizer for histories too large for PrecompactionDefaultModel.
	PrecompactionLargeWindowModel = "claude-fable-5"
)

// compactionSummarizerModels is the reviewed set both compaction purposes may
// summarize with, in default preference order. It includes every Anthropic
// non-low catalog model so a session's warm pin can summarize its own history
// and so ROUTER_COMPACTION_MODEL, which pins both purposes, validates once.
var compactionSummarizerModels = []string{
	PrecompactionDefaultModel,
	PrecompactionLargeWindowModel,
	"claude-sonnet-4-5",
	"claude-sonnet-5",
	"claude-opus-4-0",
	"claude-opus-4-1",
	"claude-opus-4-5",
	"claude-opus-4-6",
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-opus-5",
	"claude-fable-5-1",
}

// FallbackSpec declares the only fallback family and fixed alternatives a policy permits.
type FallbackSpec struct {
	Kind         FallbackKind `json:"kind"`
	Alternatives []string     `json:"alternatives,omitempty"`
}

// PolicySpec is the checked-in, reviewable policy for one purpose.
type PolicySpec struct {
	Purpose            Purpose           `json:"purpose"`
	DispatchClass      DispatchClass     `json:"dispatch_class"`
	PolicyID           PolicyID          `json:"policy_id"`
	PolicyRevision     PolicyRevision    `json:"policy_revision"`
	Owner              string            `json:"owner"`
	Rationale          string            `json:"rationale"`
	SelectionStrategy  SelectionStrategy `json:"selection_strategy"`
	CandidateSource    CandidateSource   `json:"candidate_source"`
	FixedCatalogModels []string          `json:"fixed_catalog_models,omitempty"`
	HardConstraints    []Constraint      `json:"hard_constraints"`
	SoftPreferences    []SoftPreference  `json:"soft_preferences,omitempty"`
	OverridePrecedence []OverrideSource  `json:"override_precedence"`
	Budget             BudgetSpec        `json:"budget"`
	Fallback           FallbackSpec      `json:"fallback"`
	MigrationStatus    MigrationStatus   `json:"migration_status"`
}

// Registry is an immutable, validated collection of purpose policies.
type Registry struct {
	specs     []PolicySpec
	byPurpose map[Purpose]PolicySpec
	revision  string
}

// NewRegistry validates specs, sorts them by purpose, and returns an immutable registry.
func NewRegistry(specs []PolicySpec) (Registry, error) {
	cloned := clonePolicySpecs(specs)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Purpose < cloned[j].Purpose })
	if err := validatePolicySpecs(cloned); err != nil {
		return Registry{}, err
	}
	byPurpose := make(map[Purpose]PolicySpec, len(cloned))
	for _, spec := range cloned {
		byPurpose[spec.Purpose] = clonePolicySpec(spec)
	}
	revision, err := registryRevision(cloned)
	if err != nil {
		return Registry{}, err
	}
	return Registry{specs: cloned, byPurpose: byPurpose, revision: revision}, nil
}

// DefaultRegistry returns the checked-in inference-purpose registry.
func DefaultRegistry() Registry {
	return defaultInferenceRegistry
}

// Revision returns the deterministic content identity of this registry.
func (r Registry) Revision() string {
	return r.revision
}

// Specs returns a deep copy of the registry entries in purpose order.
func (r Registry) Specs() []PolicySpec {
	return clonePolicySpecs(r.specs)
}

// FixedCatalogTargetSet returns every model a fixed-catalog policy may select
// that has a binding in availableProviders. Untiered catalog rows are included
// only when a reviewed policy names them, so the plan resolver's deployed set
// can honor policy membership without widening automatic routing.
func (r Registry) FixedCatalogTargetSet(availableProviders map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for _, spec := range r.specs {
		if spec.SelectionStrategy != SelectionStrategyFixedCatalog {
			continue
		}
		for _, catalogID := range spec.FixedCatalogModels {
			if len(catalog.EnumerateBindings(catalogID, availableProviders)) == 0 {
				continue
			}
			out[catalogID] = struct{}{}
		}
	}
	return out
}

// Spec returns a deep copy of the policy registered for purpose.
func (r Registry) Spec(purpose Purpose) (PolicySpec, bool) {
	spec, ok := r.byPurpose[purpose]
	return clonePolicySpec(spec), ok
}

func validatePolicySpecs(specs []PolicySpec) error {
	if len(specs) == 0 {
		return errors.New("inference policy registry is empty")
	}
	known := make(map[Purpose]struct{}, len(knownPurposes))
	for _, purpose := range knownPurposes {
		if _, duplicate := known[purpose]; duplicate {
			return fmt.Errorf("known inference purpose %q is duplicated", purpose)
		}
		known[purpose] = struct{}{}
	}
	seenPurposes := make(map[Purpose]struct{}, len(specs))
	seenPolicyIDs := make(map[PolicyID]struct{}, len(specs))
	for _, spec := range specs {
		if _, registered := known[spec.Purpose]; !registered {
			return fmt.Errorf("policy %q uses unknown purpose %q", spec.PolicyID, spec.Purpose)
		}
		if _, duplicate := seenPurposes[spec.Purpose]; duplicate {
			return fmt.Errorf("inference purpose %q has more than one policy", spec.Purpose)
		}
		seenPurposes[spec.Purpose] = struct{}{}
		if strings.TrimSpace(string(spec.PolicyID)) == "" || strings.TrimSpace(string(spec.PolicyRevision)) == "" || strings.TrimSpace(spec.Owner) == "" || strings.TrimSpace(spec.Rationale) == "" {
			return fmt.Errorf("purpose %q is missing policy identity, owner, rationale, or revision", spec.Purpose)
		}
		if _, duplicate := seenPolicyIDs[spec.PolicyID]; duplicate {
			return fmt.Errorf("policy ID %q is duplicated", spec.PolicyID)
		}
		seenPolicyIDs[spec.PolicyID] = struct{}{}
		if spec.DispatchClass == "" || spec.SelectionStrategy == "" || spec.CandidateSource == "" || spec.Budget.Source == "" || spec.Fallback.Kind == "" || spec.MigrationStatus == "" {
			return fmt.Errorf("policy %q is missing a required typed field", spec.PolicyID)
		}
		if !validDispatchClass(spec.DispatchClass) || !validSelectionStrategy(spec.SelectionStrategy) || !validCandidateSource(spec.CandidateSource) || !validBudgetSource(spec.Budget.Source) || !validFallbackKind(spec.Fallback.Kind) || !validMigrationStatus(spec.MigrationStatus) {
			return fmt.Errorf("policy %q contains an invalid typed value", spec.PolicyID)
		}
		if spec.SelectionStrategy == SelectionStrategyFixedCatalog && len(spec.FixedCatalogModels) == 0 {
			return fmt.Errorf("fixed policy %q has no catalog models", spec.PolicyID)
		}
		if spec.SelectionStrategy == SelectionStrategyFixedCatalog && spec.CandidateSource != CandidateSourceFixedCatalog {
			return fmt.Errorf("fixed policy %q must use the fixed catalog candidate source", spec.PolicyID)
		}
		if spec.SelectionStrategy != SelectionStrategyFixedCatalog && len(spec.FixedCatalogModels) > 0 {
			return fmt.Errorf("non-fixed policy %q declares fixed catalog models", spec.PolicyID)
		}
		if err := rejectDuplicates(spec.PolicyID, "fixed catalog model", spec.FixedCatalogModels); err != nil {
			return err
		}
		for _, model := range spec.FixedCatalogModels {
			if _, found := catalog.ByID(model); !found {
				return fmt.Errorf("policy %q names unknown catalog model %q", spec.PolicyID, model)
			}
		}
		if err := rejectDuplicates(spec.PolicyID, "constraint", spec.HardConstraints); err != nil {
			return err
		}
		for _, constraint := range spec.HardConstraints {
			if !validConstraint(constraint) {
				return fmt.Errorf("policy %q contains invalid constraint %q", spec.PolicyID, constraint)
			}
		}
		if err := rejectDuplicates(spec.PolicyID, "soft preference", spec.SoftPreferences); err != nil {
			return err
		}
		for _, preference := range spec.SoftPreferences {
			if !validSoftPreference(preference) {
				return fmt.Errorf("policy %q contains invalid soft preference %q", spec.PolicyID, preference)
			}
		}
		if err := rejectDuplicates(spec.PolicyID, "override source", spec.OverridePrecedence); err != nil {
			return err
		}
		for _, source := range spec.OverridePrecedence {
			if !validOverrideSource(source) {
				return fmt.Errorf("policy %q contains invalid override source %q", spec.PolicyID, source)
			}
		}
		if err := validateOverridePrecedence(spec); err != nil {
			return err
		}
		if err := validateSelectionContract(spec); err != nil {
			return err
		}
		if spec.Fallback.Kind == FallbackKindPlanAlternatives && len(spec.Fallback.Alternatives) == 0 {
			return fmt.Errorf("policy %q declares plan-alternative fallback without alternatives", spec.PolicyID)
		}
		if spec.Fallback.Kind != FallbackKindPlanAlternatives && len(spec.Fallback.Alternatives) > 0 {
			return fmt.Errorf("policy %q declares alternatives for fallback kind %q", spec.PolicyID, spec.Fallback.Kind)
		}
		if err := rejectDuplicates(spec.PolicyID, "fallback alternative", spec.Fallback.Alternatives); err != nil {
			return err
		}
		for _, model := range spec.Fallback.Alternatives {
			if _, found := catalog.ByID(model); !found {
				return fmt.Errorf("policy %q names unknown fallback model %q", spec.PolicyID, model)
			}
			if slices.Contains(spec.FixedCatalogModels, model) {
				return fmt.Errorf("policy %q repeats fixed catalog model %q as a fallback alternative", spec.PolicyID, model)
			}
		}
		if spec.Budget.MaxAttempts < 0 || spec.Budget.TimeoutMillis < 0 || spec.Budget.MaxOutputTokens < 0 || spec.Budget.MaxSpendUSD < 0 {
			return fmt.Errorf("policy %q has a negative budget", spec.PolicyID)
		}
	}
	for purpose := range known {
		if _, found := seenPurposes[purpose]; !found {
			return fmt.Errorf("inference purpose %q has no policy", purpose)
		}
	}
	return nil
}

func rejectDuplicates[T comparable](policyID PolicyID, field string, values []T) error {
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("policy %q repeats %s %v", policyID, field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validDispatchClass(value DispatchClass) bool {
	switch value {
	case DispatchClassMainInference, DispatchClassAuxiliaryInference, DispatchClassClientAuthoritative, DispatchClassMetadataPassthrough, DispatchClassControlPlane, DispatchClassLocalSupport, DispatchClassWebSearchTool:
		return true
	default:
		return false
	}
}

func validSelectionStrategy(value SelectionStrategy) bool {
	switch value {
	case SelectionStrategyRouter, SelectionStrategyFixedCatalog, SelectionStrategyDeploymentHardPin, SelectionStrategyClientAuthoritative, SelectionStrategyPassthrough, SelectionStrategyNone:
		return true
	default:
		return false
	}
}

func validCandidateSource(value CandidateSource) bool {
	switch value {
	case CandidateSourceRoutableCatalog, CandidateSourceFixedCatalog, CandidateSourceDeployment, CandidateSourceRequest, CandidateSourceLocal, CandidateSourceNone:
		return true
	default:
		return false
	}
}

func validFallbackKind(value FallbackKind) bool {
	switch value {
	case FallbackKindPlanAlternatives, FallbackKindBinding, FallbackKindLocalRecovery, FallbackKindFullHistory, FallbackKindRoutedDispatch, FallbackKindNone:
		return true
	default:
		return false
	}
}

func validOverrideSource(value OverrideSource) bool {
	switch value {
	case OverrideSourceRequest, OverrideSourceSession, OverrideSourceInstallation, OverrideSourceDeployment, OverrideSourcePolicyDefault, OverrideSourceClientAuthoritative:
		return true
	default:
		return false
	}
}

func validMigrationStatus(value MigrationStatus) bool {
	switch value {
	case MigrationStatusLegacyDirect, MigrationStatusCompatAdapter, MigrationStatusExecutor, MigrationStatusNonInference:
		return true
	default:
		return false
	}
}

func validConstraint(value Constraint) bool {
	switch value {
	case ConstraintCatalogBinding, ConstraintCapability, ConstraintContextWindow, ConstraintCredentialScope, ConstraintModelExclusions, ConstraintProviderExclusions, ConstraintRequestFormat, ConstraintSpend, ConstraintTenant:
		return true
	default:
		return false
	}
}

func validBudgetSource(value BudgetSource) bool {
	switch value {
	case BudgetSourceRequest, BudgetSourcePolicy, BudgetSourceDeployment, BudgetSourceLocal:
		return true
	default:
		return false
	}
}

func validSoftPreference(value SoftPreference) bool {
	switch value {
	case SoftPreferenceQualityPrice, SoftPreferencePreferredModels, SoftPreferenceCacheAffinity, SoftPreferenceSubscriptionCapacity:
		return true
	default:
		return false
	}
}

func validateOverridePrecedence(spec PolicySpec) error {
	precedenceRank := map[OverrideSource]int{
		OverrideSourceRequest:             0,
		OverrideSourceSession:             1,
		OverrideSourceInstallation:        2,
		OverrideSourceDeployment:          3,
		OverrideSourcePolicyDefault:       4,
		OverrideSourceClientAuthoritative: 0,
	}
	previousRank := -1
	for index, source := range spec.OverridePrecedence {
		if source == OverrideSourceClientAuthoritative && len(spec.OverridePrecedence) != 1 {
			return fmt.Errorf("policy %q must use client-authoritative override precedence alone", spec.PolicyID)
		}
		rank := precedenceRank[source]
		if index > 0 && rank <= previousRank {
			return fmt.Errorf("policy %q has invalid override precedence order", spec.PolicyID)
		}
		previousRank = rank
	}
	return nil
}

func validateSelectionContract(spec PolicySpec) error {
	switch spec.SelectionStrategy {
	case SelectionStrategyRouter:
		if spec.CandidateSource != CandidateSourceRoutableCatalog {
			return fmt.Errorf("router-selected policy %q must use routable catalog candidates", spec.PolicyID)
		}
	case SelectionStrategyDeploymentHardPin:
		if spec.CandidateSource != CandidateSourceDeployment {
			return fmt.Errorf("deployment hard-pin policy %q must use deployment candidates", spec.PolicyID)
		}
	case SelectionStrategyClientAuthoritative:
		if spec.CandidateSource != CandidateSourceRequest {
			return fmt.Errorf("client-authoritative policy %q must use request candidates", spec.PolicyID)
		}
	case SelectionStrategyPassthrough:
		if spec.CandidateSource != CandidateSourceRequest && spec.CandidateSource != CandidateSourceDeployment {
			return fmt.Errorf("passthrough policy %q must use request or deployment candidates", spec.PolicyID)
		}
	case SelectionStrategyNone:
		if spec.CandidateSource != CandidateSourceNone && spec.CandidateSource != CandidateSourceLocal && spec.CandidateSource != CandidateSourceDeployment {
			return fmt.Errorf("non-selecting policy %q has an invalid candidate source", spec.PolicyID)
		}
	}
	return nil
}

func registryRevision(specs []PolicySpec) (string, error) {
	payload, err := json.Marshal(struct {
		SchemaVersion string       `json:"schema_version"`
		Policies      []PolicySpec `json:"policies"`
	}{SchemaVersion: InferenceRegistrySchemaVersion, Policies: specs})
	if err != nil {
		return "", fmt.Errorf("marshal inference policy registry: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func clonePolicySpecs(specs []PolicySpec) []PolicySpec {
	cloned := make([]PolicySpec, len(specs))
	for i, spec := range specs {
		cloned[i] = clonePolicySpec(spec)
	}
	return cloned
}

func clonePolicySpec(spec PolicySpec) PolicySpec {
	spec.FixedCatalogModels = append([]string(nil), spec.FixedCatalogModels...)
	spec.HardConstraints = append([]Constraint(nil), spec.HardConstraints...)
	spec.SoftPreferences = append([]SoftPreference(nil), spec.SoftPreferences...)
	spec.OverridePrecedence = append([]OverrideSource(nil), spec.OverridePrecedence...)
	spec.Fallback.Alternatives = append([]string(nil), spec.Fallback.Alternatives...)
	return spec
}

func mustRegistry(specs []PolicySpec) Registry {
	registry, err := NewRegistry(specs)
	if err != nil {
		panic(err)
	}
	return registry
}

var defaultInferenceRegistry = mustRegistry(defaultPolicySpecs())

func defaultPolicySpecs() []PolicySpec {
	mainConstraints := []Constraint{
		ConstraintCatalogBinding,
		ConstraintCapability,
		ConstraintContextWindow,
		ConstraintCredentialScope,
		ConstraintModelExclusions,
		ConstraintProviderExclusions,
		ConstraintRequestFormat,
		ConstraintSpend,
		ConstraintTenant,
	}
	mainOverrides := []OverrideSource{
		OverrideSourceRequest,
		OverrideSourceSession,
		OverrideSourceInstallation,
		OverrideSourceDeployment,
		OverrideSourcePolicyDefault,
	}
	mainPreferences := []SoftPreference{
		SoftPreferenceQualityPrice,
		SoftPreferencePreferredModels,
		SoftPreferenceCacheAffinity,
		SoftPreferenceSubscriptionCapacity,
	}
	mainPolicy := func(purpose Purpose, id PolicyID, revision PolicyRevision, status MigrationStatus, rationale string) PolicySpec {
		return PolicySpec{
			Purpose:            purpose,
			DispatchClass:      DispatchClassMainInference,
			PolicyID:           id,
			PolicyRevision:     revision,
			Owner:              inferencePolicyOwner,
			Rationale:          rationale,
			SelectionStrategy:  SelectionStrategyRouter,
			CandidateSource:    CandidateSourceRoutableCatalog,
			HardConstraints:    append([]Constraint(nil), mainConstraints...),
			SoftPreferences:    append([]SoftPreference(nil), mainPreferences...),
			OverridePrecedence: append([]OverrideSource(nil), mainOverrides...),
			Budget:             BudgetSpec{Source: BudgetSourceRequest},
			Fallback:           FallbackSpec{Kind: FallbackKindBinding},
			MigrationStatus:    status,
		}
	}
	hardPinPolicy := func(purpose Purpose, id PolicyID, rationale string) PolicySpec {
		return PolicySpec{
			Purpose:            purpose,
			DispatchClass:      DispatchClassAuxiliaryInference,
			PolicyID:           id,
			PolicyRevision:     "1",
			Owner:              inferencePolicyOwner,
			Rationale:          rationale,
			SelectionStrategy:  SelectionStrategyDeploymentHardPin,
			CandidateSource:    CandidateSourceDeployment,
			HardConstraints:    append([]Constraint(nil), mainConstraints...),
			OverridePrecedence: []OverrideSource{OverrideSourceInstallation, OverrideSourceDeployment, OverrideSourcePolicyDefault},
			Budget:             BudgetSpec{Source: BudgetSourceRequest},
			Fallback:           FallbackSpec{Kind: FallbackKindNone},
			MigrationStatus:    MigrationStatusLegacyDirect,
		}
	}
	controlPolicy := func(purpose Purpose, id PolicyID, rationale string) PolicySpec {
		return PolicySpec{
			Purpose:           purpose,
			DispatchClass:     DispatchClassControlPlane,
			PolicyID:          id,
			PolicyRevision:    "1",
			Owner:             inferencePolicyOwner,
			Rationale:         rationale,
			SelectionStrategy: SelectionStrategyNone,
			CandidateSource:   CandidateSourceNone,
			Budget:            BudgetSpec{Source: BudgetSourceDeployment},
			Fallback:          FallbackSpec{Kind: FallbackKindNone},
			MigrationStatus:   MigrationStatusNonInference,
		}
	}

	return []PolicySpec{
		mainPolicy(PurposeAnthropicMessages, "main-anthropic-messages", "3", MigrationStatusExecutor, "Select an eligible catalog binding for Anthropic Messages while preserving request semantics and tenant boundaries."),
		mainPolicy(PurposeOpenAIChatCompletions, "main-openai-chat-completions", "3", MigrationStatusExecutor, "Select an eligible catalog binding for OpenAI Chat Completions while preserving request semantics and tenant boundaries."),
		mainPolicy(PurposeOpenAIResponses, "main-openai-responses", "3", MigrationStatusExecutor, "Select an eligible catalog binding and compatible endpoint for OpenAI Responses requests."),
		mainPolicy(PurposeGeminiGenerateContent, "main-gemini-generate-content", "3", MigrationStatusExecutor, "Select an eligible catalog binding for Gemini Generate Content while preserving native URL and body semantics."),
		{
			Purpose:            PurposeHandoverSummary,
			DispatchClass:      DispatchClassAuxiliaryInference,
			PolicyID:           "aux-handover-summary",
			PolicyRevision:     "2",
			Owner:              inferencePolicyOwner,
			Rationale:          "Use the current inexpensive summarizer before a model switch; failure preserves the full prior history.",
			SelectionStrategy:  SelectionStrategyFixedCatalog,
			CandidateSource:    CandidateSourceFixedCatalog,
			FixedCatalogModels: []string{HandoverSummaryDefaultModel},
			HardConstraints:    []Constraint{ConstraintCatalogBinding, ConstraintCredentialScope, ConstraintModelExclusions, ConstraintProviderExclusions, ConstraintTenant},
			OverridePrecedence: []OverrideSource{OverrideSourceDeployment, OverrideSourcePolicyDefault},
			Budget:             BudgetSpec{Source: BudgetSourcePolicy, MaxAttempts: 1, TimeoutMillis: 8_000, MaxOutputTokens: 800},
			Fallback:           FallbackSpec{Kind: FallbackKindFullHistory},
			MigrationStatus:    MigrationStatusExecutor,
		},
		{
			Purpose:            PurposePrecompactionSummary,
			DispatchClass:      DispatchClassAuxiliaryInference,
			PolicyID:           "aux-precompaction-summary",
			PolicyRevision:     "2",
			Owner:              inferencePolicyOwner,
			Rationale:          "Preserve elided task state with the context-window-aware summarizer cascade before local trim rescue: the session's warm Anthropic pin when it is a reviewed non-low model, else the deployment compaction model, else the large-window model.",
			SelectionStrategy:  SelectionStrategyFixedCatalog,
			CandidateSource:    CandidateSourceFixedCatalog,
			FixedCatalogModels: compactionSummarizerModels,
			HardConstraints:    []Constraint{ConstraintCatalogBinding, ConstraintContextWindow, ConstraintCredentialScope, ConstraintModelExclusions, ConstraintProviderExclusions, ConstraintTenant},
			OverridePrecedence: []OverrideSource{OverrideSourceSession, OverrideSourceDeployment, OverrideSourcePolicyDefault},
			Budget:             BudgetSpec{Source: BudgetSourcePolicy, MaxAttempts: 1, TimeoutMillis: 90_000, MaxOutputTokens: 4_000},
			Fallback:           FallbackSpec{Kind: FallbackKindLocalRecovery},
			MigrationStatus:    MigrationStatusExecutor,
		},
		{
			Purpose:            PurposeCompactionHandoverSummary,
			DispatchClass:      DispatchClassAuxiliaryInference,
			PolicyID:           "aux-compaction-handover-summary",
			PolicyRevision:     "3",
			Owner:              inferencePolicyOwner,
			Rationale:          "Restore task state after client history trimming with the same reviewed summarizer set as precompaction; failure keeps the client's remaining history unchanged.",
			SelectionStrategy:  SelectionStrategyFixedCatalog,
			CandidateSource:    CandidateSourceFixedCatalog,
			FixedCatalogModels: compactionSummarizerModels,
			HardConstraints:    []Constraint{ConstraintCatalogBinding, ConstraintContextWindow, ConstraintCredentialScope, ConstraintModelExclusions, ConstraintProviderExclusions, ConstraintTenant},
			OverridePrecedence: []OverrideSource{OverrideSourceSession, OverrideSourceDeployment, OverrideSourcePolicyDefault},
			Budget:             BudgetSpec{Source: BudgetSourcePolicy, MaxAttempts: 1, TimeoutMillis: 90_000, MaxOutputTokens: 4_000},
			Fallback:           FallbackSpec{Kind: FallbackKindFullHistory},
			MigrationStatus:    MigrationStatusExecutor,
		},
		hardPinPolicy(PurposeTitleGeneration, "aux-title-generation", "Keep hidden title-generation calls cheap and isolated from the main session pin."),
		hardPinPolicy(PurposeClassifier, "aux-classifier", "Serve client classifier turns without contaminating the main session pin."),
		hardPinPolicy(PurposeProbe, "aux-probe", "Serve provider and quota probes without creating a durable session pin."),
		hardPinPolicy(PurposeSubAgentDispatch, "aux-sub-agent-dispatch", "Apply the reviewed deployment hard pin for sub-agent work while preserving tenant eligibility."),
		{
			Purpose:            PurposeClientCompaction,
			DispatchClass:      DispatchClassClientAuthoritative,
			PolicyID:           "client-compaction",
			PolicyRevision:     "1",
			Owner:              inferencePolicyOwner,
			Rationale:          "Treat the client-supplied compaction turn and body as authoritative rather than inventing another router summary operation.",
			SelectionStrategy:  SelectionStrategyClientAuthoritative,
			CandidateSource:    CandidateSourceRequest,
			HardConstraints:    []Constraint{ConstraintCredentialScope, ConstraintRequestFormat, ConstraintTenant},
			OverridePrecedence: []OverrideSource{OverrideSourceClientAuthoritative},
			Budget:             BudgetSpec{Source: BudgetSourceRequest},
			Fallback:           FallbackSpec{Kind: FallbackKindNone},
			MigrationStatus:    MigrationStatusLegacyDirect,
		},
		{
			Purpose:            PurposeAgentShadowEvaluation,
			DispatchClass:      DispatchClassAuxiliaryInference,
			PolicyID:           "aux-agent-shadow-evaluation",
			PolicyRevision:     "1",
			Owner:              inferencePolicyOwner,
			Rationale:          "Evaluate an explicitly requested catalog model without mutating serving pins or policy state.",
			SelectionStrategy:  SelectionStrategyClientAuthoritative,
			CandidateSource:    CandidateSourceRequest,
			HardConstraints:    append([]Constraint(nil), mainConstraints...),
			OverridePrecedence: []OverrideSource{OverrideSourceRequest, OverrideSourceInstallation, OverrideSourceDeployment},
			Budget:             BudgetSpec{Source: BudgetSourceRequest},
			Fallback:           FallbackSpec{Kind: FallbackKindBinding},
			MigrationStatus:    MigrationStatusLegacyDirect,
		},
		{
			Purpose:            PurposeCountTokens,
			DispatchClass:      DispatchClassMetadataPassthrough,
			PolicyID:           "metadata-count-tokens",
			PolicyRevision:     "1",
			Owner:              inferencePolicyOwner,
			Rationale:          "Forward Anthropic token counting without model selection and use the bounded local estimator only for transient upstream failure.",
			SelectionStrategy:  SelectionStrategyPassthrough,
			CandidateSource:    CandidateSourceRequest,
			HardConstraints:    []Constraint{ConstraintCredentialScope, ConstraintRequestFormat, ConstraintTenant},
			OverridePrecedence: []OverrideSource{OverrideSourceClientAuthoritative},
			Budget:             BudgetSpec{Source: BudgetSourcePolicy, MaxAttempts: 1, TimeoutMillis: 5_000},
			Fallback:           FallbackSpec{Kind: FallbackKindLocalRecovery},
			MigrationStatus:    MigrationStatusExecutor,
		},
		{
			Purpose:            PurposeUpstreamModelListing,
			DispatchClass:      DispatchClassMetadataPassthrough,
			PolicyID:           "metadata-upstream-model-listing",
			PolicyRevision:     "1",
			Owner:              inferencePolicyOwner,
			Rationale:          "List models from the operator-selected upstream endpoint without automatic inference selection.",
			SelectionStrategy:  SelectionStrategyPassthrough,
			CandidateSource:    CandidateSourceDeployment,
			HardConstraints:    []Constraint{ConstraintCredentialScope, ConstraintTenant},
			OverridePrecedence: []OverrideSource{OverrideSourceDeployment},
			Budget:             BudgetSpec{Source: BudgetSourceDeployment},
			Fallback:           FallbackSpec{Kind: FallbackKindNone},
			MigrationStatus:    MigrationStatusNonInference,
		},
		controlPolicy(PurposePolicySidecarDecision, "control-policy-sidecar-decision", "Ask the configured policy sidecar for a routing decision; this is control-plane I/O, not upstream inference."),
		controlPolicy(PurposePolicySidecarPreview, "control-policy-sidecar-preview", "Preview a policy decision without serving or mutating routing state."),
		controlPolicy(PurposePolicySidecarOutcome, "control-policy-sidecar-outcome", "Report bounded serving outcomes to the configured policy sidecar."),
		controlPolicy(PurposePolicySidecarFeedback, "control-policy-sidecar-feedback", "Report explicit bounded feedback to the configured policy sidecar."),
		{
			Purpose:           PurposeClusterEmbedding,
			DispatchClass:     DispatchClassLocalSupport,
			PolicyID:          "local-cluster-embedding",
			PolicyRevision:    "1",
			Owner:             inferencePolicyOwner,
			Rationale:         "Compute routing features locally; this operation must never gain an upstream provider escape hatch.",
			SelectionStrategy: SelectionStrategyNone,
			CandidateSource:   CandidateSourceLocal,
			Budget:            BudgetSpec{Source: BudgetSourceLocal},
			Fallback:          FallbackSpec{Kind: FallbackKindNone},
			MigrationStatus:   MigrationStatusNonInference,
		},
		{
			Purpose:           PurposeSemanticCacheEmbedding,
			DispatchClass:     DispatchClassLocalSupport,
			PolicyID:          "local-semantic-cache-embedding",
			PolicyRevision:    "1",
			Owner:             inferencePolicyOwner,
			Rationale:         "Compute semantic-cache keys locally and independently from provider dispatch.",
			SelectionStrategy: SelectionStrategyNone,
			CandidateSource:   CandidateSourceLocal,
			Budget:            BudgetSpec{Source: BudgetSourceLocal},
			Fallback:          FallbackSpec{Kind: FallbackKindNone},
			MigrationStatus:   MigrationStatusNonInference,
		},
		{
			Purpose:           PurposeNativeWebSearch,
			DispatchClass:     DispatchClassWebSearchTool,
			PolicyID:          "tool-native-web-search",
			PolicyRevision:    "1",
			Owner:             inferencePolicyOwner,
			Rationale:         "Execute the explicit native web-search tool without exposing it as generic model inference.",
			SelectionStrategy: SelectionStrategyNone,
			CandidateSource:   CandidateSourceDeployment,
			HardConstraints:   []Constraint{ConstraintCredentialScope, ConstraintTenant},
			Budget:            BudgetSpec{Source: BudgetSourceDeployment},
			Fallback:          FallbackSpec{Kind: FallbackKindRoutedDispatch},
			MigrationStatus:   MigrationStatusNonInference,
		},
	}
}
