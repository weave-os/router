package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/config"
	"weave-os/router/internal/gcsregistry"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	servingpostgres "weave-os/router/internal/postgres/serving"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
)

const (
	envDefaultStrategy   = "ROUTER_DEFAULT_STRATEGY"
	hmmAttemptTimeoutEnv = "ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS"
)

var managedServingStrategies = []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding}

var managedServingEnvVars = []string{
	"ROUTER_SERVING_TARGET",
	"ROUTER_SERVING_PROJECT",
	"ROUTER_SERVING_REGION",
	"ROUTER_SERVING_REVISION",
	"ROUTER_SERVING_IMAGE_DIGEST",
	"ROUTER_SERVING_REGISTRY_URI",
	"ROUTER_SERVING_CONFIGURATION_URI",
	"ROUTER_SERVING_CONFIGURATION_SHA256",
	"ROUTER_SERVING_CONFIGURATION_GENERATION",
	"ROUTER_SERVING_SELECTION_SET_URI",
	"ROUTER_SERVING_SELECTION_SET_SHA256",
	"ROUTER_SERVING_SELECTION_SET_GENERATION",
	"ROUTER_SERVING_ASSERTION_KEY",
}

func buildAdmissionDecisions(pool *pgxpool.Pool) (*policyregistry.AdmissionDecisionCache, error) {
	if !servingTargetConfigured() {
		return nil, nil
	}
	target := policyregistry.ServingTarget(config.MustGet("ROUTER_SERVING_TARGET"))
	environment, err := target.Environment()
	if err != nil {
		return nil, err
	}
	admissions, err := servingpostgres.NewServingAdmissionRepo(pool, environment)
	if err != nil {
		return nil, err
	}
	channel, _ := target.Channel()
	scoped, err := admissions.ForWorker(target, channel)
	if err != nil {
		return nil, err
	}
	return policyregistry.NewAdmissionDecisionCache(scoped, 10000, 30*time.Second, time.Now)
}

func servingTargetConfigured() bool {
	return strings.TrimSpace(config.GetOr("ROUTER_SERVING_TARGET", "")) != ""
}

func validateManagedServingBoot(mode server.DeploymentMode, lookup func(string) (string, bool)) error {
	if mode != server.DeploymentModeManaged {
		return nil
	}
	for _, name := range localDevPolicyReleaseEnvVars {
		if value, present := lookup(name); present && strings.TrimSpace(value) != "" {
			return fmt.Errorf("%s is a local-dev-only policy release reader; managed workers serve from ROUTER_SERVING_TARGET", name)
		}
	}
	if target, present := lookup("ROUTER_SERVING_TARGET"); present {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("ROUTER_SERVING_TARGET is empty")
		}
		if _, err := policyregistry.ServingTarget(target).Environment(); err != nil {
			return err
		}
		return validateManagedServingDefaultStrategy(lookup)
	}
	for _, name := range managedServingEnvVars {
		if _, present := lookup(name); present {
			return fmt.Errorf("%s requires ROUTER_SERVING_TARGET", name)
		}
	}
	return nil
}

func validateManagedServingDefaultStrategy(lookup func(string) (string, bool)) error {
	raw, _ := lookup(envDefaultStrategy)
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf(
			"%s is empty while managed serving is enabled; set one of %s",
			envDefaultStrategy, strategyList(managedServingStrategies),
		)
	}
	strategy, err := server.ParseDefaultStrategy(raw)
	if err != nil {
		return err
	}
	if !slices.Contains(managedServingStrategies, strategy) {
		return fmt.Errorf(
			"%s=%q is not a managed serving strategy; set one of %s",
			envDefaultStrategy, strategy, strategyList(managedServingStrategies),
		)
	}
	return nil
}

func strategyList(strategies []router.Strategy) string {
	names := make([]string, 0, len(strategies))
	for _, strategy := range strategies {
		names = append(names, string(strategy))
	}
	return strings.Join(names, ", ")
}

func osEnvLookup(key string) (string, bool) { return os.LookupEnv(key) }

func buildManagedServingRuntime(ctx context.Context, mode server.DeploymentMode, availableProviders map[string]struct{}, taskRuntimes ...*taskDomainRuntime) (*middleware.ServingAdmissionConfig, *policyregistry.Snapshot, func(), error) {
	identity := policyregistry.WorkerIdentity{
		Target:  policyregistry.ServingTarget(config.MustGet("ROUTER_SERVING_TARGET")),
		Project: config.MustGet("ROUTER_SERVING_PROJECT"), Region: config.MustGet("ROUTER_SERVING_REGION"),
		Revision:      config.GetOr("ROUTER_SERVING_REVISION", config.GetOr("K_REVISION", "")),
		ImageDigest:   config.MustGet("ROUTER_SERVING_IMAGE_DIGEST"),
		Configuration: servingReferenceFromEnv("ROUTER_SERVING_CONFIGURATION"),
	}
	if err := identity.Validate(); err != nil {
		return nil, nil, nil, err
	}
	registryURI := strings.TrimSpace(config.GetOr("ROUTER_SERVING_REGISTRY_URI", config.GetOr("WEAVE_REGISTRY_URI", "gs://weave_ml/weave_registry")))
	registry, closeRegistry, err := openServingStore(ctx, mode, registryURI)
	if err != nil {
		return nil, nil, nil, err
	}
	timeout := parseEnvDurationMs("ROUTER_HMM_SIDECAR_TIMEOUT_MS", policyclient.DefaultTimeout)
	attemptTimeout := parseEnvAttemptTimeoutMs(policyclient.DeriveAttemptTimeout(timeout))
	cache, err := policyregistry.NewServingRuntimeCache(registry, hmmPolicySnapshotBuilder(
		availableProviders, append(slices.Clone(managedServingStrategies), router.StrategyHMMBeta, router.StrategyHMMQwen),
		config.GetOr("ROUTER_HMM_SIDECAR_AUTH", policySidecarAuthGoogleIDToken), timeout, attemptTimeout,
		taskRuntimes...,
	))
	if err != nil {
		closeRegistry()
		return nil, nil, nil, err
	}
	baseline, err := cache.PrepareWorker(ctx, identity, servingReferenceFromEnv("ROUTER_SERVING_SELECTION_SET"))
	if err != nil {
		closeRegistry()
		return nil, nil, nil, err
	}
	return &middleware.ServingAdmissionConfig{Store: registry, Identity: identity, Cache: cache, StableBaseURL: strings.TrimSpace(config.GetOr("ROUTER_STABLE_BASE_URL", "")), BetaBaseURL: strings.TrimSpace(config.GetOr("ROUTER_BETA_BASE_URL", ""))}, baseline, closeRegistry, nil
}

func openServingStore(ctx context.Context, mode server.DeploymentMode, registryURI string) (policyregistry.ServingStore, func(), error) {
	if !strings.HasPrefix(registryURI, "gs://") {
		switch {
		case strings.EqualFold(strings.TrimSpace(config.GetOr("ROUTER_ALLOW_LOCAL_SERVING_REGISTRY", "false")), "true"):
			slog.Warn("Serving from a local directory registry; ROUTER_ALLOW_LOCAL_SERVING_REGISTRY must only be set for a local stack", "registry_uri", registryURI)
		case mode == server.DeploymentModeManaged:
			return nil, nil, fmt.Errorf("managed workers require a gs:// serving registry, got %q (set ROUTER_ALLOW_LOCAL_SERVING_REGISTRY=true only for a local stack)", registryURI)
		}
		store, err := policyregistry.NewDirectoryServingStore(registryURI)
		return store, func() {}, err
	}
	registry, err := gcsregistry.NewGCSRegistry(ctx, registryURI)
	if err != nil {
		return nil, nil, err
	}
	return registry, func() { _ = registry.Close() }, nil
}

func servingReferenceFromEnv(prefix string) policyregistry.ObjectRef {
	return policyregistry.ObjectRef{
		URI: config.MustGet(prefix + "_URI"), SHA256: config.MustGet(prefix + "_SHA256"),
		Generation: int64(parseEnvInt(prefix+"_GENERATION", 0)),
	}
}

var localDevPolicyReleaseEnvVars = []string{"ROUTER_POLICY_ENVIRONMENT", "ROUTER_POLICY_RELEASE_DIR"}
