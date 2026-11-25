package main

import (
	"context"
	"errors"
	"flag"

	"weave-os/router/internal/gcsregistry"
	"weave-os/router/internal/policyregistry"
)

const (
	commandExportSelfHosted   commandName = "export-selfhosted"
	defaultServingRegistryURI             = defaultRegistryURI + "/router/serving"
)

type selfHostedExportSourceOpener func(context.Context, string) (policyregistry.SelfHostedExportStore, func(), error)

func openGCSSelfHostedExportSource(ctx context.Context, root string) (policyregistry.SelfHostedExportStore, func(), error) {
	registry, err := gcsregistry.NewGCSRegistry(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	return registry, func() { _ = registry.Close() }, nil
}

func runServingExportSelfHosted(ctx context.Context, args []string, openSource selfHostedExportSourceOpener, writeOutput func(any) error) error {
	flags := flag.NewFlagSet("serving "+string(commandExportSelfHosted), flag.ContinueOnError)
	registryURI := flags.String("registry", defaultServingRegistryURI, "GCS serving registry root, read only")
	targetRaw := flags.String("target", "", "managed target to export, for example prod/stable")
	activationID := flags.String("activation", "", "activation to export; defaults to the target's current activation")
	outDir := flags.String("out", "", "empty directory that receives the self-hosted serving registry")
	routerImageDigest := flags.String("router-image-digest", "", "router image digest the self-hosted worker runs; must equal the exported candidate's")
	classifierURL := flags.String("classifier-url", "", "classifier origin the self-hosted router calls, for example http://classifier:8080")
	classifierImageDigest := flags.String("classifier-image-digest", "", "classifier image the customer runs; the release is re-bound only when it differs from the released runtime image")
	workerProject := flags.String("worker-project", policyregistry.SelfHostedServingProject, "self-hosted worker project label")
	workerRegion := flags.String("worker-region", policyregistry.SelfHostedServingRegion, "self-hosted worker region label")
	workerRevision := flags.String("worker-revision", policyregistry.SelfHostedServingRevision, "self-hosted worker revision label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional serving arguments")
	}
	if *targetRaw == "" || *outDir == "" || *routerImageDigest == "" || *classifierURL == "" {
		return errors.New("serving export-selfhosted requires --target, --out, --router-image-digest and --classifier-url")
	}
	target := policyregistry.ServingTarget(*targetRaw)
	source, closeSource, err := openSource(ctx, *registryURI)
	if err != nil {
		return err
	}
	defer closeSource()
	exported, err := policyregistry.ExportSelfHostedServingGraph(ctx, source, policyregistry.SelfHostedExportRequest{
		Target:       target,
		ActivationID: *activationID,
		Worker: policyregistry.WorkerIdentity{
			Target: target, Project: *workerProject, Region: *workerRegion, Revision: *workerRevision, ImageDigest: *routerImageDigest,
		},
		ClassifierURL:         *classifierURL,
		ClassifierImageDigest: *classifierImageDigest,
		Dir:                   *outDir,
	})
	if err != nil {
		return err
	}
	return writeOutput(exported)
}
