package policyregistry

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/router/hmm/rosterdata"
)

// ServingTarget is a managed runtime destination, independent of routing strategy.
type ServingTarget string

const (
	TargetStaging  ServingTarget = "staging"
	TargetStable   ServingTarget = "prod/stable"
	TargetInternal ServingTarget = "prod/weave-internal"
)

// Environment rejects legacy beta and cross-environment target combinations.
func (t ServingTarget) Environment() (Environment, error) {
	switch t {
	case TargetStaging:
		return EnvironmentStaging, nil
	case TargetStable, TargetInternal:
		return EnvironmentProd, nil
	default:
		return "", fmt.Errorf("unsupported managed target %q; beta is retired", t)
	}
}

// ServingKind selects a content-addressed namespace, never a mutable alias.
type ServingKind string

const (
	ServingReleases      ServingKind = "releases"
	ServingClassifiers   ServingKind = "classifiers"
	ServingBindings      ServingKind = "bindings"
	ServingProfiles      ServingKind = "profiles"
	ServingSelectionSets ServingKind = "selection_sets"
	ServingProposals     ServingKind = "proposals"
)

// ServingSchema versions managed contracts without changing published policy v1 bytes.
type ServingSchema string

const (
	ServingReleaseV1      ServingSchema = "router_serving_release_v1"
	ServingClassifierV1   ServingSchema = "router_serving_classifier_v1"
	ServingBindingV1      ServingSchema = "router_serving_binding_v1"
	ServingProfileV1      ServingSchema = "router_serving_profile_v1"
	ServingSelectionSetV1 ServingSchema = "router_serving_selection_set_v1"
	ServingProposalV1     ServingSchema = "router_serving_proposal_v1"
	ServingControlStateV1 ServingSchema = "router_serving_control_state_v1"
)

// ChangeScope specifies which destination components a proposal may replace.
type ChangeScope string

const (
	ChangeFull       ChangeScope = "full"
	ChangeRouter     ChangeScope = "router"
	ChangeRoster     ChangeScope = "roster"
	ChangeClassifier ChangeScope = "classifier"
	ChangeProfile    ChangeScope = "profile"
	ChangeCustom     ChangeScope = "custom"
	ChangeRollback   ChangeScope = "rollback"
)

// ServingRequirements are checked against the selected image and classifier, not current main.
type ServingRequirements struct {
	RuntimeContract      ServingSchema            `json:"runtime_contract"`
	PolicySchema         rosterdata.SchemaVersion `json:"policy_schema"`
	ClassifierWireSchema string                   `json:"classifier_wire_schema"`
	TaxonomySHA256       string                   `json:"taxonomy_sha256"`
}

// ManagedRuntimeContractV1 is the first exact-snapshot worker assertion contract.
const ManagedRuntimeContractV1 ServingSchema = "router_managed_runtime_v1"

// ServingProvenance keeps both source identities separate from artifact identity.
type ServingProvenance struct {
	RouterRevision   string    `json:"router_revision"`
	WeaveRevision    string    `json:"weave_revision"`
	BuildAttestation ObjectRef `json:"build_attestation"`
}

// ClassifierBundle binds every behavior-affecting model and configuration object.
// Configuration contains no secrets; secret references belong to target bindings.
type ClassifierBundle struct {
	SchemaVersion   ServingSchema        `json:"schema_version"`
	Identity        ClassifierIdentity   `json:"identity"`
	Package         ObjectRef            `json:"package"`
	AuxiliaryModels map[string]ObjectRef `json:"auxiliary_models"`
	Configuration   ObjectRef            `json:"configuration"`
}

// ServingRelease is an environment-neutral immutable base or effective profile tuple.
type ServingRelease struct {
	SchemaVersion     ServingSchema       `json:"schema_version"`
	RouterImageDigest string              `json:"router_image_digest"`
	Policy            PolicyObject        `json:"selection_policy"`
	Classifier        ObjectRef           `json:"classifier"`
	Requirements      ServingRequirements `json:"requirements"`
	Provenance        ServingProvenance   `json:"provenance"`
}

// RoutingProfile owns a stable opaque selection key; its bytes define one revision.
type RoutingProfile struct {
	SchemaVersion ServingSchema       `json:"schema_version"`
	ProfileKey    string              `json:"profile_key"`
	Policy        PolicyObject        `json:"selection_policy"`
	Requirements  ServingRequirements `json:"requirements"`
}

// RevisionBinding identifies a prepared immutable revision, including effective configuration.
type RevisionBinding struct {
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	Audience      string    `json:"audience"`
	ImageDigest   string    `json:"image_digest"`
	Configuration ObjectRef `json:"configuration"`
}

// DeploymentBinding realizes one release locally without changing its identity.
type DeploymentBinding struct {
	SchemaVersion          ServingSchema   `json:"schema_version"`
	Target                 ServingTarget   `json:"target"`
	Project                string          `json:"project"`
	Region                 string          `json:"region"`
	Release                ObjectRef       `json:"release"`
	Router                 RevisionBinding `json:"router"`
	Classifier             RevisionBinding `json:"classifier"`
	ClassifierBundleSHA256 string          `json:"classifier_bundle_sha256"`
	Attestation            ObjectRef       `json:"attestation"`
}

// ServingSelection pins the exact release, binding and optional profile revision.
type ServingSelection struct {
	Release ObjectRef  `json:"release"`
	Binding ObjectRef  `json:"binding"`
	Profile *ObjectRef `json:"profile,omitempty"`
}

// SelectionSet is activated atomically, including all registered organization-owned keys.
type SelectionSet struct {
	SchemaVersion ServingSchema               `json:"schema_version"`
	Target        ServingTarget               `json:"target"`
	Default       ServingSelection            `json:"default"`
	Profiles      map[string]ServingSelection `json:"profiles"`
}

// DeploymentProposal freezes the entire preview before approval or queueing.
type DeploymentProposal struct {
	SchemaVersion        ServingSchema `json:"schema_version"`
	Target               ServingTarget `json:"target"`
	ExpectedGeneration   int64         `json:"expected_generation"`
	PreviousSelectionSet *ObjectRef    `json:"previous_selection_set,omitempty"`
	SelectionSet         ObjectRef     `json:"selection_set"`
	SourceRelease        ObjectRef     `json:"source_release"`
	Scope                ChangeScope   `json:"scope"`
	ProfileKey           string        `json:"profile_key,omitempty"`
	Actor                string        `json:"actor"`
	Reason               string        `json:"reason"`
	RequestID            string        `json:"request_id"`
	CreatedAt            time.Time     `json:"created_at"`
	Evidence             []ObjectRef   `json:"evidence"`
	WithdrawActivations  []string      `json:"withdraw_activations"`
}

func (r ServingRequirements) validate() error {
	if r.RuntimeContract != ManagedRuntimeContractV1 || r.PolicySchema != rosterdata.SchemaVersionPolicyV1 || r.ClassifierWireSchema != ClassifierWireSchemaV4 || !validDigest(r.TaxonomySHA256) {
		return errors.New("unsupported managed runtime, policy, classifier or taxonomy requirement")
	}
	return nil
}

func validateServingPolicy(policy PolicyObject, root string) error {
	if policy.SchemaVersion != rosterdata.SchemaVersionPolicyV1 || !validDigest(policy.SHA256) || policy.Generation <= 0 || !withinRegistry(policy.URI, root, "router_policy/v1/policies/sha256/"+policy.SHA256+".json") {
		return errors.New("invalid exact compiled policy reference")
	}
	return nil
}

func validateArtifactRef(ref ObjectRef) error {
	if !validDigest(ref.SHA256) || ref.Generation <= 0 {
		return errors.New("artifact reference requires a digest and positive storage generation")
	}
	parsed, err := url.Parse(ref.URI)
	if err != nil || parsed.Scheme != "gs" || parsed.Host == "" || parsed.Path == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("artifact reference must be a credential-free GCS object URI")
	}
	_, _, err = parseGCSObjectURI(ref.URI)
	return err
}

func servingLegacyNamespace(kind ServingKind) string {
	return "router_serving/v1/" + string(kind) + "/sha256/"
}

// servingNamespaces lists every content-addressed layout a kind may be read from. The first
// entry is the layout new objects of that kind are published to.
func servingNamespaces(kind ServingKind) ([]string, error) {
	switch kind {
	case ServingReleases, ServingClassifiers, ServingBindings, ServingProfiles, ServingSelectionSets, ServingProposals:
		return []string{servingLegacyNamespace(kind)}, nil
	case ServingCandidate:
		return []string{servingArtifactsNamespace, servingLegacyNamespace(ServingReleases)}, nil
	case ServingSelectionSet:
		return []string{servingArtifactsNamespace, servingLegacyNamespace(ServingSelectionSets)}, nil
	case ServingProposal:
		return []string{servingArtifactsNamespace, servingLegacyNamespace(ServingProposals)}, nil
	default:
		return nil, fmt.Errorf("unsupported serving object kind %q", kind)
	}
}

// ValidateServingRef prevents cross-kind substitution and paths outside the registry. A v2 kind
// admits both the artifacts/ layout and the legacy namespace of the v1 object it folds.
func ValidateServingRef(ref ObjectRef, root string, kind ServingKind) error {
	_, err := servingRefNamespace(ref, root, kind)
	return err
}

// servingRefNamespace returns the layout a reference of the given kind lives in.
func servingRefNamespace(ref ObjectRef, root string, kind ServingKind) (string, error) {
	namespaces, err := servingNamespaces(kind)
	if err != nil {
		return "", err
	}
	if err := validateArtifactRef(ref); err != nil {
		return "", err
	}
	for _, namespace := range namespaces {
		if withinRegistry(ref.URI, root, namespace+ref.SHA256+".json") {
			return namespace, nil
		}
	}
	return "", errors.New("serving reference is outside its exact content-addressed namespace")
}

func validImageDigest(digest string) bool {
	return strings.HasPrefix(digest, "sha256:") && validDigest(strings.TrimPrefix(digest, "sha256:"))
}

// Validate rejects partial classifier identities, including unpinned auxiliary models.
func (b ClassifierBundle) Validate(string) error {
	if b.SchemaVersion != ServingClassifierV1 {
		return errors.New("invalid classifier bundle identity or schema")
	}
	return b.component().validate()
}

var sourceRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Validate enforces immutable release references and complete source provenance.
func (r ServingRelease) Validate(root string) error {
	if r.SchemaVersion != ServingReleaseV1 || !validImageDigest(r.RouterImageDigest) {
		return errors.New("invalid serving release schema or router image digest")
	}
	if err := validateServingPolicy(r.Policy, root); err != nil {
		return err
	}
	if err := ValidateServingRef(r.Classifier, root, ServingClassifiers); err != nil {
		return err
	}
	if err := r.Requirements.validate(); err != nil {
		return err
	}
	return r.Provenance.validate()
}

func validateProfileKey(key string) error {
	parsed, err := uuid.Parse(key)
	if err != nil || parsed == uuid.Nil || parsed.String() != key {
		return errors.New("profile key must be a canonical nonzero opaque UUID")
	}
	return nil
}

// Validate checks a profile revision independently of enrollment or authorization.
func (p RoutingProfile) Validate(root string) error {
	if p.SchemaVersion != ServingProfileV1 {
		return errors.New("unsupported routing profile schema")
	}
	if err := validateProfileKey(p.ProfileKey); err != nil {
		return err
	}
	if err := validateServingPolicy(p.Policy, root); err != nil {
		return err
	}
	return p.Requirements.validate()
}

func (r RevisionBinding) validate() error {
	audience, err := url.Parse(r.Audience)
	if err != nil || audience.Scheme != "https" || audience.Host == "" || audience.Path != "" || audience.RawQuery != "" || audience.Fragment != "" || audience.User != nil {
		return errors.New("binding requires an explicit credential-free IAM service audience")
	}
	parsed, err := url.Parse(r.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || strings.TrimSpace(r.Name) == "" || !validImageDigest(r.ImageDigest) {
		return errors.New("binding requires a named immutable HTTPS revision and image digest")
	}
	return validateArtifactRef(r.Configuration)
}

// Validate checks target-local realization; readiness separately verifies actual revision identity.
func (b DeploymentBinding) Validate(root string) error {
	if b.SchemaVersion != ServingBindingV1 || strings.TrimSpace(b.Project) == "" || strings.TrimSpace(b.Region) == "" || !validDigest(b.ClassifierBundleSHA256) {
		return errors.New("invalid deployment binding schema, location or classifier bundle")
	}
	if _, err := b.Target.Environment(); err != nil {
		return err
	}
	if err := ValidateServingRef(b.Release, root, ServingReleases); err != nil {
		return err
	}
	if err := b.Router.validate(); err != nil {
		return err
	}
	if err := b.Classifier.validate(); err != nil {
		return err
	}
	return validateArtifactRef(b.Attestation)
}

// validate accepts either a v1 tuple (release, binding, profile revision) or the normalized
// form of a v2 lane, whose binding and profile revision are both the selection set that embeds it.
func (s ServingSelection) validate(root string, profile bool) error {
	if profile != (s.Profile != nil) {
		return errors.New("only named profile selections must carry an exact profile revision")
	}
	if s.isLane(root) {
		if err := ValidateServingRef(s.Release, root, ServingCandidate); err != nil {
			return err
		}
		if err := ValidateServingRef(s.Binding, root, ServingSelectionSet); err != nil {
			return err
		}
		if s.Profile != nil && *s.Profile != s.Binding {
			return errors.New("lane profile revision must be the selection set that embeds the lane")
		}
		return nil
	}
	if err := ValidateServingRef(s.Release, root, ServingReleases); err != nil {
		return err
	}
	if err := ValidateServingRef(s.Binding, root, ServingBindings); err != nil {
		return err
	}
	if s.Profile != nil {
		return ValidateServingRef(*s.Profile, root, ServingProfiles)
	}
	return nil
}

// isLane reports whether the selection names a lane embedded in a v2 selection set.
func (s ServingSelection) isLane(root string) bool {
	return isServingArtifactURI(s.Binding.URI, root)
}

// Validate requires an explicit profile inventory, including for default-only activations.
func (s SelectionSet) Validate(root string) error {
	if s.SchemaVersion != ServingSelectionSetV1 || s.Profiles == nil {
		return errors.New("selection set requires a supported schema and explicit profile map")
	}
	if _, err := s.Target.Environment(); err != nil {
		return err
	}
	if err := s.Default.validate(root, false); err != nil {
		return err
	}
	for key, selection := range s.Profiles {
		if err := validateProfileKey(key); err != nil {
			return err
		}
		if err := selection.validate(root, true); err != nil {
			return fmt.Errorf("profile %q: %w", key, err)
		}
	}
	return nil
}

// Validate freezes actor, idempotency identity, scope and expected destination state.
func (p DeploymentProposal) Validate(root string) error {
	if p.SchemaVersion != ServingProposalV1 || p.ExpectedGeneration < 0 || strings.TrimSpace(p.Actor) == "" || strings.TrimSpace(p.Reason) == "" || p.CreatedAt.IsZero() {
		return errors.New("invalid proposal schema, generation or audit identity")
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
	if (p.ExpectedGeneration > 0) != (p.PreviousSelectionSet != nil) {
		return errors.New("proposal must bind the previous selection set unless bootstrapping")
	}
	if p.Scope == ChangeRollback && p.PreviousSelectionSet == nil {
		return errors.New("exact rollback requires an existing target activation")
	}
	if p.PreviousSelectionSet != nil {
		if err := ValidateServingRef(*p.PreviousSelectionSet, root, ServingSelectionSets); err != nil {
			return err
		}
	}
	if err := ValidateServingRef(p.SelectionSet, root, ServingSelectionSets); err != nil {
		return err
	}
	if err := ValidateServingRef(p.SourceRelease, root, ServingReleases); err != nil {
		return err
	}
	if err := validateProposalEvidence(p.Evidence); err != nil {
		return err
	}
	return validateWithdrawals(p.WithdrawActivations)
}

func validateProposalScope(scope ChangeScope, profileKey string) error {
	switch scope {
	case ChangeFull, ChangeRouter, ChangeRoster, ChangeClassifier, ChangeCustom, ChangeRollback:
		if profileKey != "" {
			return errors.New("only profile scope accepts a profile key")
		}
		return nil
	case ChangeProfile:
		return validateProfileKey(profileKey)
	default:
		return fmt.Errorf("unknown deployment scope %q", scope)
	}
}

func validateProposalEvidence(evidence []ObjectRef) error {
	if len(evidence) == 0 {
		return errors.New("proposal requires compatibility and target-local validation evidence")
	}
	for _, ref := range evidence {
		if err := validateArtifactRef(ref); err != nil {
			return err
		}
	}
	return nil
}

func validateWithdrawals(activationIDs []string) error {
	seen := make(map[string]struct{}, len(activationIDs))
	for _, activationID := range activationIDs {
		if _, err := uuid.Parse(activationID); err != nil {
			return errors.New("withdrawal requires exact activation UUIDs")
		}
		if _, exists := seen[activationID]; exists {
			return errors.New("duplicate activation withdrawal")
		}
		seen[activationID] = struct{}{}
	}
	return nil
}

// ServingManifest is the common strict validation boundary for immutable managed objects.
type ServingManifest interface {
	Validate(string) error
}

// DecodeServingManifest validates manifest bytes, whether supplied for publication or read
// back from the registry: strict decode, supported schema and semantic contract. The JSON
// encoding is not part of the contract; an object's identity is the digest of the exact
// bytes it was published with. A v1 kind decodes exactly its own type; a v2 kind selects the
// v1 or v2 type from the declared schema_version.
func DecodeServingManifest(payload []byte, root string, kind ServingKind) (ServingManifest, error) {
	var manifest ServingManifest
	switch kind {
	case ServingReleases:
		manifest = &ServingRelease{}
	case ServingClassifiers:
		manifest = &ClassifierBundle{}
	case ServingBindings:
		manifest = &DeploymentBinding{}
	case ServingProfiles:
		manifest = &RoutingProfile{}
	case ServingSelectionSets:
		manifest = &SelectionSet{}
	case ServingProposals:
		manifest = &DeploymentProposal{}
	case ServingCandidate, ServingSelectionSet, ServingProposal:
		schema, err := servingSchemaOf(payload)
		if err != nil {
			return nil, err
		}
		if manifest, err = servingFamilyManifest(kind, schema); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported serving kind %q", kind)
	}
	if err := strictDecode(payload, manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(root); err != nil {
		return nil, err
	}
	return manifest, nil
}

// DecodeServingObject decodes stored bytes for a reference and binds the object's version to
// its layout: artifacts/ holds only v2 objects and legacy namespaces only v1 objects, so a
// reference cannot be relabeled across layouts.
func DecodeServingObject(payload []byte, root string, kind ServingKind, ref ObjectRef) (ServingManifest, error) {
	if err := ValidateServingRef(ref, root, kind); err != nil {
		return nil, err
	}
	manifest, err := DecodeServingManifest(payload, root, kind)
	if err != nil {
		return nil, err
	}
	if isServingV2Manifest(manifest) != isServingArtifactURI(ref.URI, root) {
		return nil, errors.New("serving object schema does not belong to its storage layout")
	}
	return manifest, nil
}
