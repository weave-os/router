package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/errgroup"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

type startupMessageRole string

const startupMessageUser startupMessageRole = "user"

type startupMessage struct {
	Role    startupMessageRole `json:"role"`
	Content string             `json:"content"`
}

type startupModelTarget struct {
	CatalogID   string
	Provider    string
	Credentials *requestcontext.Credentials
}

type startupModelGeneration struct {
	plan        policy.ResolvedPlan
	credentials *requestcontext.Credentials
}

func startupModelSet(enabledProviders map[string]struct{}, deployment policy.DeploymentPolicyConfig) map[string]struct{} {
	models := catalog.RoutingTargetSet(enabledProviders)
	for _, spec := range policy.DefaultRegistry().Specs() {
		// Optional features supply their explicit binding and credentials below.
		if spec.Optional {
			continue
		}
		for _, model := range spec.FixedCatalogModels {
			if len(catalog.EnumerateBindings(model, enabledProviders)) > 0 {
				models[model] = struct{}{}
			}
		}
	}
	for _, target := range deployment.TargetOverrides {
		if len(catalog.EnumerateBindings(target.Target.CatalogID, enabledProviders)) > 0 {
			models[target.Target.CatalogID] = struct{}{}
		}
	}
	return models
}

func startupDeploymentTargets(enabledProviders map[string]struct{}, deployment policy.DeploymentPolicyConfig) []startupModelTarget {
	var targets []startupModelTarget
	for _, override := range deployment.TargetOverrides {
		if _, configured := enabledProviders[override.Target.Provider]; configured {
			targets = append(targets, startupModelTarget{CatalogID: override.Target.CatalogID, Provider: override.Target.Provider})
		}
	}
	return targets
}

func warmStartupModels(ctx context.Context, log *slog.Logger, clients *dispatch.Clients, models, enabledProviders map[string]struct{}, requiredTargets ...startupModelTarget) error {
	resolvableModels := make(map[string]struct{}, len(models)+len(requiredTargets))
	maps.Copy(resolvableModels, models)
	resolvableProviders := make(map[string]struct{}, len(enabledProviders)+len(requiredTargets))
	maps.Copy(resolvableProviders, enabledProviders)
	for _, target := range requiredTargets {
		if target.CatalogID == "" || target.Provider == "" {
			return fmt.Errorf("required startup model must name its catalog model and provider")
		}
		if target.Credentials != nil && len(target.Credentials.APIKey) == 0 {
			return fmt.Errorf("required startup provider %s has an empty explicit credential", target.Provider)
		}
		if _, configured := enabledProviders[target.Provider]; !configured && target.Credentials == nil {
			return fmt.Errorf("required startup provider %s has no deployment credentials", target.Provider)
		}
		resolvableModels[target.CatalogID] = struct{}{}
		resolvableProviders[target.Provider] = struct{}{}
	}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(resolvableModels, resolvableProviders, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	if err != nil {
		return err
	}
	// Share the serving adapters without recording synthetic work as customer attempts.
	executor, err := dispatch.NewExecutor(clients)
	if err != nil {
		return err
	}
	modelIDs := make([]string, 0, len(models))
	for model := range models {
		modelIDs = append(modelIDs, model)
	}
	slices.Sort(modelIDs)
	resolved := make([]startupModelGeneration, 0, len(modelIDs)+len(requiredTargets))
	type binding struct{ provider, model string }
	ordinaryBindings := make(map[binding]struct{})
	for _, model := range modelIDs {
		plan, err := plans.Resolve(policy.ResolutionRequest{
			Purpose:       policy.PurposeStartupWarmup,
			RouterRequest: router.Request{EnabledProviders: enabledProviders, EstimatedInputTokens: 4},
			Overrides:     []policy.TargetOverride{{Source: policy.OverrideSourceRequest, CatalogID: model}},
		})
		if err != nil {
			return fmt.Errorf("resolve startup model %s: %w", model, err)
		}
		resolved = append(resolved, startupModelGeneration{plan: plan})
		selected := plan.SelectedTarget()
		ordinaryBindings[binding{selected.Provider, selected.CatalogID}] = struct{}{}
	}
	for _, target := range requiredTargets {
		selected := binding{target.Provider, target.CatalogID}
		if _, warmed := ordinaryBindings[selected]; warmed && target.Credentials == nil {
			continue
		}
		plan, err := plans.Resolve(policy.ResolutionRequest{
			Purpose:       policy.PurposeStartupWarmup,
			RouterRequest: router.Request{EnabledProviders: map[string]struct{}{target.Provider: {}}, EstimatedInputTokens: 4},
			Overrides:     []policy.TargetOverride{{Source: policy.OverrideSourceRequest, CatalogID: target.CatalogID, Provider: target.Provider}},
		})
		if err != nil {
			return fmt.Errorf("resolve required startup model %s/%s: %w", target.Provider, target.CatalogID, err)
		}
		resolved = append(resolved, startupModelGeneration{plan: plan, credentials: target.Credentials})
		if target.Credentials == nil {
			ordinaryBindings[selected] = struct{}{}
		}
	}
	log.Info("Starting synthetic model generations", "models", len(models), "generations", len(resolved), "max_concurrency", 4, "max_output_tokens", 1024)
	startupCtx := ctx
	workers, ctx := errgroup.WithContext(ctx)
	workers.SetLimit(4)
	for _, generation := range resolved {
		if err := ctx.Err(); err != nil {
			break
		}
		workers.Go(func() error {
			started := time.Now()
			plan := generation.plan
			target := plan.SelectedTarget()
			callCtx, cancel := context.WithTimeout(ctx, time.Duration(plan.Budget().TimeoutMillis)*time.Millisecond)
			defer cancel()
			callCtx = requestcontext.WithCredentials(callCtx, generation.credentials)
			transport := dispatch.Buffered{
				Reason:           string(policy.PurposeStartupWarmup),
				MaxResponseBytes: 1 << 20,
				Prepare: func(ctx context.Context, attempt dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
					return prepareStartupGeneration(ctx, attempt.Target)
				},
				Consume: func(_ context.Context, attempt dispatch.Attempt, response *http.Response) error {
					return validateStartupGeneration(response, attempt.Target)
				},
			}.Transport()
			generationResult, err := executor.Run(callCtx, inference.InvocationRequest{Purpose: policy.PurposeStartupWarmup}, plan, transport)
			if err != nil {
				return fmt.Errorf("generate startup model %s/%s: %w", target.Provider, target.CatalogID, err)
			}
			if generationResult.Outcome.FallbackUsed || generationResult.Outcome.AttemptCount != 1 || generationResult.Outcome.ServedTarget.CatalogID != target.CatalogID || generationResult.Outcome.ServedTarget.Provider != target.Provider {
				return fmt.Errorf("startup model %s did not execute its exact target once", target.CatalogID)
			}
			log.Info("Startup model generation completed", "provider", target.Provider, "model", target.CatalogID, "elapsed", time.Since(started))
			return nil
		})
	}
	if err := workers.Wait(); err != nil {
		return err
	}
	return startupCtx.Err()
}

func startupGenerationBudget(model string) (int, string) {
	reasoning := router.Lookup(model).Reasoning()
	if len(reasoning.Levels) == 0 || router.Lookup(model).Supports(router.CapExtendedThinking) && !router.Lookup(model).Supports(router.CapAdaptiveThinking) {
		return 32, ""
	}
	// Low effort is universally declared; a disable is not accepted by every
	// reasoning provider, even for models whose reasoning is not always on.
	return 1024, reasoning.Levels[0]
}

func prepareStartupGeneration(ctx context.Context, target inference.Target) (providers.PreparedRequest, *http.Request, error) {
	tokens, effort := startupGenerationBudget(target.CatalogID)
	body, err := json.Marshal(map[string]any{"model": target.CatalogID, "messages": []startupMessage{{Role: startupMessageUser, Content: "Reply with OK."}}, "max_tokens": tokens, "stream": false})
	if err != nil {
		return providers.PreparedRequest{}, nil, err
	}
	parse := translate.ParseOpenAI
	if providers.FamilyFor(target.Provider) == providers.FamilyAnthropic {
		// This synthetic message is valid in both formats. Native Anthropic
		// emission applies adaptive thinking and its explicit effort level.
		parse = translate.ParseAnthropic
	}
	envelope, err := parse(body)
	if err != nil {
		return providers.PreparedRequest{}, nil, err
	}
	capabilities := router.Lookup(target.CatalogID)
	options := translate.EmitOptions{
		TargetModel:          target.CatalogID,
		TargetProvider:       target.Provider,
		Capabilities:         capabilities,
		ForceEffort:          effort,
		ForceReasoningEffort: translate.ResolveForceEffort(capabilities, effort),
	}
	var prepared providers.PreparedRequest
	switch providers.FamilyFor(target.Provider) {
	case providers.FamilyAnthropic:
		prepared, err = envelope.PrepareAnthropic(nil, options)
	case providers.FamilyGemini:
		prepared, err = envelope.PrepareGemini(nil, options)
	case providers.FamilyOpenAICompat:
		if target.Provider == providers.ProviderOpenAI && options.Capabilities.Supports(router.CapReasoning) {
			prepared, err = envelope.PrepareOpenAIResponses(nil, options)
		} else {
			prepared, err = envelope.PrepareOpenAI(nil, options)
		}
	default:
		return providers.PreparedRequest{}, nil, fmt.Errorf("startup target has no supported translation family")
	}
	if err != nil {
		return providers.PreparedRequest{}, nil, err
	}
	// Serving translation reserves large reasoning headroom. Startup has its own
	// bounded budget and accepts measured reasoning without requiring a full answer.
	outputField := "max_tokens"
	switch {
	case providers.FamilyFor(target.Provider) == providers.FamilyGemini:
		outputField = "generationConfig.maxOutputTokens"
	case prepared.Endpoint == providers.EndpointResponses:
		outputField = "max_output_tokens"
	case gjson.GetBytes(prepared.Body, "max_completion_tokens").Exists():
		outputField = "max_completion_tokens"
	}
	prepared.Body, err = sjson.SetBytes(prepared.Body, outputField, tokens)
	if err != nil {
		return providers.PreparedRequest{}, nil, err
	}
	if providers.FamilyFor(target.Provider) != providers.FamilyGemini {
		prepared.Body, err = sjson.SetBytes(prepared.Body, "stream", false)
		if err != nil {
			return providers.PreparedRequest{}, nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://router.invalid/", bytes.NewReader(prepared.Body))
	if err != nil {
		return providers.PreparedRequest{}, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return prepared, request, nil
}

func validateStartupGeneration(response *http.Response, target inference.Target) error {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &providers.UpstreamStatusError{Status: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 || !gjson.ValidBytes(body) {
		return fmt.Errorf("startup generation returned invalid JSON or exceeded response budget")
	}
	if !gjson.ParseBytes(body).IsObject() || gjson.GetBytes(body, "error").Type != gjson.Null {
		return fmt.Errorf("startup generation returned an error envelope")
	}
	hasText := func(value gjson.Result) bool {
		return value.Type == gjson.String && strings.TrimSpace(value.String()) != ""
	}
	hasReasoningUsage := func(path string) bool {
		value := gjson.GetBytes(body, path)
		return value.Type == gjson.Number && value.Int() > 0
	}
	switch providers.FamilyFor(target.Provider) {
	case providers.FamilyAnthropic:
		for _, block := range gjson.GetBytes(body, "content").Array() {
			if hasText(block.Get("text")) || hasText(block.Get("thinking")) {
				return nil
			}
		}
	case providers.FamilyGemini:
		for _, candidate := range gjson.GetBytes(body, "candidates").Array() {
			for _, part := range candidate.Get("content.parts").Array() {
				if hasText(part.Get("text")) {
					return nil
				}
			}
		}
		if hasReasoningUsage("usageMetadata.thoughtsTokenCount") {
			return nil
		}
	case providers.FamilyOpenAICompat:
		if hasText(gjson.GetBytes(body, "choices.0.message.content")) || hasText(gjson.GetBytes(body, "choices.0.message.reasoning_content")) {
			return nil
		}
		for _, output := range gjson.GetBytes(body, "output").Array() {
			for _, content := range output.Get("content").Array() {
				if hasText(content.Get("text")) {
					return nil
				}
			}
		}
		if hasReasoningUsage("usage.output_tokens_details.reasoning_tokens") || hasReasoningUsage("usage.completion_tokens_details.reasoning_tokens") {
			return nil
		}
	}
	return fmt.Errorf("startup generation returned no text or measured reasoning")
}
