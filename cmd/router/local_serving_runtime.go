package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"

	"weave-os/router/internal/config"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
)

type localServingIdentity struct {
	Target              policyregistry.ServingTarget
	SelectionSHA256     string
	RouterRevision      string
	ClassifierSHA256    string
	ClassifierURL       string
	SelectionURI        string
	SelectionGeneration int64
	ProfileKey          string
}

func localServingIdentityFromEnv(mode server.DeploymentMode) (localServingIdentity, error) {
	identity := localServingIdentity{
		Target:           policyregistry.ServingTarget(strings.TrimSpace(config.GetOr("ROUTER_LOCAL_POLICY_TARGET", ""))),
		SelectionSHA256:  strings.TrimSpace(config.GetOr("ROUTER_LOCAL_SELECTION_SET_SHA256", "")),
		RouterRevision:   strings.TrimSpace(config.GetOr("ROUTER_LOCAL_ROUTER_REVISION", "")),
		ClassifierSHA256: strings.TrimSpace(config.GetOr("ROUTER_LOCAL_CLASSIFIER_SHA256", "")),
		ClassifierURL:    strings.TrimSpace(config.GetOr("ROUTER_LOCAL_CLASSIFIER_URL", "")),
		SelectionURI:     strings.TrimSpace(config.GetOr("ROUTER_LOCAL_SELECTION_SET_URI", "")),
		ProfileKey:       strings.TrimSpace(config.GetOr("ROUTER_LOCAL_PROFILE_KEY", "")),
	}
	identity.SelectionGeneration, _ = strconv.ParseInt(config.GetOr("ROUTER_LOCAL_SELECTION_SET_GENERATION", "0"), 10, 64)
	if identity.SelectionURI == "" || identity.SelectionGeneration <= 0 {
		return identity, errors.New("local policy snapshot requires an immutable selection URI and generation")
	}
	if mode != server.DeploymentModeSelfHosted {
		return identity, errors.New("local policy snapshot requires selfhosted deployment mode")
	}
	if _, err := identity.Target.Environment(); err != nil {
		return identity, err
	}
	for _, digest := range []string{identity.SelectionSHA256, identity.ClassifierSHA256} {
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return identity, errors.New("local policy snapshot requires lowercase SHA-256 identities")
		}
	}
	if len(identity.RouterRevision) != 40 || strings.Trim(identity.RouterRevision, "0123456789abcdef") != "" {
		return identity, errors.New("local policy snapshot requires the exact router source revision")
	}
	endpoint, err := url.Parse(identity.ClassifierURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return identity, errors.New("local classifier URL must be an HTTP loopback origin")
	}
	if host := endpoint.Hostname(); host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return identity, errors.New("local classifier URL must use loopback")
	}
	if endpoint.Port() == "" {
		return identity, errors.New("local classifier URL requires an explicit port")
	}
	return identity, nil
}

// buildLocalServingRuntime loads an immutable selection once. A later
// activation cannot change a running local session or silently swap its roster.
func buildLocalServingRuntime(ctx context.Context, availableProviders map[string]struct{}) (*policyregistry.Snapshot, func(), error) {
	identity, err := localServingIdentityFromEnv(server.DeploymentModeSelfHosted)
	if err != nil {
		return nil, nil, err
	}
	registryURI := strings.TrimSpace(config.GetOr("ROUTER_SERVING_REGISTRY_URI", "gs://weave_ml/weave_registry/router/serving"))
	registry, err := policyregistry.NewGCSRegistry(ctx, registryURI)
	if err != nil {
		return nil, nil, err
	}
	closeRegistry := func() { _ = registry.Close() }
	fail := func(err error) (*policyregistry.Snapshot, func(), error) {
		closeRegistry()
		return nil, nil, err
	}
	selectionRef := policyregistry.ObjectRef{URI: identity.SelectionURI, SHA256: identity.SelectionSHA256, Generation: identity.SelectionGeneration}
	manifest, _, err := registry.ReadServingObject(ctx, policyregistry.ServingSelectionSet, selectionRef)
	if err != nil {
		return fail(fmt.Errorf("read pinned selection set: %w", err))
	}
	selectionSet, ok := manifest.(*policyregistry.SelectionSetV2)
	if !ok || selectionSet.Target != identity.Target {
		return fail(errors.New("local serving requires a v2 selection set for the requested target"))
	}
	selection := selectionSet.View(selectionRef).Default
	if identity.ProfileKey != "" {
		var exists bool
		selection, exists = selectionSet.View(selectionRef).Profiles[identity.ProfileKey]
		if !exists {
			return fail(errors.New("requested profile is absent from pinned selection"))
		}
	}
	prepared, err := policyregistry.ReadPreparedSelection(ctx, registry, identity.Target, identity.ProfileKey, selection)
	if err != nil {
		return fail(fmt.Errorf("validate pinned serving selection: %w", err))
	}
	if prepared.Candidate.Provenance.RouterRevision != identity.RouterRevision || prepared.Candidate.Classifier.Identity.PackageSHA256 != identity.ClassifierSHA256 {
		return fail(errors.New("local router revision or classifier package differs from the pinned production candidate"))
	}
	timeout := parseEnvDurationMs("ROUTER_HMM_SIDECAR_TIMEOUT_MS", policyclient.DefaultTimeout)
	attemptTimeout := parseEnvAttemptTimeoutMs("ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS", policyclient.DeriveAttemptTimeout(timeout))
	buildRouters := hmmPolicySnapshotBuilder(availableProviders, []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding}, policySidecarAuthNone, timeout, attemptTimeout)
	cache, err := policyregistry.NewServingRuntimeCache(registry, func(ctx context.Context, candidate policyregistry.Candidate) (map[router.Strategy]router.Router, error) {
		candidate.HeadSnapshot.Head.ClassifierRevisionURL = identity.ClassifierURL
		candidate.ClassifierAudience = ""
		return buildRouters(ctx, candidate)
	})
	if err != nil {
		return fail(err)
	}
	snapshot, err := cache.Snapshot(ctx, policyregistry.SessionReleaseBinding{Target: identity.Target, ProfileKey: identity.ProfileKey, Selection: selection})
	if err != nil {
		return fail(fmt.Errorf("load pinned local HMM snapshot: %w", err))
	}
	return snapshot, closeRegistry, nil
}

const localServingStartupTimeout = 90 * time.Second
const localServingRouteWarmupTimeout = 25 * time.Second
const localServingRouteWarmupAttemptTimeout = 11 * time.Second
const localServingRouteWarmupAttempts = 2

// warmLocalHMMRoute exercises the classifier and selection path before /readyz
// can admit traffic. Route makes a decision only; it does not call a provider.
func warmLocalHMMRoute(ctx context.Context, hmmRouter router.Router) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("local HMM route warm-up stopped before attempt 1: %w", err)
	}
	attempts := 0
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		attempts++
		attemptCtx, cancel := context.WithTimeout(ctx, localServingRouteWarmupAttemptTimeout)
		defer cancel()
		_, err := hmmRouter.Route(attemptCtx, router.Request{
			PromptText: "Synthetic local HMM router readiness warm-up.",
		})
		return struct{}{}, err
	},
		backoff.WithBackOff(backoff.NewExponentialBackOff()),
		backoff.WithMaxTries(localServingRouteWarmupAttempts),
		backoff.WithMaxElapsedTime(0),
	)
	if err != nil {
		return fmt.Errorf("local HMM route warm-up failed after %d attempts: %w", attempts, err)
	}
	return nil
}
