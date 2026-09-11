// Package policyregistry owns the immutable HMM release and mutable lane-head
// contracts used by the router control plane.
package policyregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"weave-os/router/internal/router/hmm/rosterdata"
)

// ReleaseSchemaV1 is the first atomic classifier-and-selection-policy release.
const ReleaseSchemaV1 = "hmm_go_policy_release_v1"

// LaneHeadSchemaV1 is the first generation-CAS lane-head contract.
const LaneHeadSchemaV1 = "router_policy_lane_head_v1"

// ClassifierWireSchemaV4 is the classification-facts-only sidecar contract.
const ClassifierWireSchemaV4 = "policy_router_v4"

// Environment identifies one runtime deployment environment.
type Environment string

const (
	EnvironmentStaging Environment = "staging-01"
	EnvironmentProd    Environment = "prod-01"
)

// Lane identifies an independently promoted policy lane.
type Lane string

const (
	LaneStable Lane = "stable"
	LaneBeta   Lane = "beta"
)

// ClassifierIdentity binds a release to immutable classifier bytes and taxonomy.
type ClassifierIdentity struct {
	ArtifactID     string   `json:"artifact_id"`
	PackageSHA256  string   `json:"package_sha256"`
	ImageDigest    string   `json:"image_digest"`
	WireSchema     string   `json:"wire_schema"`
	ClassOrder     []string `json:"class_order"`
	TaxonomySHA256 string   `json:"taxonomy_sha256"`
}

// PolicyObject identifies immutable Go selection-policy bytes.
type PolicyObject struct {
	URI           string                   `json:"uri"`
	SHA256        string                   `json:"sha256"`
	SchemaVersion rosterdata.SchemaVersion `json:"schema_version"`
	Generation    int64                    `json:"generation"`
}

// Provenance records where a release and its optional offline evidence came from.
type Provenance struct {
	SourceRevision string `json:"source_revision"`
	EvidenceURI    string `json:"evidence_uri,omitempty"`
	EvidenceSHA256 string `json:"evidence_sha256,omitempty"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      string `json:"created_at"`
}

// Release atomically names the classifier and Go selection policy promoted together.
type Release struct {
	SchemaVersion string             `json:"schema_version"`
	Classifier    ClassifierIdentity `json:"classifier"`
	Policy        PolicyObject       `json:"selection_policy"`
	Provenance    Provenance         `json:"provenance"`
}

// LaneHead is the only mutable HMM activation object.
type LaneHead struct {
	SchemaVersion          string      `json:"schema_version"`
	Environment            Environment `json:"environment"`
	Lane                   Lane        `json:"lane"`
	ReleaseURI             string      `json:"release_uri"`
	ReleaseSHA256          string      `json:"release_sha256"`
	ReleaseGeneration      int64       `json:"release_generation"`
	ClassifierRevisionURL  string      `json:"classifier_revision_url"`
	ClassifierRevisionName string      `json:"classifier_revision_name"`
	PreviousReleaseURI     string      `json:"previous_release_uri,omitempty"`
	PreviousReleaseSHA256  string      `json:"previous_release_sha256,omitempty"`
	PromotedBy             string      `json:"promoted_by"`
	PromotedAt             string      `json:"promoted_at"`
	Reason                 string      `json:"reason"`
}

// ObjectRef identifies exact immutable bytes in GCS.
type ObjectRef struct {
	URI        string `json:"uri"`
	SHA256     string `json:"sha256"`
	Generation int64  `json:"generation"`
}

// HeadSnapshot couples decoded lane-head bytes to their GCS concurrency token.
type HeadSnapshot struct {
	Head       LaneHead `json:"head"`
	Generation int64    `json:"generation"`
}

// ReleaseID returns the content-derived lowercase SHA-256 identity.
func ReleaseID(release Release) (string, error) {
	canonical, err := CanonicalBytes(release)
	if err != nil {
		return "", err
	}
	return Digest(canonical), nil
}

// CanonicalBytes encodes a contract as stable compact JSON with no trailing newline.
func CanonicalBytes(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical policy JSON: %w", err)
	}
	return payload, nil
}

// Digest returns a lowercase hex SHA-256 digest.
func Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// DecodeRelease strictly decodes and validates an immutable release.
func DecodeRelease(payload []byte, registryRoot string) (Release, error) {
	var release Release
	if err := strictDecode(payload, &release); err != nil {
		return Release{}, fmt.Errorf("decode router policy release: %w", err)
	}
	if err := release.Validate(registryRoot); err != nil {
		return Release{}, err
	}
	canonical, err := CanonicalBytes(release)
	if err != nil {
		return Release{}, err
	}
	if !bytes.Equal(canonical, payload) {
		return Release{}, errors.New("router policy release is not canonical JSON")
	}
	return release, nil
}

// DecodeLaneHead strictly decodes and validates a mutable lane head.
func DecodeLaneHead(payload []byte, registryRoot string, environment Environment, lane Lane) (LaneHead, error) {
	var head LaneHead
	if err := strictDecode(payload, &head); err != nil {
		return LaneHead{}, fmt.Errorf("decode router policy lane head: %w", err)
	}
	if err := head.Validate(registryRoot, environment, lane); err != nil {
		return LaneHead{}, err
	}
	return head, nil
}

// Validate enforces the immutable release contract.
func (r Release) Validate(registryRoot string) error {
	if r.SchemaVersion != ReleaseSchemaV1 {
		return fmt.Errorf("unsupported release schema_version %q", r.SchemaVersion)
	}
	if strings.TrimSpace(r.Classifier.ArtifactID) == "" || !validDigest(r.Classifier.PackageSHA256) {
		return errors.New("release classifier artifact_id and package_sha256 are required")
	}
	if !strings.HasPrefix(r.Classifier.ImageDigest, "sha256:") || !validDigest(strings.TrimPrefix(r.Classifier.ImageDigest, "sha256:")) {
		return errors.New("release classifier image_digest must be sha256:<digest>")
	}
	if r.Classifier.WireSchema != ClassifierWireSchemaV4 || len(r.Classifier.ClassOrder) == 0 || !validDigest(r.Classifier.TaxonomySHA256) {
		return errors.New("release classifier wire schema, class order, and taxonomy digest are required")
	}
	if TaxonomyDigest(r.Classifier.ClassOrder) != r.Classifier.TaxonomySHA256 {
		return errors.New("release classifier taxonomy digest does not match class_order")
	}
	if r.Policy.SchemaVersion != rosterdata.SchemaVersionPolicyV1 || !validDigest(r.Policy.SHA256) || r.Policy.Generation <= 0 {
		return errors.New("release selection policy schema, digest, and generation are invalid")
	}
	if !withinRegistry(r.Policy.URI, registryRoot, "router_policy/v1/policies/sha256/"+r.Policy.SHA256+".json") {
		return errors.New("release selection policy URI is outside its content-addressed registry path")
	}
	if strings.TrimSpace(r.Provenance.SourceRevision) == "" || strings.TrimSpace(r.Provenance.CreatedBy) == "" {
		return errors.New("release source_revision and created_by are required")
	}
	if _, err := time.Parse(time.RFC3339, r.Provenance.CreatedAt); err != nil {
		return fmt.Errorf("release created_at must be RFC3339: %w", err)
	}
	if (r.Provenance.EvidenceURI == "") != (r.Provenance.EvidenceSHA256 == "") {
		return errors.New("release evidence URI and digest must be supplied together")
	}
	if r.Provenance.EvidenceSHA256 != "" && !validDigest(r.Provenance.EvidenceSHA256) {
		return errors.New("release evidence_sha256 is invalid")
	}
	return nil
}

// Validate enforces facet identity and immutable release binding.
func (h LaneHead) Validate(registryRoot string, environment Environment, lane Lane) error {
	if h.SchemaVersion != LaneHeadSchemaV1 {
		return fmt.Errorf("unsupported lane-head schema_version %q", h.SchemaVersion)
	}
	if err := ValidateEnvironment(environment); err != nil {
		return err
	}
	if err := ValidateLane(lane); err != nil {
		return err
	}
	if h.Environment != environment || h.Lane != lane {
		return fmt.Errorf("lane head facet %s/%s does not match requested %s/%s", h.Environment, h.Lane, environment, lane)
	}
	if !validDigest(h.ReleaseSHA256) || h.ReleaseGeneration <= 0 {
		return errors.New("lane head release digest and generation are invalid")
	}
	if !withinRegistry(h.ReleaseURI, registryRoot, "router_policy/v1/releases/sha256/"+h.ReleaseSHA256+".json") {
		return errors.New("lane head release URI is outside its content-addressed registry path")
	}
	if (h.PreviousReleaseURI == "") != (h.PreviousReleaseSHA256 == "") {
		return errors.New("lane head previous release URI and digest must be supplied together")
	}
	if h.PreviousReleaseURI != "" && (!validDigest(h.PreviousReleaseSHA256) || !withinRegistry(h.PreviousReleaseURI, registryRoot, "router_policy/v1/releases/sha256/"+h.PreviousReleaseSHA256+".json")) {
		return errors.New("lane head previous release binding is invalid")
	}
	parsedRevisionURL, err := url.Parse(h.ClassifierRevisionURL)
	if err != nil || parsedRevisionURL.Scheme != "https" || parsedRevisionURL.Host == "" || parsedRevisionURL.Path != "" || parsedRevisionURL.RawQuery != "" || parsedRevisionURL.Fragment != "" || parsedRevisionURL.User != nil || strings.TrimSpace(h.ClassifierRevisionName) == "" {
		return errors.New("lane head classifier revision must use a named immutable HTTPS revision")
	}
	if strings.TrimSpace(h.PromotedBy) == "" || strings.TrimSpace(h.Reason) == "" {
		return errors.New("lane head promotion actor and reason are required")
	}
	if _, err := time.Parse(time.RFC3339, h.PromotedAt); err != nil {
		return fmt.Errorf("lane head promoted_at must be RFC3339: %w", err)
	}
	return nil
}

// ValidateEnvironment rejects unrecognized serving environments.
func ValidateEnvironment(environment Environment) error {
	switch environment {
	case EnvironmentStaging, EnvironmentProd:
		return nil
	default:
		return fmt.Errorf("unsupported router policy environment %q", environment)
	}
}

// ValidateLane rejects unrecognized promotion lanes.
func ValidateLane(lane Lane) error {
	switch lane {
	case LaneStable, LaneBeta:
		return nil
	default:
		return fmt.Errorf("unsupported router policy lane %q", lane)
	}
}

// TaxonomyDigest returns the classifier's canonical ordered-label digest.
func TaxonomyDigest(classOrder []string) string {
	payload, _ := json.Marshal(classOrder)
	return Digest(payload)
}

func strictDecode(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func withinRegistry(candidate, registryRoot, suffix string) bool {
	root := strings.TrimRight(registryRoot, "/")
	return candidate == root+"/"+suffix
}
