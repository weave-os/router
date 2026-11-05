package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server"
	"weave-os/router/internal/translate"
)

func buildLocalServingRuntime(ctx context.Context, mode server.DeploymentMode, availableProviders map[string]struct{}, taskRuntimes ...*taskDomainRuntime) (*policyregistry.PinnedRuntime, func(), error) {
	if mode != server.DeploymentModeSelfHosted {
		return nil, nil, fmt.Errorf("local pinned serving requires selfhosted mode")
	}
	// Reject unsupported admission before any registry access; never silently choose the default.
	profileKey := config.GetOr("ROUTER_LOCAL_PROFILE_KEY", "")
	if profileKey != "" {
		return nil, nil, fmt.Errorf("local composition requires default profile until product admission is supplied")
	}
	pin := policyregistry.PinnedRuntimeRequest{
		Target:     policyregistry.ServingTarget(config.MustGet("ROUTER_LOCAL_POLICY_TARGET")),
		ProfileKey: profileKey,
		SelectionSet: policyregistry.ObjectRef{URI: config.MustGet("ROUTER_LOCAL_SELECTION_SET_URI"),
			SHA256: config.MustGet("ROUTER_LOCAL_SELECTION_SET_SHA256"), Generation: int64(parseEnvInt("ROUTER_LOCAL_SELECTION_SET_GENERATION", 0))},
		RouterRevision: config.MustGet("ROUTER_LOCAL_ROUTER_REVISION"), PolicySHA256: config.MustGet("ROUTER_LOCAL_POLICY_SHA256"),
		ClassifierArtifactID:    config.MustGet("ROUTER_LOCAL_CLASSIFIER_ARTIFACT_ID"),
		ClassifierPackageSHA256: config.MustGet("ROUTER_LOCAL_CLASSIFIER_SHA256"),
		ClassifierImageDigest:   config.MustGet("ROUTER_LOCAL_CLASSIFIER_IMAGE_DIGEST"),
		ClassifierURL:           config.MustGet("ROUTER_LOCAL_CLASSIFIER_URL"),
	}
	registry, err := policyregistry.NewGCSRegistry(ctx, strings.TrimSpace(config.GetOr("ROUTER_SERVING_REGISTRY_URI", "gs://weave_ml/weave_registry/router/serving")))
	if err != nil {
		return nil, nil, err
	}
	closeRegistry := func() { _ = registry.Close() }
	timeout := parseEnvDurationMs("ROUTER_HMM_SIDECAR_TIMEOUT_MS", 11*time.Second)
	attemptTimeout := parseEnvAttemptTimeoutMs("ROUTER_HMM_SIDECAR_ATTEMPT_TIMEOUT_MS", 10500*time.Millisecond)
	loaded, err := policyregistry.LoadPinnedRuntime(ctx, registry, pin, hmmPolicySnapshotBuilder(
		availableProviders, []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding, router.StrategyHMMBeta, router.StrategyHMMQwen},
		policySidecarAuthNone, timeout, attemptTimeout, taskRuntimes...))
	if err != nil {
		closeRegistry()
		return nil, nil, err
	}
	return loaded, closeRegistry, nil
}

// warmLocalHMMRoute checks classification/selection only; it never dispatches a model completion.
func warmLocalHMMRoute(ctx context.Context, runtime *policyregistry.PinnedRuntime) error {
	message := "Synthetic local pinned routing readiness check."
	_, err := policyregistry.NewAdmittedRouter(router.StrategyHMMEmbedding, runtime.Snapshot).Route(
		runtime.Bind(ctx), router.Request{PromptText: message,
			ConversationMessages: []router.ConversationMessage{{Role: string(translate.EscalationRoleUser), Text: message}}})
	return err
}

// buildInitialRoutingStrategy avoids unrelated ONNX initialization in pinned
// local mode. No request may fall back to an unselected strategy there.
func buildInitialRoutingStrategy(availableProviders map[string]struct{}, localPinned bool) (router.Router, string, error) {
	if localPinned {
		return localUnselectedStrategyRouter{}, "", nil
	}
	return buildClusterScorer(availableProviders)
}

type localUnselectedStrategyRouter struct{}

func (localUnselectedStrategyRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, fmt.Errorf("local pinned runtime requires its selected HMM snapshot; fallback strategy is unavailable")
}
