package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"weave-os/router/internal/policyregistry"
)

// Managed artifact inspection is available before the gateway rollout. Activation
// is intentionally not exposed until private-endpoint validation is wired.
func runServing(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: policyctl serving <validate|publish|status> [flags]")
	}
	command := commandName(args[0])
	if command != commandValidate && command != commandPublish && command != commandStatus {
		return fmt.Errorf("unsupported serving command %q", command)
	}
	flags := flag.NewFlagSet("serving "+string(command), flag.ContinueOnError)
	registryURI := flags.String("registry", defaultRegistryURI, "GCS registry root")
	kindRaw := flags.String("kind", "", "releases, classifiers, bindings, profiles, selection_sets or proposals")
	manifestPath := flags.String("manifest", "", "canonical manifest JSON file")
	targetRaw := flags.String("target", "", "staging, prod/stable or prod/weave-internal")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional serving arguments")
	}
	if command == commandStatus {
		target := policyregistry.ServingTarget(*targetRaw)
		if _, err := target.Environment(); err != nil {
			return err
		}
		registry, err := policyregistry.NewGCSRegistry(ctx, *registryURI)
		if err != nil {
			return err
		}
		defer registry.Close()
		snapshot, err := registry.ReadServingState(ctx, target)
		if err != nil {
			return err
		}
		return writeJSON(snapshot)
	}
	if *manifestPath == "" || *kindRaw == "" {
		return errors.New("serving manifest operation requires --kind and --manifest")
	}
	payload, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read serving manifest: %w", err)
	}
	payload = bytes.TrimSpace(payload)
	kind := policyregistry.ServingKind(*kindRaw)
	if _, err := policyregistry.DecodeServingManifest(payload, *registryURI, kind); err != nil {
		return err
	}
	if command == commandValidate {
		return writeJSON(struct {
			Kind   policyregistry.ServingKind `json:"kind"`
			SHA256 string                     `json:"sha256"`
		}{Kind: kind, SHA256: policyregistry.Digest(payload)})
	}
	registry, err := policyregistry.NewGCSRegistry(ctx, *registryURI)
	if err != nil {
		return err
	}
	defer registry.Close()
	reference, err := registry.PublishServingManifest(ctx, kind, payload)
	if err != nil {
		return err
	}
	return writeJSON(reference)
}
