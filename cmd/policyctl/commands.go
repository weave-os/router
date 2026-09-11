package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/hmm/policycompiler"
	"weave-os/router/internal/router/hmm/rosterdata"
)

type commandName string

const (
	commandCompile  commandName = "compile"
	commandValidate commandName = "validate"
	commandPublish  commandName = "publish"
	commandPromote  commandName = "promote"
	commandRollback commandName = "rollback"
	commandStatus   commandName = "status"
)

const defaultRegistryURI = "gs://weave_ml/weave_registry"

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: policyctl <compile|validate|publish|promote|rollback|status> [flags]")
	}
	switch commandName(args[0]) {
	case commandCompile:
		return runCompile(args[1:])
	case commandValidate:
		return runValidate(args[1:])
	case commandPublish:
		return runPublish(ctx, args[1:])
	case commandPromote:
		return runPromote(ctx, args[1:], false)
	case commandRollback:
		return runPromote(ctx, args[1:], true)
	case commandStatus:
		return runStatus(ctx, args[1:])
	default:
		return fmt.Errorf("unknown policyctl command %q", args[0])
	}
}

func runCompile(args []string) error {
	flags := flag.NewFlagSet(string(commandCompile), flag.ContinueOnError)
	sourcePath := flags.String("source", "", "reviewed roster source JSON")
	outputPath := flags.String("output", "", "canonical policy output JSON")
	classOrderRaw := flags.String("class-order", "", "comma-separated classifier class order")
	sourceRevision := flags.String("source-revision", "", "reviewed source revision")
	evidenceURI := flags.String("evidence-uri", "", "optional offline-evidence URI")
	evidenceSHA := flags.String("evidence-sha256", "", "optional offline-evidence digest")
	checkOnly := flags.Bool("check", false, "validate and print identity without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sourcePath == "" || (!*checkOnly && *outputPath == "") {
		return errors.New("compile requires --source and either --output or --check")
	}
	source, err := os.ReadFile(*sourcePath)
	if err != nil {
		return fmt.Errorf("read policy source: %w", err)
	}
	canonical, policy, err := policycompiler.Compile(source, policycompiler.Options{
		ClassOrder: parseList(*classOrderRaw), SourceRevision: *sourceRevision,
		EvidenceURI: *evidenceURI, EvidenceSHA256: *evidenceSHA,
	})
	if err != nil {
		return err
	}
	if !*checkOnly {
		if err := os.WriteFile(*outputPath, append(canonical, '\n'), 0o644); err != nil {
			return fmt.Errorf("write compiled policy: %w", err)
		}
	}
	return writeJSON(map[string]any{
		"schema_version": policy.SchemaVersion,
		"policy_sha256":  policyregistry.Digest(canonical),
		"class_order":    policy.ClassOrder,
		"arms":           len(policy.AllArms()),
		"output":         *outputPath,
	})
}

func runValidate(args []string) error {
	flags := flag.NewFlagSet(string(commandValidate), flag.ContinueOnError)
	policyPath := flags.String("policy", "", "compiled policy JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *policyPath == "" {
		return errors.New("validate requires --policy")
	}
	payload, err := os.ReadFile(*policyPath)
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	policy, err := rosterdata.ParseValidated(payload)
	if err != nil {
		return err
	}
	if policy.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		return fmt.Errorf("policy uses legacy schema %q", policy.SchemaVersion)
	}
	canonical, err := rosterdata.CanonicalBytes(policy)
	if err != nil {
		return err
	}
	return writeJSON(map[string]any{
		"schema_version": policy.SchemaVersion,
		"policy_sha256":  policyregistry.Digest(canonical),
		"class_order":    policy.ClassOrder,
		"clusters":       len(policy.Clusters),
		"arms":           len(policy.AllArms()),
	})
}

func runPublish(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(string(commandPublish), flag.ContinueOnError)
	registryURI := flags.String("registry", defaultRegistryURI, "GCS registry root")
	policyPath := flags.String("policy", "", "compiled policy JSON")
	classifierArtifactID := flags.String("classifier-artifact-id", "", "immutable classifier artifact ID")
	classifierPackageSHA := flags.String("classifier-package-sha256", "", "classifier package digest")
	classifierImageDigest := flags.String("classifier-image-digest", "", "classifier image sha256 digest")
	classifierTaxonomySHA := flags.String("classifier-taxonomy-sha256", "", "ordered classifier taxonomy digest")
	sourceRevision := flags.String("source-revision", "", "source revision")
	createdBy := flags.String("actor", defaultActor(), "publishing actor")
	evidenceURI := flags.String("evidence-uri", "", "optional evidence URI")
	evidenceSHA := flags.String("evidence-sha256", "", "optional evidence digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *policyPath == "" || *classifierArtifactID == "" || *classifierPackageSHA == "" || *classifierImageDigest == "" || *sourceRevision == "" {
		return errors.New("publish requires --policy, classifier identity flags, and --source-revision")
	}
	payload, err := os.ReadFile(*policyPath)
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	policy, err := rosterdata.ParseValidated(payload)
	if err != nil {
		return err
	}
	if policy.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		return fmt.Errorf("publish requires schema %q", rosterdata.SchemaVersionPolicyV1)
	}
	registry, err := policyregistry.NewGCSRegistry(ctx, *registryURI)
	if err != nil {
		return err
	}
	defer registry.Close()
	policyRef, err := registry.PublishPolicy(ctx, payload)
	if err != nil {
		return err
	}
	taxonomySHA := *classifierTaxonomySHA
	if taxonomySHA == "" {
		taxonomySHA = policyregistry.TaxonomyDigest(policy.ClassOrder)
	}
	release := policyregistry.Release{
		SchemaVersion: policyregistry.ReleaseSchemaV1,
		Classifier: policyregistry.ClassifierIdentity{
			ArtifactID: *classifierArtifactID, PackageSHA256: *classifierPackageSHA,
			ImageDigest: *classifierImageDigest, WireSchema: policyregistry.ClassifierWireSchemaV4,
			ClassOrder: append([]string(nil), policy.ClassOrder...), TaxonomySHA256: taxonomySHA,
		},
		Policy: policyregistry.PolicyObject{
			URI: policyRef.URI, SHA256: policyRef.SHA256,
			SchemaVersion: policy.SchemaVersion, Generation: policyRef.Generation,
		},
		Provenance: policyregistry.Provenance{
			SourceRevision: *sourceRevision, EvidenceURI: *evidenceURI,
			EvidenceSHA256: *evidenceSHA, CreatedBy: *createdBy, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	releaseRef, err := registry.PublishRelease(ctx, release)
	if err != nil {
		return err
	}
	return writeJSON(map[string]any{"policy": policyRef, "release": releaseRef, "release_id": releaseRef.SHA256})
}

func runPromote(ctx context.Context, args []string, rollback bool) error {
	command := commandPromote
	releaseFlag := "release"
	if rollback {
		command = commandRollback
		releaseFlag = "to-release"
	}
	flags := flag.NewFlagSet(string(command), flag.ContinueOnError)
	registryURI := flags.String("registry", defaultRegistryURI, "GCS registry root")
	environmentRaw := flags.String("environment", "", "staging-01 or prod-01")
	laneRaw := flags.String("lane", "", "stable or beta")
	releaseID := flags.String(releaseFlag, "", "immutable release digest")
	expectedGeneration := flags.Int64("expected-generation", -1, "current head generation, or 0 when creating")
	expectedRelease := flags.String("expect-current", "", "expected current release digest")
	classifierRevisionURL := flags.String("classifier-revision-url", "", "tagged classifier revision URL")
	classifierRevisionName := flags.String("classifier-revision-name", "", "tagged classifier revision name")
	reason := flags.String("reason", "", "human promotion reason")
	actor := flags.String("actor", defaultActor(), "promotion actor")
	prodApproved := flags.Bool("approved", false, "confirm protected production approval")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *environmentRaw == "" || *laneRaw == "" || *releaseID == "" || *expectedGeneration < 0 || *classifierRevisionURL == "" || *classifierRevisionName == "" || *reason == "" || *actor == "" {
		return fmt.Errorf("%s requires environment, lane, release, expected generation, classifier revision, actor, and reason", command)
	}
	environment := policyregistry.Environment(*environmentRaw)
	lane := policyregistry.Lane(*laneRaw)
	if environment == policyregistry.EnvironmentProd && !*prodApproved {
		return errors.New("production promotion requires --approved after protected-environment approval")
	}
	registry, err := policyregistry.NewGCSRegistry(ctx, *registryURI)
	if err != nil {
		return err
	}
	defer registry.Close()
	releaseRef, err := registry.ReleaseRef(ctx, *releaseID)
	if err != nil {
		return err
	}
	release, err := registry.ReadRelease(ctx, releaseRef)
	if err != nil {
		return err
	}
	if err := validateClassifierRevision(ctx, release, *classifierRevisionURL); err != nil {
		return err
	}
	var current policyregistry.HeadSnapshot
	if *expectedGeneration > 0 {
		current, err = registry.ReadHead(ctx, environment, lane)
		if err != nil {
			return err
		}
		if current.Generation != *expectedGeneration {
			return fmt.Errorf("lane head generation is %d, expected %d", current.Generation, *expectedGeneration)
		}
		if *expectedRelease == "" || current.Head.ReleaseSHA256 != *expectedRelease {
			return fmt.Errorf("lane head release is %q, expected %q", current.Head.ReleaseSHA256, *expectedRelease)
		}
	} else if *expectedRelease != "" {
		return errors.New("--expect-current must be empty when --expected-generation=0")
	}
	if err := validateClassifierRevisionURL(*classifierRevisionURL); err != nil {
		return err
	}
	head := policyregistry.LaneHead{
		SchemaVersion: policyregistry.LaneHeadSchemaV1, Environment: environment, Lane: lane,
		ReleaseURI: releaseRef.URI, ReleaseSHA256: releaseRef.SHA256, ReleaseGeneration: releaseRef.Generation,
		ClassifierRevisionURL: *classifierRevisionURL, ClassifierRevisionName: *classifierRevisionName,
		PromotedBy: *actor, PromotedAt: time.Now().UTC().Format(time.RFC3339), Reason: *reason,
	}
	if current.Generation > 0 {
		head.PreviousReleaseURI = current.Head.ReleaseURI
		head.PreviousReleaseSHA256 = current.Head.ReleaseSHA256
	}
	updated, err := registry.Promote(ctx, head, *expectedGeneration)
	if err != nil {
		return err
	}
	return writeJSON(updated)
}

func runStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(string(commandStatus), flag.ContinueOnError)
	registryURI := flags.String("registry", defaultRegistryURI, "GCS registry root")
	releaseID := flags.String("release", "", "optional immutable release digest")
	environmentRaw := flags.String("environment", "", "optional environment")
	laneRaw := flags.String("lane", "", "optional lane")
	if err := flags.Parse(args); err != nil {
		return err
	}
	registry, err := policyregistry.NewGCSRegistry(ctx, *registryURI)
	if err != nil {
		return err
	}
	defer registry.Close()
	if *releaseID != "" {
		releaseRef, err := registry.ReleaseRef(ctx, *releaseID)
		if err != nil {
			return err
		}
		release, err := registry.ReadRelease(ctx, releaseRef)
		if err != nil {
			return err
		}
		if _, err := registry.ReadPolicy(ctx, policyregistry.ObjectRef{
			URI: release.Policy.URI, SHA256: release.Policy.SHA256, Generation: release.Policy.Generation,
		}); err != nil {
			return err
		}
		return writeJSON(map[string]any{"release_id": releaseRef.SHA256, "release_ref": releaseRef, "release": release})
	}
	environments := []policyregistry.Environment{policyregistry.EnvironmentStaging, policyregistry.EnvironmentProd}
	lanes := []policyregistry.Lane{policyregistry.LaneStable, policyregistry.LaneBeta}
	if *environmentRaw != "" {
		environments = []policyregistry.Environment{policyregistry.Environment(*environmentRaw)}
	}
	if *laneRaw != "" {
		lanes = []policyregistry.Lane{policyregistry.Lane(*laneRaw)}
	}
	type facetStatus struct {
		Environment policyregistry.Environment   `json:"environment"`
		Lane        policyregistry.Lane          `json:"lane"`
		Head        *policyregistry.HeadSnapshot `json:"head,omitempty"`
		Release     *policyregistry.Release      `json:"release,omitempty"`
		Error       string                       `json:"error,omitempty"`
	}
	statuses := make([]facetStatus, 0, len(environments)*len(lanes))
	for _, environment := range environments {
		for _, lane := range lanes {
			status := facetStatus{Environment: environment, Lane: lane}
			head, readErr := registry.ReadHead(ctx, environment, lane)
			if readErr != nil {
				status.Error = readErr.Error()
				statuses = append(statuses, status)
				continue
			}
			status.Head = &head
			release, readErr := registry.ReadRelease(ctx, policyregistry.ObjectRef{URI: head.Head.ReleaseURI, SHA256: head.Head.ReleaseSHA256, Generation: head.Head.ReleaseGeneration})
			if readErr != nil {
				status.Error = readErr.Error()
			} else {
				status.Release = &release
			}
			statuses = append(statuses, status)
		}
	}
	return writeJSON(statuses)
}

func validateClassifierRevision(ctx context.Context, release policyregistry.Release, revisionURL string) error {
	if err := validateClassifierRevisionURL(revisionURL); err != nil {
		return err
	}
	client, err := policyclient.NewGoogleIDToken(revisionURL, 15*time.Second)
	if err != nil {
		return err
	}
	health, err := client.ReadClassifierHealth(ctx)
	if err != nil {
		return fmt.Errorf("probe classifier revision: %w", err)
	}
	expected := release.Classifier
	switch {
	case health.ClassifierArtifactID != expected.ArtifactID:
		return fmt.Errorf("classifier artifact %q does not match release %q", health.ClassifierArtifactID, expected.ArtifactID)
	case health.ClassifierSHA256 != expected.PackageSHA256:
		return fmt.Errorf("classifier package digest %q does not match release %q", health.ClassifierSHA256, expected.PackageSHA256)
	case health.ClassifierImageDigest != expected.ImageDigest:
		return fmt.Errorf("classifier image digest %q does not match release %q", health.ClassifierImageDigest, expected.ImageDigest)
	case health.SchemaVersion != expected.WireSchema:
		return fmt.Errorf("classifier schema %q does not match release %q", health.SchemaVersion, expected.WireSchema)
	case health.ClassifierTaxonomySHA != expected.TaxonomySHA256:
		return fmt.Errorf("classifier taxonomy digest %q does not match release %q", health.ClassifierTaxonomySHA, expected.TaxonomySHA256)
	case !slices.Equal(health.ClassifierClassOrder, expected.ClassOrder):
		return errors.New("classifier class order does not match release")
	}
	return nil
}

func validateClassifierRevisionURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return errors.New("classifier revision URL must be an absolute HTTPS base URL without path, query, fragment, or userinfo")
	}
	return nil
}

func parseList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func defaultActor() string {
	for _, key := range []string{"GITHUB_ACTOR", "USER"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "unknown"
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
