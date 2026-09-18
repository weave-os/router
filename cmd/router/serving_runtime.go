package main

import (
	"context"
	"strings"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"
)

func managedServingEnabled() bool {
	return strings.TrimSpace(config.GetOr("ROUTER_SERVING_ASSERTION_KEY", "")) != ""
}

func buildManagedServingRuntime(ctx context.Context, availableProviders map[string]struct{}) (*middleware.ServingAdmissionConfig, *policyregistry.Snapshot, func(), error) {
	signer, err := policyregistry.NewAssertionSigner([]byte(strings.TrimSpace(config.GetOr("ROUTER_SERVING_ASSERTION_KEY", ""))), time.Now)
	if err != nil {
		return nil, nil, nil, err
	}
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
	registry, err := policyregistry.NewGCSRegistry(ctx, registryURI)
	if err != nil {
		return nil, nil, nil, err
	}
	closeRegistry := func() { _ = registry.Close() }
	timeout := parseEnvDurationMs("ROUTER_HMM_SIDECAR_TIMEOUT_MS", policyclient.DefaultTimeout)
	attemptTimeout := parseEnvAttemptTimeoutMs("ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS", policyclient.DeriveAttemptTimeout(timeout))
	cache, err := policyregistry.NewServingRuntimeCache(registry, hmmPolicySnapshotBuilder(
		availableProviders, []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding},
		config.GetOr("ROUTER_HMM_SIDECAR_AUTH", policySidecarAuthGoogleIDToken), timeout, attemptTimeout,
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
	return &middleware.ServingAdmissionConfig{Signer: signer, Store: registry, Identity: identity, Cache: cache}, baseline, closeRegistry, nil
}

func servingReferenceFromEnv(prefix string) policyregistry.ObjectRef {
	return policyregistry.ObjectRef{
		URI: config.MustGet(prefix + "_URI"), SHA256: config.MustGet(prefix + "_SHA256"),
		Generation: int64(parseEnvInt(prefix+"_GENERATION", 0)),
	}
}
