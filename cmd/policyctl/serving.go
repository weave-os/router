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
	"time"

	"google.golang.org/api/idtoken"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/servingvalidate"
)

const commandApply commandName = "apply"

// removedServingVerbs maps each retired verb to the invocation that absorbed it.
var removedServingVerbs = map[commandName]string{
	"validate": "publish --dry-run",
	"resolve":  "apply --proposal-sha256 <sha256>",
	"prepare":  "apply --dry-run",
	"activate": "apply",
}

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
	getenv       func(string) string
}

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
		writeOutput: writeJSON, clock: time.Now, logger: logger, getenv: os.Getenv,
	})
}

func workflowActorFor(getenv func(string) string, proposal policyregistry.ProposalView) string {
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
		return errors.New("usage: policyctl serving <publish|apply|status|rollback> [flags]")
	}
	command := commandName(args[0])
	if replacement, removed := removedServingVerbs[command]; removed {
		return fmt.Errorf("serving %s was removed; use `policyctl serving %s`", command, replacement)
	}
	flags := flag.NewFlagSet("serving "+string(command), flag.ContinueOnError)
	registryURI := flags.String("registry", defaultRegistryURI, "GCS registry root")
	var kindRaw, manifestPath, targetRaw, proposalPath, proposalDigest *string
	var dryRun *bool
	switch command {
	case commandPublish:
		kindRaw = flags.String("kind", "", "candidate, selection_set or proposal")
		manifestPath = flags.String("manifest", "", "immutable manifest JSON file")
		dryRun = flags.Bool("dry-run", false, "decode, validate and report the digest without writing to the registry")
	case commandApply:
		proposalPath = flags.String("proposal", "", "JSON file containing the exact published proposal ObjectRef")
		proposalDigest = flags.String("proposal-sha256", "", "resolve this immutable proposal digest instead of reading an ObjectRef file")
		dryRun = flags.Bool("dry-run", false, "validate the proposal and its destinations without activating")
	case commandRollback:
		proposalPath = flags.String("proposal", "", "JSON file containing the exact published rollback proposal ObjectRef")
	case commandStatus:
		targetRaw = flags.String("target", "", "staging, prod/stable or prod/weave-internal")
		proposalPath = flags.String("proposal", "", "JSON file containing the exact published proposal ObjectRef")
	default:
		return fmt.Errorf("unsupported serving command %q", command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional serving arguments")
	}
	switch command {
	case commandPublish:
		return servingPublish(ctx, dependencies, *registryURI, policyregistry.ServingKind(*kindRaw), *manifestPath, *dryRun)
	case commandStatus:
		if (*targetRaw == "") == (*proposalPath == "") {
			return errors.New("serving status requires exactly one of --target or --proposal")
		}
		if *targetRaw != "" {
			return servingTargetStatus(ctx, dependencies, *registryURI, policyregistry.ServingTarget(*targetRaw))
		}
		var ref policyregistry.ObjectRef
		if err := readServingReference(*proposalPath, &ref); err != nil {
			return err
		}
		if err := policyregistry.ValidateServingRef(ref, *registryURI, policyregistry.ServingProposal); err != nil {
			return err
		}
		registry, err := dependencies.openRegistry(ctx, *registryURI)
		if err != nil {
			return err
		}
		defer registry.Close()
		return servingProposalStatus(ctx, registry, ref, dependencies.writeOutput)
	case commandApply:
		if (*proposalPath == "") == (*proposalDigest == "") {
			return errors.New("serving apply requires exactly one of --proposal with an exact immutable proposal reference or --proposal-sha256")
		}
		return servingApply(ctx, dependencies, *registryURI, *proposalPath, *proposalDigest, *dryRun, false)
	default:
		if *proposalPath == "" {
			return errors.New("serving rollback requires --proposal with an exact immutable proposal reference")
		}
		return servingApply(ctx, dependencies, *registryURI, *proposalPath, "", false, true)
	}
}

// servingPublish digests the manifest bytes exactly as they would be stored; a dry run performs
// every pre-write check and reports that digest without opening the registry.
func servingPublish(ctx context.Context, dependencies servingDependencies, root string, kind policyregistry.ServingKind, path string, dryRun bool) error {
	if path == "" || kind == "" {
		return errors.New("serving publish requires --kind and --manifest")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read serving manifest: %w", err)
	}
	payload = bytes.TrimSpace(payload)
	if _, err := policyregistry.DecodePublishableServingManifest(payload, root, kind); err != nil {
		return err
	}
	if dryRun {
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

func servingTargetStatus(ctx context.Context, dependencies servingDependencies, root string, target policyregistry.ServingTarget) error {
	if _, err := target.Environment(); err != nil {
		return err
	}
	registry, err := dependencies.openRegistry(ctx, root)
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

// servingApply drives one proposal through the controller. A dry run stops after destination
// validation; rollback additionally requires the proposal to declare the rollback scope so the
// operator's stated intent and the proposal's transition cannot disagree.
func servingApply(ctx context.Context, dependencies servingDependencies, root, proposalPath, proposalDigest string, dryRun, rollback bool) error {
	var proposalRef policyregistry.ObjectRef
	if proposalPath != "" {
		if err := readServingReference(proposalPath, &proposalRef); err != nil {
			return err
		}
		if err := policyregistry.ValidateServingRef(proposalRef, root, policyregistry.ServingProposal); err != nil {
			return err
		}
	}
	endpoints, err := dependencies.endpoints()
	if err != nil {
		return err
	}
	registry, err := dependencies.openRegistry(ctx, root)
	if err != nil {
		return err
	}
	defer registry.Close()
	if proposalPath == "" {
		if proposalRef, err = registry.ServingRef(ctx, policyregistry.ServingProposal, proposalDigest); err != nil {
			return err
		}
	}
	controller, err := policyregistry.NewServingController(registry, policyregistry.DestinationValidator{Endpoints: endpoints}, dependencies.clock, dependencies.logger)
	if err != nil {
		return err
	}
	if dryRun {
		preparation, err := controller.Prepare(ctx, proposalRef)
		if err != nil {
			return err
		}
		return dependencies.writeOutput(preparation)
	}
	manifest, _, err := registry.ReadServingObject(ctx, policyregistry.ServingProposal, proposalRef)
	if err != nil {
		return err
	}
	typed, ok := manifest.(policyregistry.ProposalManifest)
	if !ok {
		return errors.New("registry returned the wrong manifest kind for the proposal")
	}
	proposal := typed.View()
	activate := controller.Activate
	if rollback {
		if proposal.Scope != policyregistry.ChangeRollback {
			return fmt.Errorf("serving rollback requires a proposal with scope %q, got %q; use `policyctl serving apply` for forward changes", policyregistry.ChangeRollback, proposal.Scope)
		}
		activate = controller.Rollback
	}
	getenv := dependencies.getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	activation, err := activate(ctx, proposalRef, workflowActorFor(getenv, proposal))
	if err != nil {
		return err
	}
	if err := dependencies.writeOutput(activation); err != nil {
		return fmt.Errorf("activated; output observation degraded; reconcile the same proposal without creating another activation: %w", err)
	}
	return nil
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
	manifest, payload, err := registry.ReadServingObject(ctx, policyregistry.ServingProposal, ref)
	if err != nil {
		return err
	}
	if policyregistry.Digest(payload) != ref.SHA256 {
		return errors.New("proposal digest mismatch; status is keyed on the recorded proposal reference")
	}
	proposal, ok := manifest.(policyregistry.ProposalManifest)
	if !ok {
		return errors.New("registry returned the wrong proposal manifest kind")
	}
	snapshot, err := registry.ReadServingState(ctx, proposal.View().Target)
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
