package policyregistry

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
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

func servingNamespace(kind ServingKind) (string, error) {
	switch kind {
	case ServingReleases, ServingClassifiers, ServingBindings, ServingProfiles, ServingSelectionSets, ServingProposals:
		return "router_serving/v1/" + string(kind) + "/sha256/", nil
	default:
		return "", fmt.Errorf("unsupported serving object kind %q", kind)
	}
}

// ValidateServingRef prevents cross-kind substitution and paths outside the registry.
func ValidateServingRef(ref ObjectRef, root string, kind ServingKind) error {
	namespace, err := servingNamespace(kind)
	if err != nil {
		return err
	}
	if err := validateArtifactRef(ref); err != nil {
		return err
	}
	if !withinRegistry(ref.URI, root, namespace+ref.SHA256+".json") {
		return errors.New("serving reference is outside its exact content-addressed namespace")
	}
	return nil
}

func validImageDigest(digest string) bool {
	return strings.HasPrefix(digest, "sha256:") && validDigest(strings.TrimPrefix(digest, "sha256:"))
}

// Validate rejects partial classifier identities, including unpinned auxiliary models.
func (b ClassifierBundle) Validate(string) error {
	identity := b.Identity
	if b.SchemaVersion != ServingClassifierV1 || strings.TrimSpace(identity.ArtifactID) == "" || !validImageDigest(identity.ImageDigest) || identity.WireSchema != ClassifierWireSchemaV4 || len(identity.ClassOrder) == 0 || TaxonomyDigest(identity.ClassOrder) != identity.TaxonomySHA256 {
		return errors.New("invalid classifier bundle identity or schema")
	}
	if err := validateArtifactRef(b.Package); err != nil {
		return err
	}
	if b.Package.SHA256 != identity.PackageSHA256 {
		return errors.New("classifier package reference does not match attested identity")
	}
	if b.AuxiliaryModels == nil {
		return errors.New("classifier auxiliary model inventory is required, even when empty")
	}
	for name, ref := range b.AuxiliaryModels {
		if strings.TrimSpace(name) == "" {
			return errors.New("auxiliary model name is required")
		}
		if err := validateArtifactRef(ref); err != nil {
			return fmt.Errorf("auxiliary model %q: %w", name, err)
		}
	}
	return validateArtifactRef(b.Configuration)
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
	if !sourceRevisionPattern.MatchString(r.Provenance.RouterRevision) || !sourceRevisionPattern.MatchString(r.Provenance.WeaveRevision) {
		return errors.New("serving provenance requires exact router and Weave source revisions")
	}
	return validateArtifactRef(r.Provenance.BuildAttestation)
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

func (s ServingSelection) validate(root string, profile bool) error {
	if err := ValidateServingRef(s.Release, root, ServingReleases); err != nil {
		return err
	}
	if err := ValidateServingRef(s.Binding, root, ServingBindings); err != nil {
		return err
	}
	if profile != (s.Profile != nil) {
		return errors.New("only named profile selections must carry an exact profile revision")
	}
	if s.Profile != nil {
		return ValidateServingRef(*s.Profile, root, ServingProfiles)
	}
	return nil
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
	if _, err := uuid.Parse(p.RequestID); err != nil || p.RequestID == uuid.Nil.String() {
		return errors.New("proposal request ID must be a nonzero UUID")
	}
	if _, err := p.Target.Environment(); err != nil {
		return err
	}
	switch p.Scope {
	case ChangeFull, ChangeRouter, ChangeRoster, ChangeClassifier, ChangeCustom, ChangeRollback:
		if p.ProfileKey != "" {
			return errors.New("only profile scope accepts a profile key")
		}
	case ChangeProfile:
		if err := validateProfileKey(p.ProfileKey); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown deployment scope %q", p.Scope)
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
	if len(p.Evidence) == 0 {
		return errors.New("proposal requires compatibility and target-local validation evidence")
	}
	for _, ref := range p.Evidence {
		if err := validateArtifactRef(ref); err != nil {
			return err
		}
	}
	seen := make(map[string]struct{}, len(p.WithdrawActivations))
	for _, activationID := range p.WithdrawActivations {
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

// DecodeServingManifest validates caller-supplied manifest bytes before publication:
// strict decode, supported schema, semantic contract, and exact canonical encoding.
func DecodeServingManifest(payload []byte, root string, kind ServingKind) (ServingManifest, error) {
	return decodeServingManifest(payload, root, kind, true)
}

// DecodeStoredServingManifest validates manifest bytes fetched from the registry: strict
// decode and the semantic contract, without requiring the stored encoding to match this
// binary's canonical form. Byte integrity comes from the verified reference digest and
// generation; canonical form can drift when contract structs evolve between binary versions.
func DecodeStoredServingManifest(payload []byte, root string, kind ServingKind) (ServingManifest, error) {
	return decodeServingManifest(payload, root, kind, false)
}

func decodeServingManifest(payload []byte, root string, kind ServingKind, requireCanonical bool) (ServingManifest, error) {
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
	default:
		return nil, fmt.Errorf("unsupported serving kind %q", kind)
	}
	if err := strictDecode(payload, manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(root); err != nil {
		return nil, err
	}
	canonical, err := CanonicalBytes(manifest)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, payload) {
		if requireCanonical {
			return nil, errors.New("serving manifest is not canonical JSON")
		}
		logStorageDrift(string(kind), payload)
	}
	return manifest, nil
}

// logStorageDrift reports stored registry bytes that decode and validate but no longer
// re-encode to this binary's canonical form. Reads tolerate the drift; a republish of the
// object rewrites canonical bytes.
func logStorageDrift(object string, payload []byte) {
	slog.Warn("Stored registry object is valid but not canonical JSON; encoding drifted since publish", "object", object, "sha256", Digest(payload))
}
