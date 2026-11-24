package policyregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

const (
	ServingCandidate    ServingKind = "candidate"
	ServingSelectionSet ServingKind = "selection_set"
	ServingProposal     ServingKind = "proposal"
)

const (
	ServingCandidateV2    ServingSchema = "router_serving_candidate_v2"
	ServingSelectionSetV2 ServingSchema = "router_serving_selection_set_v2"
	ServingProposalV2     ServingSchema = "router_serving_proposal_v2"
)

const ServingArtifactsNamespace = "artifacts/"

type ClassifierComponent struct {
	Identity        ClassifierIdentity   `json:"identity"`
	Package         ObjectRef            `json:"package"`
	AuxiliaryModels map[string]ObjectRef `json:"auxiliary_models"`
	Configuration   ObjectRef            `json:"configuration"`
}

type CandidateComposition struct {
	RouterImageDigest          string                           `json:"router_image_digest"`
	Policy                     PolicyObject                     `json:"selection_policy"`
	StrategyPolicies           map[router.Strategy]PolicyObject `json:"strategy_policies,omitempty"`
	Classifier                 ClassifierComponent              `json:"classifier"`
	Requirements               ServingRequirements              `json:"requirements"`
	Provenance                 ServingProvenance                `json:"provenance"`
	ClassifierReleaseSHA256    string                           `json:"-"`
	ClassifierServiceURL       string                           `json:"-"`
	ClassifierAudience         string                           `json:"-"`
	EnabledClassifierVariants  []policy.ClassifierVariantID     `json:"-"`
	SupportedAPIMajors         []policy.ClassifierAPIMajor      `json:"-"`
	ClassifierReleaseV5        *ClassifierReleaseManifestV1     `json:"-"`
	ClassifierServiceBindingV4 *ClassifierServiceBinding        `json:"-"`
}

type CandidateV2 struct {
	SchemaVersion ServingSchema `json:"schema_version"`
	CandidateComposition
}

type LaneBinding struct {
	Project     string          `json:"project"`
	Region      string          `json:"region"`
	Router      RevisionBinding `json:"router"`
	Classifier  RevisionBinding `json:"classifier"`
	Attestation ObjectRef       `json:"attestation"`
}

type ServingLane struct {
	Candidate ObjectRef `json:"candidate"`
	LaneBinding
	ProfileKey               string                           `json:"profile_key,omitempty"`
	ProfilePolicy            *PolicyObject                    `json:"profile_policy,omitempty"`
	ProfileRequirements      *ServingRequirements             `json:"profile_requirements,omitempty"`
	classifierServiceBinding *ClassifierServiceBinding        `json:"-"`
	classifierReleaseV5      *ClassifierReleaseManifestV1     `json:"-"`
	v3Candidate              *CandidateV3                     `json:"-"`
	v4StrategyPolicies       map[router.Strategy]PolicyObject `json:"-"`
}

type SelectionSetV2 struct {
	SchemaVersion ServingSchema          `json:"schema_version"`
	Target        ServingTarget          `json:"target"`
	Default       ServingLane            `json:"default"`
	Profiles      map[string]ServingLane `json:"profiles"`
}

type DeploymentProposalV2 struct {
	SchemaVersion        ServingSchema `json:"schema_version"`
	Target               ServingTarget `json:"target"`
	PreviousSelectionSet *ObjectRef    `json:"previous_selection_set,omitempty"`
	SelectionSet         ObjectRef     `json:"selection_set"`
	SourceCandidate      ObjectRef     `json:"source_candidate"`
	Scope                ChangeScope   `json:"scope"`
	ProfileKey           string        `json:"profile_key,omitempty"`
	Actor                string        `json:"actor"`
	Reason               string        `json:"reason"`
	RequestID            string        `json:"request_id"`
	CreatedAt            time.Time     `json:"created_at"`
	Evidence             []ObjectRef   `json:"evidence"`
	WithdrawActivations  []string      `json:"withdraw_activations"`
}

func (b ClassifierBundle) component() ClassifierComponent {
	return ClassifierComponent{Identity: b.Identity, Package: b.Package, AuxiliaryModels: b.AuxiliaryModels, Configuration: b.Configuration}
}

func (r ServingRelease) composition(classifier ClassifierComponent) CandidateComposition {
	return CandidateComposition{RouterImageDigest: r.RouterImageDigest, Policy: r.Policy, Classifier: classifier, Requirements: r.Requirements, Provenance: r.Provenance}
}

func (b DeploymentBinding) lane() LaneBinding {
	return LaneBinding{Project: b.Project, Region: b.Region, Router: b.Router, Classifier: b.Classifier, Attestation: b.Attestation}
}

func (c ClassifierComponent) Equal(other ClassifierComponent) bool {
	return c.Identity.Equal(other.Identity) && c.Package == other.Package && c.Configuration == other.Configuration && maps.Equal(c.AuxiliaryModels, other.AuxiliaryModels)
}

func (c ClassifierComponent) validate() error {
	identity := c.Identity
	if strings.TrimSpace(identity.ArtifactID) == "" || !validImageDigest(identity.ImageDigest) || identity.WireSchema != ClassifierWireSchemaV4 || len(identity.ClassOrder) == 0 || TaxonomyDigest(identity.ClassOrder) != identity.TaxonomySHA256 {
		return errors.New("invalid classifier bundle identity or schema")
	}
	if err := identity.validateVariants(); err != nil {
		return err
	}
	if err := ValidateArtifactRef(c.Package); err != nil {
		return err
	}
	if c.Package.SHA256 != identity.PackageSHA256 {
		return errors.New("classifier package reference does not match attested identity")
	}
	if c.AuxiliaryModels == nil {
		return errors.New("classifier auxiliary model inventory is required, even when empty")
	}
	for name, ref := range c.AuxiliaryModels {
		if strings.TrimSpace(name) == "" {
			return errors.New("auxiliary model name is required")
		}
		if err := ValidateArtifactRef(ref); err != nil {
			return fmt.Errorf("auxiliary model %q: %w", name, err)
		}
	}
	return ValidateArtifactRef(c.Configuration)
}

func (p ServingProvenance) validate() error {
	if !sourceRevisionPattern.MatchString(p.RouterRevision) || !sourceRevisionPattern.MatchString(p.WeaveRevision) {
		return errors.New("serving provenance requires exact router and Weave source revisions")
	}
	return ValidateArtifactRef(p.BuildAttestation)
}

func (c CandidateComposition) Equal(other CandidateComposition) bool {
	return c.RouterImageDigest == other.RouterImageDigest && c.Policy == other.Policy && maps.Equal(c.StrategyPolicies, other.StrategyPolicies) && c.Requirements == other.Requirements && c.Provenance == other.Provenance && c.Classifier.Equal(other.Classifier) && c.ClassifierReleaseSHA256 == other.ClassifierReleaseSHA256 && c.ClassifierServiceURL == other.ClassifierServiceURL && c.ClassifierAudience == other.ClassifierAudience && slices.Equal(c.EnabledClassifierVariants, other.EnabledClassifierVariants) && slices.Equal(c.SupportedAPIMajors, other.SupportedAPIMajors)
}

func (c CandidateComposition) validate(root string) error {
	if !validImageDigest(c.RouterImageDigest) {
		return errors.New("invalid candidate router image digest")
	}
	if err := validateServingPolicy(c.Policy, root); err != nil {
		return err
	}
	if err := c.Classifier.validate(); err != nil {
		return err
	}
	if err := c.Requirements.validate(); err != nil {
		return err
	}
	if c.Requirements.ClassifierWireSchema != c.Classifier.Identity.WireSchema || c.Requirements.TaxonomySHA256 != c.Classifier.Identity.TaxonomySHA256 {
		return errors.New("candidate requirements do not match its classifier identity")
	}
	for strategy, policy := range c.StrategyPolicies {
		if !router.IsBetaStrategy(strategy) {
			return fmt.Errorf("candidate strategy policy %q is not a beta strategy", strategy)
		}
		if _, found := c.Classifier.Identity.Variants[strategy]; !found {
			return fmt.Errorf("candidate strategy policy %q has no classifier variant", strategy)
		}
		if err := validateServingPolicy(policy, root); err != nil {
			return fmt.Errorf("candidate strategy policy %q: %w", strategy, err)
		}
		if policy.SchemaVersion != c.Requirements.PolicySchema {
			return fmt.Errorf("candidate strategy policy %q schema does not match runtime requirements", strategy)
		}
	}
	return c.Provenance.validate()
}

func (c CandidateV2) Validate(root string) error {
	if c.SchemaVersion != ServingCandidateV2 {
		return errors.New("unsupported serving candidate schema")
	}
	return c.validate(root)
}

func (b LaneBinding) validate() error {
	if strings.TrimSpace(b.Project) == "" || strings.TrimSpace(b.Region) == "" {
		return errors.New("lane requires an explicit project and region")
	}
	if err := b.Router.validate(); err != nil {
		return err
	}
	if err := b.Classifier.validate(); err != nil {
		return err
	}
	return ValidateArtifactRef(b.Attestation)
}

func (l ServingLane) validate(root string, profileKey string) error {
	if err := ValidateServingRef(l.Candidate, root, ServingCandidate); err != nil {
		return err
	}
	if err := l.LaneBinding.validate(); err != nil {
		return err
	}
	if profileKey == "" {
		if l.ProfileKey != "" || l.ProfilePolicy != nil || l.ProfileRequirements != nil {
			return errors.New("default lane must not carry profile fields")
		}
		return nil
	}
	if l.ProfileKey != profileKey || l.ProfilePolicy == nil || l.ProfileRequirements == nil {
		return errors.New("profile lane must pin its own key, policy and requirements")
	}
	if err := validateServingPolicy(*l.ProfilePolicy, root); err != nil {
		return err
	}
	return l.ProfileRequirements.validate()
}

func (s SelectionSetV2) Validate(root string) error {
	if s.SchemaVersion != ServingSelectionSetV2 || s.Profiles == nil {
		return errors.New("selection set requires a supported schema and explicit profile map")
	}
	if _, err := s.Target.Environment(); err != nil {
		return err
	}
	if err := s.Default.validate(root, ""); err != nil {
		return fmt.Errorf("default lane: %w", err)
	}
	for key, lane := range s.Profiles {
		if err := validateProfileKey(key); err != nil {
			return err
		}
		if err := lane.validate(root, key); err != nil {
			return fmt.Errorf("profile %q: %w", key, err)
		}
	}
	return nil
}

func (s SelectionSetV2) lane(profileKey string) (ServingLane, bool) {
	if profileKey == "" {
		return s.Default, true
	}
	lane, exists := s.Profiles[profileKey]
	return lane, exists
}

func (p DeploymentProposalV2) Validate(root string) error {
	if p.SchemaVersion != ServingProposalV2 || strings.TrimSpace(p.Actor) == "" || strings.TrimSpace(p.Reason) == "" || p.CreatedAt.IsZero() {
		return errors.New("invalid proposal schema or audit identity")
	}
	if err := ValidateServingRequestID(p.RequestID); err != nil {
		return fmt.Errorf("proposal %w", err)
	}
	if _, err := p.Target.Environment(); err != nil {
		return err
	}
	if err := validateProposalScope(p.Scope, p.ProfileKey); err != nil {
		return err
	}
	if p.Scope == ChangeRollback && p.PreviousSelectionSet == nil {
		return errors.New("exact rollback requires an existing target activation")
	}
	if p.PreviousSelectionSet != nil {
		if err := ValidateServingRef(*p.PreviousSelectionSet, root, ServingSelectionSet); err != nil {
			return err
		}
	}
	if err := ValidateServingRef(p.SelectionSet, root, ServingSelectionSet); err != nil {
		return err
	}
	if err := ValidateServingRef(p.SourceCandidate, root, ServingCandidate); err != nil {
		return err
	}
	if err := validateProposalEvidence(p.Evidence); err != nil {
		return err
	}
	return validateWithdrawals(p.WithdrawActivations)
}

type ProposalView struct {
	Target               ServingTarget
	PreviousSelectionSet *ObjectRef
	SelectionSet         ObjectRef
	SourceCandidate      ObjectRef
	Scope                ChangeScope
	ProfileKey           string
	Actor                string
	Reason               string
	RequestID            string
	CreatedAt            time.Time
	Evidence             []ObjectRef
	WithdrawActivations  []string
	ExpectedGeneration   *int64
}

type ProposalManifest interface {
	ServingManifest
	View() ProposalView
}

func (p DeploymentProposal) View() ProposalView {
	expected := p.ExpectedGeneration
	return ProposalView{Target: p.Target, PreviousSelectionSet: p.PreviousSelectionSet, SelectionSet: p.SelectionSet, SourceCandidate: p.SourceRelease, Scope: p.Scope, ProfileKey: p.ProfileKey, Actor: p.Actor, Reason: p.Reason, RequestID: p.RequestID, CreatedAt: p.CreatedAt, Evidence: p.Evidence, WithdrawActivations: p.WithdrawActivations, ExpectedGeneration: &expected}
}

func (p DeploymentProposalV2) View() ProposalView {
	return ProposalView{Target: p.Target, PreviousSelectionSet: p.PreviousSelectionSet, SelectionSet: p.SelectionSet, SourceCandidate: p.SourceCandidate, Scope: p.Scope, ProfileKey: p.ProfileKey, Actor: p.Actor, Reason: p.Reason, RequestID: p.RequestID, CreatedAt: p.CreatedAt, Evidence: p.Evidence, WithdrawActivations: p.WithdrawActivations}
}

type SelectionSetView struct {
	Target   ServingTarget
	Default  ServingSelection
	Profiles map[string]ServingSelection
}

func (s SelectionSet) View() SelectionSetView {
	return SelectionSetView{Target: s.Target, Default: s.Default, Profiles: s.Profiles}
}

func (s SelectionSetV2) View(ref ObjectRef) SelectionSetView {
	profiles := make(map[string]ServingSelection, len(s.Profiles))
	for key, lane := range s.Profiles {
		profileRef := ref
		profiles[key] = ServingSelection{Release: lane.Candidate, Binding: ref, Profile: &profileRef}
	}
	return SelectionSetView{Target: s.Target, Default: ServingSelection{Release: s.Default.Candidate, Binding: ref}, Profiles: profiles}
}

func (v SelectionSetView) validate(root string) error {
	if v.Profiles == nil {
		return errors.New("selection set requires an explicit profile map")
	}
	if _, err := v.Target.Environment(); err != nil {
		return err
	}
	if err := v.Default.validate(root, false); err != nil {
		return err
	}
	for key, selection := range v.Profiles {
		if err := validateProfileKey(key); err != nil {
			return err
		}
		if err := selection.validate(root, true); err != nil {
			return fmt.Errorf("profile %q: %w", key, err)
		}
	}
	return nil
}

func (v SelectionSetView) selection(profileKey string) (ServingSelection, bool) {
	if profileKey == "" {
		return v.Default, true
	}
	selection, exists := v.Profiles[profileKey]
	return selection, exists
}

func servingSchemaOf(payload []byte) (ServingSchema, error) {
	var header struct {
		SchemaVersion ServingSchema `json:"schema_version"`
	}
	if err := json.Unmarshal(payload, &header); err != nil {
		return "", fmt.Errorf("read serving manifest schema: %w", err)
	}
	if header.SchemaVersion == "" {
		return "", errors.New("serving manifest declares no schema_version")
	}
	return header.SchemaVersion, nil
}

func servingFamilyManifest(kind ServingKind, schema ServingSchema) (ServingManifest, error) {
	switch {
	case kind == ServingCandidate && schema == ServingReleaseV1:
		return &ServingRelease{}, nil
	case kind == ServingCandidate && schema == ServingCandidateV2:
		return &CandidateV2{}, nil
	case kind == ServingCandidate && schema == ServingCandidateV3:
		return &CandidateV3{}, nil
	case kind == ServingSelectionSet && schema == ServingSelectionSetV1:
		return &SelectionSet{}, nil
	case kind == ServingSelectionSet && schema == ServingSelectionSetV2:
		return &SelectionSetV2{}, nil
	case kind == ServingSelectionSet && schema == ServingSelectionSetV3:
		return &SelectionSetV3{}, nil
	case kind == ServingSelectionSet && schema == ServingSelectionSetV4:
		return &SelectionSetV4{}, nil
	case kind == ServingClassifierReleases && schema == ServingClassifierReleaseV1:
		return &ClassifierReleaseManifestV1{}, nil
	case kind == ServingProposal && schema == ServingProposalV1:
		return &DeploymentProposal{}, nil
	case kind == ServingProposal && schema == ServingProposalV2:
		return &DeploymentProposalV2{}, nil
	default:
		return nil, fmt.Errorf("schema %q is not a %s manifest", schema, kind)
	}
}

func ServingFoldedKind(kind ServingKind) (ServingKind, bool) {
	switch kind {
	case ServingReleases, ServingClassifiers:
		return ServingCandidate, true
	case ServingBindings, ServingProfiles, ServingSelectionSets:
		return ServingSelectionSet, true
	case ServingProposals:
		return ServingProposal, true
	default:
		return "", false
	}
}

func ValidatePublishableServingKind(kind ServingKind) error {
	if folded, isV1 := ServingFoldedKind(kind); isV1 {
		return fmt.Errorf("serving kind %q is read-only: folded into %q", kind, folded)
	}
	switch kind {
	case ServingCandidate, ServingSelectionSet, ServingProposal, ServingClassifierReleases:
		return nil
	default:
		return fmt.Errorf("unsupported serving object kind %q", kind)
	}
}

func DecodePublishableServingManifest(payload []byte, root string, kind ServingKind) (ServingManifest, error) {
	if err := ValidatePublishableServingKind(kind); err != nil {
		return nil, err
	}
	manifest, err := DecodeServingManifest(payload, root, kind)
	if err != nil {
		return nil, err
	}
	if !isServingArtifactManifest(manifest) {
		return nil, fmt.Errorf("%s publication requires an artifacts/ schema; v1 objects are read-only", kind)
	}
	return manifest, nil
}

func isServingArtifactManifest(manifest ServingManifest) bool {
	switch manifest.(type) {
	case *CandidateV2, *CandidateV3, *SelectionSetV2, *SelectionSetV3, *SelectionSetV4, *DeploymentProposalV2, *ClassifierReleaseManifestV1:
		return true
	default:
		return false
	}
}

func isServingArtifactURI(uri, root string) bool {
	return strings.HasPrefix(uri, strings.TrimRight(root, "/")+"/"+ServingArtifactsNamespace)
}
