package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
)

func hmmPolicySnapshotBuilder(
	availableProviders map[string]struct{},
	strategies []router.Strategy,
	authMode string,
	timeout time.Duration,
	attemptTimeout time.Duration,
	taskRuntimes ...*taskDomainRuntime,
) policyregistry.Builder {
	return func(ctx context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		var taskRuntime *taskDomainRuntime
		if len(taskRuntimes) > 0 {
			taskRuntime = taskRuntimes[0]
		}
		taskResolver, domainEvidence, evidenceSHA, err := taskRuntime.bind(candidate)
		if err != nil {
			return nil, fmt.Errorf("bind task classifier: %w", err)
		}
		if candidate.ClassifierReleaseV5 != nil {
			return buildHMMPolicyRoutersV5(ctx, candidate, availableProviders, strategies, authMode, timeout, attemptTimeout, taskResolver, domainEvidence, evidenceSHA)
		}
		clientFactory := policyclient.NewGoogleIDToken
		if candidate.ClassifierAudience != "" {
			clientFactory = func(url string, timeout time.Duration, options ...policyclient.Option) (*policyclient.Client, error) {
				return policyclient.NewGoogleIDTokenForAudience(url, candidate.ClassifierAudience, timeout, options...)
			}
		}
		client, err := buildHMMPolicyClientWithGoogleIDTokenFactory(
			candidate.HeadSnapshot.Head.ClassifierRevisionURL,
			authMode,
			timeout,
			clientFactory,
			policyclient.WithAttemptTimeout(attemptTimeout),
			policyclient.WithTaskDomain(taskResolver),
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
		case !maps.Equal(health.ClassifierVariants, expectedClassifier.Variants):
			return nil, fmt.Errorf("alternate classifiers do not match release")
		}
		capabilities, err := client.Capabilities(ctx)
		if err != nil {
			return nil, fmt.Errorf("read classifier capabilities: %w", err)
		}
		if capabilities.SchemaVersion != policy.SchemaVersionV4 {
			return nil, fmt.Errorf("classifier capabilities schema %q is not %q", capabilities.SchemaVersion, policy.SchemaVersionV4)
		}
		// The sidecar reports these false because the v4 classifier never sees
		// preferences or a quality/price bias; the strategy still honors both
		// because Go's arm selector applies them after classification.
		capabilities.HonorsQualityPriceBias = true
		capabilities.HonorsPreferredModels = true
		capabilities.SupportsRoutingDistribution = true

		armSelector := selection.SelectorWithDomainEvidence(candidate.Policy, domainEvidence, evidenceSHA)
		routers := make(map[router.Strategy]router.Router, len(strategies))
		for _, strategy := range strategies {
			artifactID, artifactSHA := expectedClassifier.ArtifactID, expectedClassifier.PackageSHA256
			if router.IsBetaStrategy(strategy) {
				variant, found := expectedClassifier.Variants[strategy]
				if !found && !(strategy == router.StrategyHMMBeta && candidate.HeadSnapshot.Head.Lane == policyregistry.LaneBeta) {
					continue
				}
				if found {
					artifactID, artifactSHA = variant.ArtifactID, variant.PackageSHA256
				}
			}
			policyRouter := hmm.NewForStrategy(strategy, client, availableProviders)
			strategyPolicySHA := candidate.Release.Policy.SHA256
			strategyArmSelector := armSelector
			if strategyPolicy, found := candidate.StrategyPolicies[strategy]; found {
				strategyPolicySHA = strategyPolicy.Reference.SHA256
				strategyArmSelector = selection.SelectorWithDomainEvidence(strategyPolicy.Roster, domainEvidence, evidenceSHA)
			}
			strategyCapabilities := capabilities
			if strategy == router.StrategyHMMQwen {
				strategyCapabilities.ReportsOutcomes = false
				strategyCapabilities.ReportsFeedback = false
			}
			policyRouter.WithCapabilities(strategyCapabilities)
			policyRouter.WithClassifierIdentity(artifactID, artifactSHA)
			policyRouter.WithSelectionPolicyIdentity(
				candidate.HeadSnapshot.Head.ReleaseSHA256,
				strategyPolicySHA,
				candidate.HeadSnapshot.Generation,
			)
			policyRouter.WithArmSelector(strategyArmSelector)
			routers[strategy] = policyRouter
		}
		return routers, nil
	}
}

func buildHMMPolicyRoutersV5(
	ctx context.Context,
	candidate policyregistry.Candidate,
	availableProviders map[string]struct{},
	strategies []router.Strategy,
	authMode string,
	timeout time.Duration,
	attemptTimeout time.Duration,
	taskResolver taskdomain.Resolver,
	domainEvidence *selection.DomainEvidence,
	evidenceSHA string,
) (map[router.Strategy]router.Router, error) {
	classifierOptions := []policyclient.Option{policyclient.WithAttemptTimeout(attemptTimeout)}
	if taskResolver != nil {
		classifierOptions = append(classifierOptions, policyclient.WithTaskDomain(taskResolver))
	}
	if candidate.ClassifierWorkerURL == "" || candidate.ClassifierWorkerAudience == "" {
		return nil, errors.New("v5 classifier runtime binding has no worker URL or audience")
	}
	classifierClient, err := buildHMMPolicyClientWithGoogleIDTokenFactory(
		candidate.ClassifierWorkerURL,
		authMode,
		timeout,
		func(url string, timeout time.Duration, options ...policyclient.Option) (*policyclient.Client, error) {
			return policyclient.NewGoogleIDTokenForAudience(url, candidate.ClassifierWorkerAudience, timeout, options...)
		},
		classifierOptions...,
	)
	if err != nil {
		return nil, err
	}
	var client *policyclient.Client
	integrationURL := config.GetOr("ROUTER_HMM_INTEGRATION_URL", "")
	if integrationURL == "" {
		client = classifierClient
	} else {
		integrationOptions := []policyclient.Option{policyclient.WithAttemptTimeout(attemptTimeout)}
		if taskResolver != nil {
			integrationOptions = append(integrationOptions, policyclient.WithTaskDomain(taskResolver))
		}
		integrationClient, integrationErr := buildHMMPolicyClientWithGoogleIDTokenFactory(
			integrationURL, authMode, timeout, policyclient.NewGoogleIDToken,
			integrationOptions...,
		)
		if integrationErr != nil {
			return nil, integrationErr
		}
		client, err = policyclient.NewWithClassifierService(integrationClient, classifierClient)
		if err != nil {
			return nil, err
		}
	}
	release := candidate.ClassifierReleaseV5
	if release == nil {
		return nil, errors.New("v5 classifier release is required")
	}
	if !slices.Contains(candidate.SupportedAPIMajors, release.APIMajor) {
		return nil, errors.New("Router worker does not support the selected classifier API major")
	}
	capabilities, err := client.CapabilitiesForClassifierRelease(
		ctx, candidate.ClassifierSelectionID, candidate.ClassifierReleaseID,
	)
	if err != nil {
		return nil, fmt.Errorf("read v5 classifier capabilities: %w", err)
	}
	if capabilities.APIMajor != release.APIMajor || capabilities.WireSchema != release.WireSchema ||
		capabilities.TaxonomySHA256 != release.Taxonomy.SHA256 || !slices.Equal(capabilities.ClassOrder, release.Taxonomy.ClassOrder) ||
		!slices.Equal(capabilities.ClassifierVariants, candidate.ClassifierEnabledVariants) {
		return nil, errors.New("v5 classifier capabilities do not match admitted target release")
	}
	goCapabilities := policy.Capabilities{
		SchemaVersion:               capabilities.SchemaVersion,
		ReportsOutcomes:             capabilities.ReportsOutcomes,
		ReportsFeedback:             capabilities.ReportsFeedback,
		HonorsPreferredModels:       true,
		HonorsQualityPriceBias:      true,
		SupportsPreview:             capabilities.SupportsPreview,
		SupportsRoutingDistribution: true,
	}
	armSelector := selection.SelectorWithDomainEvidence(candidate.Policy, domainEvidence, evidenceSHA)
	routers := make(map[router.Strategy]router.Router, len(strategies))
	for _, strategy := range strategies {
		variantID, supportedStrategy := classifierVariantForStrategy(strategy)
		if !supportedStrategy || !slices.Contains(candidate.ClassifierEnabledVariants, variantID) {
			continue
		}
		variant, exists := release.Variants[variantID]
		if !exists {
			return nil, fmt.Errorf("classifier release is missing enabled variant %q", variantID)
		}
		policyRouter := hmm.NewForStrategy(strategy, client, availableProviders)
		strategyPolicySHA := candidate.Release.Policy.SHA256
		strategyArmSelector := armSelector
		if strategyPolicy, found := candidate.StrategyPolicies[strategy]; found {
			strategyPolicySHA = strategyPolicy.Reference.SHA256
			strategyArmSelector = selection.SelectorWithDomainEvidence(strategyPolicy.Roster, domainEvidence, evidenceSHA)
		}
		strategyCapabilities := goCapabilities
		if strategy == router.StrategyHMMQwen {
			strategyCapabilities.ReportsOutcomes = false
			strategyCapabilities.ReportsFeedback = false
		}
		policyRouter.WithCapabilities(strategyCapabilities)
		policyRouter.WithClassifierReleaseIdentity(
			candidate.ClassifierReleaseID,
			candidate.ClassifierSelectionID,
			release.Taxonomy.SHA256,
			variantID,
			variant.ArtifactID,
			variant.ArtifactSHA256,
		)
		policyRouter.WithSelectionPolicyIdentity(
			candidate.HeadSnapshot.Head.ReleaseSHA256,
			strategyPolicySHA,
			candidate.HeadSnapshot.Generation,
		)
		policyRouter.WithArmSelector(strategyArmSelector)
		routers[strategy] = policyRouter
	}
	return routers, nil
}

func classifierVariantForStrategy(strategy router.Strategy) (policy.ClassifierVariantID, bool) {
	switch strategy {
	case router.StrategyHMM, router.StrategyHMMEmbedding:
		return policy.ClassifierVariantPrimary, true
	case router.StrategyHMMBeta:
		return policy.ClassifierVariantHMMBeta, true
	case router.StrategyHMMQwen:
		return policy.ClassifierVariantQwen, true
	default:
		return "", false
	}
}
