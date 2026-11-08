package policyregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// ClassifierRuntimeBindingSchema tags the wire format of a release's environment dispatch binding; decoding rejects any other value.
type ClassifierRuntimeBindingSchema string

// ClassifierRuntimeBindingSchemaV1 is the only supported runtime binding schema.
const ClassifierRuntimeBindingSchemaV1 ClassifierRuntimeBindingSchema = "router_classifier_runtime_binding_v1"

const (
	classifierRuntimeBindingsNamespace = "runtime_bindings/"
	classifierWorkerService            = "router-classifier-worker"
	cloudRunHostSuffix                 = ".a.run.app"
)

var (
	classifierWorkerProjects = map[Environment]string{
		EnvironmentStaging: "workweave-staging-01",
		EnvironmentProd:    "workweave-prod-01",
	}
	imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ClassifierRuntimeBinding pins one immutable classifier release to the tagged
// worker revision that serves it in one environment. The release publisher owns
// the object; the Router only reads it.
type ClassifierRuntimeBinding struct {
	SchemaVersion       ClassifierRuntimeBindingSchema `json:"schema_version"`
	Environment         Environment                    `json:"environment"`
	ProjectID           string                         `json:"project_id"`
	ReleaseSHA256       string                         `json:"release_sha256"`
	WorkerRevision      string                         `json:"worker_revision"`
	WorkerURL           string                         `json:"worker_url"`
	WorkerAudience      string                         `json:"worker_audience"`
	ImageDigest         string                         `json:"image_digest"`
	ConfigurationSHA256 string                         `json:"configuration_sha256"`
	// Variants holds worker-private dispatch targets (e.g. Qwen); the Router never calls them.
	Variants json.RawMessage `json:"variants,omitempty"`
}

// ResolvedClassifierRuntimeBinding is a runtime binding and the exact bytes it was read from.
type ResolvedClassifierRuntimeBinding struct {
	Ref     ObjectRef
	Binding ClassifierRuntimeBinding
}

// ClassifierRuntimeBindingURI is the publisher-owned location of a release's binding in one environment.
func ClassifierRuntimeBindingURI(root string, environment Environment, releaseSHA256 string) string {
	return strings.TrimSuffix(root, "/") + "/" + classifierRuntimeBindingRelativeName(environment, releaseSHA256)
}

func classifierRuntimeBindingRelativeName(environment Environment, releaseSHA256 string) string {
	return fmt.Sprintf("%s%s/classifier_releases/%s.json", classifierRuntimeBindingsNamespace, environment, releaseSHA256)
}

// DecodeClassifierRuntimeBinding strictly decodes a binding and requires it to
// name the expected environment and release with canonical worker origins.
func DecodeClassifierRuntimeBinding(payload []byte, environment Environment, releaseSHA256 string) (ClassifierRuntimeBinding, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var binding ClassifierRuntimeBinding
	if err := decoder.Decode(&binding); err != nil {
		return ClassifierRuntimeBinding{}, fmt.Errorf("decode classifier runtime binding: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ClassifierRuntimeBinding{}, errors.New("classifier runtime binding contains trailing JSON")
	}
	if err := binding.validate(environment, releaseSHA256); err != nil {
		return ClassifierRuntimeBinding{}, err
	}
	return binding, nil
}

func (b ClassifierRuntimeBinding) validate(environment Environment, releaseSHA256 string) error {
	if b.SchemaVersion != ClassifierRuntimeBindingSchemaV1 {
		return fmt.Errorf("unsupported classifier runtime binding schema %q", b.SchemaVersion)
	}
	project, supported := classifierWorkerProjects[environment]
	if !supported || b.Environment != environment || b.ProjectID != project {
		return errors.New("classifier runtime binding names another environment or project")
	}
	if !validDigest(releaseSHA256) || b.ReleaseSHA256 != releaseSHA256 {
		return errors.New("classifier runtime binding names another release")
	}
	if b.WorkerRevision == "" || len(b.WorkerRevision) > 256 || strings.ContainsFunc(b.WorkerRevision, unicode.IsSpace) {
		return errors.New("classifier runtime binding worker revision is not canonical")
	}
	if !imageDigestPattern.MatchString(b.ImageDigest) || !validDigest(b.ConfigurationSHA256) {
		return errors.New("classifier runtime binding image or configuration digest is invalid")
	}
	if !canonicalClassifierWorkerOrigin(b.WorkerURL) || !canonicalClassifierWorkerOrigin(b.WorkerAudience) {
		return errors.New("classifier runtime binding worker URL and audience must be canonical Cloud Run origins of the classifier worker")
	}
	var variants map[string]json.RawMessage
	if len(b.Variants) > 0 && json.Unmarshal(b.Variants, &variants) != nil {
		return errors.New("classifier runtime binding variants must be an object")
	}
	return nil
}

// canonicalClassifierWorkerOrigin accepts only the exact "https://<host>" form of
// the environment's classifier worker service or one of its tagged revision hosts.
func canonicalClassifierWorkerOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Opaque != "" {
		return false
	}
	host := parsed.Hostname()
	if raw != "https://"+host || host != strings.ToLower(host) || !strings.HasSuffix(host, cloudRunHostSuffix) {
		return false
	}
	return host == classifierWorkerService+cloudRunHostSuffix || classifierWorkerHostPattern.MatchString(host)
}

// classifierWorkerHostPattern is "[<tag>---]router-classifier-worker-<hash>-<region>.a.run.app";
// the hash segment excludes dashes so a longer service name cannot pass as the worker.
var classifierWorkerHostPattern = regexp.MustCompile(`^(?:[a-z0-9]+(?:-[a-z0-9]+)*---)?` + regexp.QuoteMeta(classifierWorkerService) + `-[a-z0-9]+-[a-z]{2,4}` + regexp.QuoteMeta(cloudRunHostSuffix) + `$`)

// ReadClassifierRuntimeBinding reads the current generation of a release's
// environment binding and records the exact digest and generation it served.
func (r *Registry) ReadClassifierRuntimeBinding(ctx context.Context, environment Environment, releaseSHA256 string) (ResolvedClassifierRuntimeBinding, error) {
	if !validDigest(releaseSHA256) {
		return ResolvedClassifierRuntimeBinding{}, errors.New("classifier runtime binding requires a release digest")
	}
	if _, supported := classifierWorkerProjects[environment]; !supported {
		return ResolvedClassifierRuntimeBinding{}, fmt.Errorf("unsupported classifier runtime environment %q", environment)
	}
	name := r.prefix + "/" + classifierRuntimeBindingRelativeName(environment, releaseSHA256)
	attrs, err := r.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return ResolvedClassifierRuntimeBinding{}, classifyStorageError("read classifier runtime binding attributes", err)
	}
	payload, err := r.readObject(ctx, name, attrs.Generation)
	if err != nil {
		return ResolvedClassifierRuntimeBinding{}, err
	}
	binding, err := DecodeClassifierRuntimeBinding(payload, environment, releaseSHA256)
	if err != nil {
		return ResolvedClassifierRuntimeBinding{}, err
	}
	return ResolvedClassifierRuntimeBinding{
		Ref:     ObjectRef{URI: ClassifierRuntimeBindingURI(r.rootURI, environment, releaseSHA256), SHA256: Digest(payload), Generation: attrs.Generation},
		Binding: binding,
	}, nil
}
