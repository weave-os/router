package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
)

func hmmPolicySnapshotBuilder(
	availableProviders map[string]struct{},
	strategies []router.Strategy,
	authMode string,
	timeout time.Duration,
	attemptTimeout time.Duration,
) policyregistry.Builder {
	return func(ctx context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		client, err := buildHMMPolicyClient(
			candidate.HeadSnapshot.Head.ClassifierRevisionURL,
			authMode,
			timeout,
			policyclient.WithAttemptTimeout(attemptTimeout),
		)
		if err != nil {
			return nil, err
		}
		health, err := client.ReadClassifierHealth(ctx)
		if err != nil {
			return nil, err
		}
		expectedClassifier := candidate.Release.Classifier
		switch {
		case health.ClassifierArtifactID != expectedClassifier.ArtifactID:
			return nil, fmt.Errorf("classifier artifact %q does not match release %q", health.ClassifierArtifactID, expectedClassifier.ArtifactID)
		case health.ClassifierSHA256 != expectedClassifier.PackageSHA256:
			return nil, fmt.Errorf("classifier package digest %q does not match release %q", health.ClassifierSHA256, expectedClassifier.PackageSHA256)
		case health.ClassifierImageDigest != expectedClassifier.ImageDigest:
			return nil, fmt.Errorf("classifier image digest %q does not match release %q", health.ClassifierImageDigest, expectedClassifier.ImageDigest)
		case health.SchemaVersion != expectedClassifier.WireSchema:
			return nil, fmt.Errorf("classifier schema %q does not match release %q", health.SchemaVersion, expectedClassifier.WireSchema)
		case health.ClassifierTaxonomySHA != expectedClassifier.TaxonomySHA256:
			return nil, fmt.Errorf("classifier taxonomy digest %q does not match release %q", health.ClassifierTaxonomySHA, expectedClassifier.TaxonomySHA256)
		case !slices.Equal(health.ClassifierClassOrder, expectedClassifier.ClassOrder):
			return nil, fmt.Errorf("classifier class order does not match release")
		}
		capabilities, err := client.Capabilities(ctx)
		if err != nil {
			return nil, fmt.Errorf("read classifier capabilities: %w", err)
		}
		if capabilities.SchemaVersion != policy.SchemaVersionV4 {
			return nil, fmt.Errorf("classifier capabilities schema %q is not %q", capabilities.SchemaVersion, policy.SchemaVersionV4)
		}
		capabilities.HonorsQualityPriceBias = true
		capabilities.HonorsPreferredModels = true
		capabilities.SupportsRoutingDistribution = true

		armSelector := selection.Selector(candidate.Policy)
		routers := make(map[router.Strategy]router.Router, len(strategies))
		for _, strategy := range strategies {
			policyRouter := hmm.NewForStrategy(strategy, client, availableProviders)
			policyRouter.WithCapabilities(capabilities)
			policyRouter.WithClassifierIdentity(expectedClassifier.ArtifactID, expectedClassifier.PackageSHA256)
			policyRouter.WithSelectionPolicyIdentity(
				candidate.HeadSnapshot.Head.ReleaseSHA256,
				candidate.Release.Policy.SHA256,
				candidate.HeadSnapshot.Generation,
			)
			policyRouter.WithArmSelector(armSelector)
			routers[strategy] = policyRouter
		}
		return routers, nil
	}
}
