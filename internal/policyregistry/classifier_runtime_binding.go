package policyregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

type ClassifierRuntimeBindingSchema string

const ClassifierRuntimeBindingSchemaV1 ClassifierRuntimeBindingSchema = "router_classifier_runtime_binding_v1"

const (
	classifierRuntimeBindingsNamespace = "runtime_bindings/"
	classifierWorkerService            = "router-classifier-worker"
	cloudRunHostSuffix                 = ".a.run.app"
)

var (
	ClassifierWorkerProjects = map[Environment]string{
		EnvironmentStaging: "workweave-staging-01",
		EnvironmentProd:    "workweave-prod-01",
	}
	imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

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
	Variants            json.RawMessage                `json:"variants,omitempty"`
}

type ResolvedClassifierRuntimeBinding struct {
	Ref     ObjectRef
	Binding ClassifierRuntimeBinding
}

func ClassifierRuntimeBindingURI(root string, environment Environment, releaseSHA256 string) string {
	return strings.TrimSuffix(root, "/") + "/" + ClassifierRuntimeBindingRelativeName(environment, releaseSHA256)
}

func ClassifierRuntimeBindingRelativeName(environment Environment, releaseSHA256 string) string {
	return fmt.Sprintf("%s%s/classifier_releases/%s.json", classifierRuntimeBindingsNamespace, environment, releaseSHA256)
}

func DecodeClassifierRuntimeBinding(payload []byte, environment Environment, releaseSHA256 string) (ClassifierRuntimeBinding, error) {
	binding, err := DecodeLocalClassifierRuntimeBinding(payload, environment, releaseSHA256)
	if err != nil {
		return ClassifierRuntimeBinding{}, err
	}
	if err := binding.validateManagedWorker(environment); err != nil {
		return ClassifierRuntimeBinding{}, err
	}
	return binding, nil
}

func DecodeLocalClassifierRuntimeBinding(payload []byte, environment Environment, releaseSHA256 string) (ClassifierRuntimeBinding, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var binding ClassifierRuntimeBinding
	if err := decoder.Decode(&binding); err != nil {
		return ClassifierRuntimeBinding{}, fmt.Errorf("decode classifier runtime binding: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ClassifierRuntimeBinding{}, errors.New("classifier runtime binding contains trailing JSON")
	}
	if err := binding.validateIdentity(environment, releaseSHA256); err != nil {
		return ClassifierRuntimeBinding{}, err
	}
	return binding, nil
}

func (b ClassifierRuntimeBinding) validateIdentity(environment Environment, releaseSHA256 string) error {
	if b.SchemaVersion != ClassifierRuntimeBindingSchemaV1 {
		return fmt.Errorf("unsupported classifier runtime binding schema %q", b.SchemaVersion)
	}
	if b.Environment != environment {
		return errors.New("classifier runtime binding names another environment or project")
	}
	if !ValidDigest(releaseSHA256) || b.ReleaseSHA256 != releaseSHA256 {
		return errors.New("classifier runtime binding names another release")
	}
	if b.WorkerRevision == "" || len(b.WorkerRevision) > 256 || strings.ContainsFunc(b.WorkerRevision, unicode.IsSpace) {
		return errors.New("classifier runtime binding worker revision is not canonical")
	}
	if !imageDigestPattern.MatchString(b.ImageDigest) || !ValidDigest(b.ConfigurationSHA256) {
		return errors.New("classifier runtime binding image or configuration digest is invalid")
	}
	var variants map[string]json.RawMessage
	if len(b.Variants) > 0 && json.Unmarshal(b.Variants, &variants) != nil {
		return errors.New("classifier runtime binding variants must be an object")
	}
	return nil
}

func (b ClassifierRuntimeBinding) validateManagedWorker(environment Environment) error {
	project, supported := ClassifierWorkerProjects[environment]
	if !supported || b.ProjectID != project {
		return errors.New("classifier runtime binding names another environment or project")
	}
	if !canonicalClassifierWorkerOrigin(b.WorkerURL) || !canonicalClassifierWorkerOrigin(b.WorkerAudience) {
		return errors.New("classifier runtime binding worker URL and audience must be canonical Cloud Run origins of the classifier worker")
	}
	return nil
}

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

var classifierWorkerHostPattern = regexp.MustCompile(`^(?:[a-z0-9]+(?:-[a-z0-9]+)*---)?` + regexp.QuoteMeta(classifierWorkerService) + `-[a-z0-9]+-[a-z]{2,4}` + regexp.QuoteMeta(cloudRunHostSuffix) + `$`)
