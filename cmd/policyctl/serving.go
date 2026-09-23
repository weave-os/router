package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"google.golang.org/api/idtoken"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/servingvalidate"
)

const (
	commandResolve  commandName = "resolve"
	commandPrepare  commandName = "prepare"
	commandActivate commandName = "activate"
)

type servingRegistry interface {
	policyregistry.ServingValidationStore
	PublishServingManifest(context.Context, policyregistry.ServingKind, []byte) (policyregistry.ObjectRef, error)
	ServingRef(context.Context, policyregistry.ServingKind, string) (policyregistry.ObjectRef, error)
	Close() error
}

type servingDependencies struct {
	openRegistry func(context.Context, string) (servingRegistry, error)
	endpoints    func() (policyregistry.DestinationEndpoints, error)
	writeOutput  func(any) error
	clock        func() time.Time
	logger       *slog.Logger
	stderr       io.Writer
	getenv       func(string) string
}

// deprecatedServingFlags are parsed and ignored so existing workflow invocations keep working.
var deprecatedServingFlags = []string{"approved-proposal", "workflow-actor", "validation-origin"}

func runServing(ctx context.Context, args []string) (runErr error) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if len(args) > 0 {
		logger = logger.With("command", args[0])
	}
	defer func() {
		if runErr != nil {
			logger.Error("Serving command failed", "err", runErr)
		}
	}()
	return runServingWith(ctx, args, servingDependencies{
		openRegistry: func(ctx context.Context, root string) (servingRegistry, error) {
			return policyregistry.NewGCSRegistry(ctx, root)
		},
		endpoints: func() (policyregistry.DestinationEndpoints, error) {
			return servingvalidate.New(&http.Client{}, func(ctx context.Context, audience string) (string, error) {
				source, err := idtoken.NewTokenSource(ctx, audience)
				if err != nil {
					return "", err
				}
				token, err := source.Token()
				if err != nil {
					return "", err
				}
				return token.AccessToken, nil
			})
		},
		writeOutput: writeJSON, clock: time.Now, logger: logger, stderr: os.Stderr, getenv: os.Getenv,
	})
}

func workflowActorFor(getenv func(string) string, proposal policyregistry.DeploymentProposal) string {
	if actor := getenv("WORKFLOW_ACTOR"); actor != "" {
		return actor
	}
	if actor, run := getenv("GITHUB_ACTOR"), getenv("GITHUB_RUN_ID"); actor != "" && run != "" {
		return actor + "@run:" + run
	}
	if user := getenv("USER"); user != "" {
		return user
	}
	return proposal.Actor
}

func runServingWith(ctx context.Context, args []string, dependencies servingDependencies) error {
	if len(args) == 0 {
		return errors.New("usage: policyctl serving <validate|publish|resolve|prepare|activate|rollback|status> [flags]")
	}
	command := commandName(args[0])
	switch command {
	case commandValidate, commandPublish, commandResolve, commandPrepare, commandActivate, commandRollback, commandStatus:
	default:
		return fmt.Errorf("unsupported serving command %q", command)
	}
	flags := flag.NewFlagSet("serving "+string(command), flag.ContinueOnError)
	registryURI := flags.String("registry", defaultRegistryURI, "GCS registry root")
	kindRaw := flags.String("kind", "", "releases, classifiers, bindings, profiles, selection_sets or proposals")
	manifestPath := flags.String("manifest", "", "immutable manifest JSON file")
	targetRaw := flags.String("target", "", "staging, prod/stable or prod/weave-internal")
	proposalPath := flags.String("proposal", "", "JSON file containing the exact published proposal ObjectRef")
	proposalDigest := flags.String("proposal-sha256", "", "resolve this immutable proposal digest once before activation")
	stored := flags.Bool("stored", false, "validate manifest bytes exactly as fetched from the registry, without trimming surrounding whitespace")
	for _, name := range deprecatedServingFlags {
		flags.Func(name, "deprecated; parsed and ignored", func(string) error { return nil })
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if dependencies.stderr != nil {
		flags.Visit(func(f *flag.Flag) {
			if slices.Contains(deprecatedServingFlags, f.Name) {
				fmt.Fprintf(dependencies.stderr, "warning: --%s is deprecated and ignored; it will be removed in a future release\n", f.Name)
			}
		})
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional serving arguments")
	}
	if *targetRaw != "" && (command != commandStatus || *proposalPath != "") {
		return errors.New("--target is only accepted for target status; lifecycle destinations are bound by the immutable proposal")
	}
	if command == commandValidate || command == commandPublish {
		if *stored && command == commandPublish {
			return errors.New("--stored only applies to serving validate; publish digests the trimmed manifest bytes")
		}
		return servingManifestOperation(ctx, dependencies, command, *registryURI, policyregistry.ServingKind(*kindRaw), *manifestPath, *stored)
	}
	if command == commandResolve {
		if *proposalDigest == "" {
			return errors.New("serving resolve requires --proposal-sha256")
		}
		registry, err := dependencies.openRegistry(ctx, *registryURI)
		if err != nil {
			return err
		}
		defer registry.Close()
		ref, err := registry.ServingRef(ctx, policyregistry.ServingProposals, *proposalDigest)
		if err != nil {
			return err
		}
		if _, _, err := registry.ReadServingObject(ctx, policyregistry.ServingProposals, ref); err != nil {
			return err
		}
		return dependencies.writeOutput(ref)
	}
	if command == commandStatus && *proposalPath == "" {
		target := policyregistry.ServingTarget(*targetRaw)
		if _, err := target.Environment(); err != nil {
			return err
		}
		registry, err := dependencies.openRegistry(ctx, *registryURI)
		if err != nil {
			return err
		}
		defer registry.Close()
		snapshot, err := registry.ReadServingState(ctx, target)
		if err != nil {
			return err
		}
		return dependencies.writeOutput(snapshot)
	}
	if *proposalPath == "" {
		return errors.New("serving lifecycle operation requires --proposal with an exact immutable proposal reference")
	}
	var proposalRef policyregistry.ObjectRef
	if err := readServingReference(*proposalPath, &proposalRef); err != nil {
		return err
	}
	if err := policyregistry.ValidateServingRef(proposalRef, *registryURI, policyregistry.ServingProposals); err != nil {
		return err
	}
	var validator policyregistry.DestinationValidator
	if command != commandStatus {
		endpoints, err := dependencies.endpoints()
		if err != nil {
			return err
		}
		validator.Endpoints = endpoints
	}
	registry, err := dependencies.openRegistry(ctx, *registryURI)
	if err != nil {
		return err
	}
	defer registry.Close()
	if command == commandStatus {
		return servingProposalStatus(ctx, registry, proposalRef, dependencies.writeOutput)
	}
	controller, err := policyregistry.NewServingController(registry, validator, dependencies.clock, dependencies.logger)
	if err != nil {
		return err
	}
	if command == commandPrepare {
		preparation, err := controller.Prepare(ctx, proposalRef)
		if err != nil {
			return err
		}
		return dependencies.writeOutput(preparation)
	}
	proposal, _, err := registry.ReadServingObject(ctx, policyregistry.ServingProposals, proposalRef)
	if err != nil {
		return err
	}
	typed, ok := proposal.(*policyregistry.DeploymentProposal)
	if !ok {
		return errors.New("registry returned the wrong manifest kind for the proposal")
	}
	getenv := dependencies.getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	activate := controller.Activate
	if command == commandRollback {
		activate = controller.Rollback
	}
	activation, err := activate(ctx, proposalRef, workflowActorFor(getenv, *typed))
	if err != nil {
		return err
	}
	if err := dependencies.writeOutput(activation); err != nil {
		return fmt.Errorf("activated; output observation degraded; reconcile the same proposal without creating another activation: %w", err)
	}
	return nil
}

func servingManifestOperation(ctx context.Context, dependencies servingDependencies, command commandName, root string, kind policyregistry.ServingKind, path string, stored bool) error {
	if path == "" || kind == "" {
		return errors.New("serving manifest operation requires --kind and --manifest")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read serving manifest: %w", err)
	}
	if !stored {
		payload = bytes.TrimSpace(payload)
	}
	if _, err := policyregistry.DecodeServingManifest(payload, root, kind); err != nil {
		return err
	}
	if command == commandValidate {
		return dependencies.writeOutput(struct {
			Kind   policyregistry.ServingKind `json:"kind"`
			SHA256 string                     `json:"sha256"`
		}{kind, policyregistry.Digest(payload)})
	}
	registry, err := dependencies.openRegistry(ctx, root)
	if err != nil {
		return err
	}
	defer registry.Close()
	reference, err := registry.PublishServingManifest(ctx, kind, payload)
	if err != nil {
		return err
	}
	return dependencies.writeOutput(reference)
}

func readServingReference(path string, reference *policyregistry.ObjectRef) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(payload) > 64<<10 {
		return errors.New("proposal reference exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(reference); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("proposal reference contains trailing JSON")
	}
	return nil
}

func servingProposalStatus(ctx context.Context, registry servingRegistry, ref policyregistry.ObjectRef, writeOutput func(any) error) error {
	manifest, payload, err := registry.ReadServingObject(ctx, policyregistry.ServingProposals, ref)
	if err != nil {
		return err
	}
	if policyregistry.Digest(payload) != ref.SHA256 {
		return errors.New("proposal digest mismatch; status is keyed on the recorded proposal reference")
	}
	proposal, ok := manifest.(*policyregistry.DeploymentProposal)
	if !ok {
		return errors.New("registry returned the wrong proposal manifest kind")
	}
	snapshot, err := registry.ReadServingState(ctx, proposal.Target)
	if err != nil && !errors.Is(err, policyregistry.ErrNotFound) {
		return err
	}
	if errors.Is(err, policyregistry.ErrNotFound) {
		snapshot = policyregistry.ServingStateSnapshot{}
	}
	for _, activation := range snapshot.State.Activations {
		if activation.Proposal != ref {
			continue
		}
		outcome := policyregistry.ActivationCurrent
		if activation.ID != snapshot.State.CurrentActivationID {
			outcome = policyregistry.ActivationSuperseded
		}
		return writeOutput(policyregistry.ActivationResult{Snapshot: snapshot, Activation: activation, Outcome: outcome, Replayed: true})
	}
	return writeOutput(struct {
		Proposal   policyregistry.ObjectRef `json:"proposal"`
		Activated  bool                     `json:"activated"`
		Generation int64                    `json:"generation"`
	}{ref, false, snapshot.Generation})
}
