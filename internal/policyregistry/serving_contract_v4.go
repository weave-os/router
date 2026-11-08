package policyregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

// ClassifierVariantKind describes the immutable artifact format for one variant.
type ClassifierVariantKind string

const (
	ClassifierVariantKindHMMPackage ClassifierVariantKind = "hmm_package"
	ClassifierVariantKindQwenBundle ClassifierVariantKind = "qwen_bundle"
)

// ModelArtifactDataKind is the WeaveData kind for published HMM package archives.
type ModelArtifactDataKind string

const ClassifierModelArtifactDataKind ModelArtifactDataKind = "model_artifact"

// ClassifierModelArtifactRole is the canonical publication role for HMM packages.
type ClassifierModelArtifactRole string

const ClassifierModelArtifactRoleTrainedPolicy ClassifierModelArtifactRole = "trained_policy_artifact"

// WeaveDataModelArtifactRef mirrors the canonical WeaveDataRef JSON without importing ml_infra.
type WeaveDataModelArtifactRef struct {
	DataID        string                      `json:"data_id"`
	DataKind      ModelArtifactDataKind       `json:"data_kind"`
	SchemaVersion string                      `json:"schema_version"`
	Version       string                      `json:"version"`
	StorageURI    string                      `json:"storage_uri"`
	ContentSHA256 string                      `json:"content_sha256"`
	RowCount      *int64                      `json:"row_count"`
	Role          ClassifierModelArtifactRole `json:"role"`
	Required      bool                        `json:"required"`
}

// ClassifierTaxonomy binds the exact ordered classifier labels to one digest.
type ClassifierTaxonomy struct {
	ClassOrder []string `json:"class_order"`
	SHA256     string   `json:"sha256"`
}

// ClassifierVariantManifest pins one HMM package or Qwen classifier bundle.
// Qwen has a source bundle digest rather than a fabricated GCS package object.
type ClassifierVariantManifest struct {
	Kind                        ClassifierVariantKind      `json:"kind"`
	ArtifactID                  string                     `json:"artifact_id"`
	ArtifactSHA256              string                     `json:"artifact_sha256"`
	PackageWeaveDataRef         *WeaveDataModelArtifactRef `json:"package_weavedata_ref,omitempty"`
	BundleSHA256                string                     `json:"bundle_sha256,omitempty"`
	AuxiliaryModels             map[string]ObjectRef       `json:"auxiliary_models"`
	SemanticConfigurationSHA256 string                     `json:"semantic_configuration_sha256"`
	EmbeddingContractSHA256     string                     `json:"embedding_contract_sha256,omitempty"`
}

// ClassifierReleaseProvenance records immutable public source and build evidence.
type ClassifierReleaseProvenance struct {
	RouterRevision   string    `json:"router_revision"`
	WeaveRevision    string    `json:"weave_revision"`
	BuildAttestation ObjectRef `json:"build_attestation"`
}

// ClassifierReleaseManifestV1 is an immutable logical classifier release.
// Its release ID is ObjectRef.SHA256 over the exact serialized manifest bytes.
type ClassifierReleaseManifestV1 struct {
	SchemaVersion      ServingSchema                                            `json:"schema_version"`
	APIMajor           policy.ClassifierAPIMajor                                `json:"api_major"`
	WireSchema         string                                                   `json:"wire_schema"`
	Taxonomy           ClassifierTaxonomy                                       `json:"taxonomy"`
	Variants           map[policy.ClassifierVariantID]ClassifierVariantManifest `json:"variants"`
	RuntimeImageDigest string                                                   `json:"runtime_image_digest"`
	Provenance         ClassifierReleaseProvenance                              `json:"provenance"`
}

// Validate enforces content identity without embedding deployment URLs or secrets.
func (m ClassifierReleaseManifestV1) Validate(root string) error {
	if m.SchemaVersion != ServingClassifierReleaseV1 || m.APIMajor != policy.ClassifierAPIMajorV5 || m.WireSchema != policy.SchemaVersionV5 {
		return errors.New("unsupported classifier release schema or API contract")
	}
	if len(m.Taxonomy.ClassOrder) == 0 || hasDuplicateLabel(m.Taxonomy.ClassOrder) || !validDigest(m.Taxonomy.SHA256) || TaxonomyDigest(m.Taxonomy.ClassOrder) != m.Taxonomy.SHA256 {
		return errors.New("classifier release taxonomy is invalid")
	}
	if !validImageDigest(m.RuntimeImageDigest) {
		return errors.New("classifier release runtime image digest is invalid")
	}
	if len(m.Variants) == 0 || m.Variants[policy.ClassifierVariantPrimary].ArtifactID == "" {
		return errors.New("classifier release requires a primary variant")
	}
	for variantID, variant := range m.Variants {
		if variantID != policy.ClassifierVariantPrimary && variantID != policy.ClassifierVariantHMMBeta && variantID != policy.ClassifierVariantQwen {
			return fmt.Errorf("unsupported classifier variant %q", variantID)
		}
		if strings.TrimSpace(variant.ArtifactID) == "" || !validDigest(variant.ArtifactSHA256) || variant.AuxiliaryModels == nil {
			return fmt.Errorf("classifier variant %q has incomplete identity", variantID)
		}
		if !validDigest(variant.SemanticConfigurationSHA256) {
			return fmt.Errorf("classifier variant %q semantic configuration digest is invalid", variantID)
		}
		for name, ref := range variant.AuxiliaryModels {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("classifier variant %q has an empty auxiliary model name", variantID)
			}
			if err := validateArtifactRef(ref); err != nil {
				return fmt.Errorf("classifier variant %q auxiliary model %q: %w", variantID, name, err)
			}
		}
		switch variant.Kind {
		case ClassifierVariantKindHMMPackage:
			if variantID == policy.ClassifierVariantQwen || variant.PackageWeaveDataRef == nil || variant.BundleSHA256 != "" || !validDigest(variant.EmbeddingContractSHA256) {
				return fmt.Errorf("HMM variant %q requires a WeaveData package ref and embedding contract digest", variantID)
			}
			if len(variant.AuxiliaryModels) != 0 {
				return fmt.Errorf("HMM variant %q cannot declare unsupported auxiliary model refs", variantID)
			}
			ref := variant.PackageWeaveDataRef
			if ref.DataID == "" || strings.TrimSpace(ref.StorageURI) == "" || ref.DataKind != ClassifierModelArtifactDataKind || ref.SchemaVersion == "" || ref.Version == "" || !validDigest(ref.ContentSHA256) || ref.Role != ClassifierModelArtifactRoleTrainedPolicy || !ref.Required || (ref.RowCount != nil && *ref.RowCount < 0) {
				return fmt.Errorf("classifier variant %q WeaveDataRef is incomplete or not required", variantID)
			}
			if variant.PackageWeaveDataRef.ContentSHA256 != variant.ArtifactSHA256 {
				return fmt.Errorf("classifier variant %q WeaveDataRef content hash does not match artifact_sha256", variantID)
			}
		case ClassifierVariantKindQwenBundle:
			if variantID != policy.ClassifierVariantQwen || variant.PackageWeaveDataRef != nil || variant.EmbeddingContractSHA256 != "" || variant.BundleSHA256 != variant.ArtifactSHA256 || !validDigest(variant.BundleSHA256) || len(variant.AuxiliaryModels) != 0 {
				return errors.New("Qwen variant requires its bundle digest and no HMM package or embedding refs")
			}
		default:
			return fmt.Errorf("classifier variant %q has unsupported kind %q", variantID, variant.Kind)
		}
	}
	if !sourceRevisionPattern.MatchString(m.Provenance.RouterRevision) || !sourceRevisionPattern.MatchString(m.Provenance.WeaveRevision) {
		return errors.New("classifier release provenance requires exact source revisions")
	}
	return validateArtifactRef(m.Provenance.BuildAttestation)
}

// CandidateRequirementsV3 declares immutable worker/API compatibility only.
type CandidateRequirementsV3 struct {
	RuntimeContract    ServingSchema               `json:"runtime_contract"`
	PolicySchema       rosterdata.SchemaVersion    `json:"policy_schema"`
	SupportedAPIMajors []policy.ClassifierAPIMajor `json:"supported_api_majors"`
}

// RouterCandidateRequirementsDeclarationV1 is the schema_version written to RouterCandidateRequirementsPath; the checked-in file must carry this exact value.
const RouterCandidateRequirementsDeclarationV1 ServingSchema = "router_candidate_requirements_v1"

// RouterCandidateRequirementsPath is the checked-in declaration publishers read at a Router commit.
const RouterCandidateRequirementsPath = "docs/ROUTER_CANDIDATE_REQUIREMENTS.json"

// RouterCandidateRequirementsDeclaration states what this Router build supports; a publisher copies
// Requirements verbatim into the CandidateV3 it builds from the same commit.
type RouterCandidateRequirementsDeclaration struct {
	SchemaVersion ServingSchema           `json:"schema_version"`
	Requirements  CandidateRequirementsV3 `json:"requirements"`
}

// BuiltRouterCandidateRequirements derives the requirements from the runtime contract, policy schema, and classifier API majors compiled into this build.
func BuiltRouterCandidateRequirements() CandidateRequirementsV3 {
	return CandidateRequirementsV3{
		RuntimeContract:    ManagedRuntimeContractV1,
		PolicySchema:       rosterdata.SchemaVersionPolicyV1,
		SupportedAPIMajors: policy.SupportedClassifierAPIMajors(),
	}
}

// RouterCandidateRequirementsJSON renders the declaration written to RouterCandidateRequirementsPath.
func RouterCandidateRequirementsJSON() ([]byte, error) {
	encoded, err := json.MarshalIndent(RouterCandidateRequirementsDeclaration{SchemaVersion: RouterCandidateRequirementsDeclarationV1, Requirements: BuiltRouterCandidateRequirements()}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// CandidateV3 contains Router worker code and policy only; classifier releases are target-bound.
type CandidateV3 struct {
	SchemaVersion     ServingSchema           `json:"schema_version"`
	RouterImageDigest string                  `json:"router_image_digest"`
	Requirements      CandidateRequirementsV3 `json:"requirements"`
	Provenance        ServingProvenance       `json:"provenance"`
}

// Validate checks worker and classifier API compatibility independently of classifier content.
func (c CandidateV3) Validate(root string) error {
	if c.SchemaVersion != ServingCandidateV3 || !validImageDigest(c.RouterImageDigest) {
		return errors.New("invalid v3 candidate schema or Router image digest")
	}
	if c.Requirements.RuntimeContract != ManagedRuntimeContractV1 || c.Requirements.PolicySchema != rosterdata.SchemaVersionPolicyV1 {
		return errors.New("unsupported v3 Router runtime or policy schema")
	}
	if len(c.Requirements.SupportedAPIMajors) == 0 || !slices.Contains(c.Requirements.SupportedAPIMajors, policy.ClassifierAPIMajorV5) {
		return errors.New("v3 Router candidate must support classifier API v5")
	}
	if !slices.IsSorted(c.Requirements.SupportedAPIMajors) || len(slices.Compact(slices.Clone(c.Requirements.SupportedAPIMajors))) != len(c.Requirements.SupportedAPIMajors) {
		return errors.New("supported_api_majors must be sorted and unique")
	}
	return c.Provenance.validate()
}

// ClassifierServiceBinding is one immutable target's logical release and variant allowlist.
// ServiceURL and Audience are retired gateway fields: both present or both omitted, accepted
// for historical selections but never dispatched to; the release's runtime binding names the worker.
type ClassifierServiceBinding struct {
	Release         ObjectRef                    `json:"release"`
	ServiceURL      string                       `json:"service_url,omitempty"`
	Audience        string                       `json:"audience,omitempty"`
	EnabledVariants []policy.ClassifierVariantID `json:"enabled_variants"`
}

func (b ClassifierServiceBinding) validate(root string) error {
	if err := ValidateServingRef(b.Release, root, ServingClassifierReleases); err != nil {
		return err
	}
	if len(b.EnabledVariants) == 0 || !slices.Contains(b.EnabledVariants, policy.ClassifierVariantPrimary) {
		return errors.New("classifier service binding must enable its primary variant")
	}
	if !slices.IsSorted(b.EnabledVariants) || len(slices.Compact(slices.Clone(b.EnabledVariants))) != len(b.EnabledVariants) {
		return errors.New("enabled_variants must be sorted and unique")
	}
	for _, variantID := range b.EnabledVariants {
		if variantID != policy.ClassifierVariantPrimary && variantID != policy.ClassifierVariantHMMBeta && variantID != policy.ClassifierVariantQwen {
			return fmt.Errorf("unsupported enabled classifier variant %q", variantID)
		}
	}
	if b.ServiceURL == "" && b.Audience == "" {
		return nil
	}
	serviceURL, err := url.Parse(b.ServiceURL)
	if err != nil || serviceURL.Scheme != "https" || serviceURL.Host == "" || serviceURL.User != nil || serviceURL.Path != "" || serviceURL.RawQuery != "" || serviceURL.Fragment != "" {
		return errors.New("classifier service URL must be a stable HTTPS service origin")
	}
	audience, err := url.Parse(b.Audience)
	if err != nil || audience.Scheme != "https" || audience.Host == "" || audience.User != nil || audience.Path != "" || audience.RawQuery != "" || audience.Fragment != "" {
		return errors.New("classifier service audience must be an HTTPS origin")
	}
	return nil
}

// CandidateCodeV4 is the target-specific Router worker binding for a v4 set.
type CandidateCodeV4 struct {
	Candidate   ObjectRef       `json:"candidate"`
	Project     string          `json:"project"`
	Region      string          `json:"region"`
	Router      RevisionBinding `json:"router"`
	Attestation ObjectRef       `json:"attestation"`
}

func (b CandidateCodeV4) validate(root string) error {
	if err := ValidateServingRef(b.Candidate, root, ServingCandidate); err != nil {
		return err
	}
	if strings.TrimSpace(b.Project) == "" || strings.TrimSpace(b.Region) == "" {
		return errors.New("v4 selection code binding requires project and region")
	}
	if err := b.Router.validate(); err != nil {
		return err
	}
	return validateArtifactRef(b.Attestation)
}

// SelectionSetV4 atomically selects target policy and logical classifier release.
type SelectionSetV4 struct {
	SchemaVersion    ServingSchema                    `json:"schema_version"`
	Target           ServingTarget                    `json:"target"`
	Code             CandidateCodeV4                  `json:"code"`
	Classifier       ClassifierServiceBinding         `json:"classifier"`
	DefaultPolicy    PolicyObject                     `json:"default_policy"`
	StrategyPolicies map[router.Strategy]PolicyObject `json:"strategy_policies"`
	Profiles         map[string]PolicyObject          `json:"profiles"`
}

// Validate checks all target-level immutable identities and enabled variants.
func (s SelectionSetV4) Validate(root string) error {
	if s.SchemaVersion != ServingSelectionSetV4 || s.Profiles == nil || s.StrategyPolicies == nil {
		return errors.New("v4 selection set requires a supported schema and explicit policy maps")
	}
	if _, err := s.Target.Environment(); err != nil {
		return err
	}
	if err := s.Code.validate(root); err != nil {
		return fmt.Errorf("selection set code binding: %w", err)
	}
	if err := s.Classifier.validate(root); err != nil {
		return fmt.Errorf("selection set classifier binding: %w", err)
	}
	if s.Target != TargetInternal {
		for _, variantID := range s.Classifier.EnabledVariants {
			if variantID != policy.ClassifierVariantPrimary {
				return fmt.Errorf("classifier binding enables internal-only variant %q for target %q", variantID, s.Target)
			}
		}
	}
	if err := validateServingPolicy(s.DefaultPolicy, root); err != nil {
		return fmt.Errorf("selection set default policy: %w", err)
	}
	for key, policyObject := range s.Profiles {
		if err := validateProfileKey(key); err != nil {
			return err
		}
		if err := validateServingPolicy(policyObject, root); err != nil {
			return fmt.Errorf("selection set profile %q: %w", key, err)
		}
	}
	for strategy, policyObject := range s.StrategyPolicies {
		if !router.IsBetaStrategy(strategy) {
			return fmt.Errorf("selection set strategy policy %q is not a beta strategy", strategy)
		}
		if err := validateServingPolicy(policyObject, root); err != nil {
			return fmt.Errorf("selection set strategy policy %q: %w", strategy, err)
		}
		if policyObject.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
			return fmt.Errorf("selection set strategy policy %q has unsupported schema", strategy)
		}
	}
	return nil
}

// View preserves the exact persisted session binding shape used by v1-v3 readers.
func (s SelectionSetV4) View(ref ObjectRef) SelectionSetView {
	profiles := make(map[string]ServingSelection, len(s.Profiles))
	for key := range s.Profiles {
		profileRef := ref
		profiles[key] = ServingSelection{Release: s.Code.Candidate, Binding: ref, Profile: &profileRef}
	}
	return SelectionSetView{Target: s.Target, Default: ServingSelection{Release: s.Code.Candidate, Binding: ref}, Profiles: profiles}
}

// PolicyFor returns the default policy for an empty profile key, otherwise the named profile's policy.
func (s SelectionSetV4) PolicyFor(profileKey string) (PolicyObject, bool) {
	if profileKey == "" {
		return s.DefaultPolicy, true
	}
	policyObject, exists := s.Profiles[profileKey]
	return policyObject, exists
}

func hasDuplicateLabel(labels []string) bool {
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if _, exists := seen[label]; exists {
			return true
		}
		seen[label] = struct{}{}
	}
	return false
}
