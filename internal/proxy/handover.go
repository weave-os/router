package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const DefaultHandoverTimeout = 8 * time.Second

const DefaultHandoverMaxTokens = 800

const handoverInstruction = "Summarize the conversation so far in <= 800 tokens. " +
	"Preserve all decisions, file paths, code snippets, and the user's latest intent. " +
	"Output only the summary text — no preamble, no closing remark."

const DefaultCompactionMaxTokens = 4000

const DefaultCompactionTimeout = 90 * time.Second

type ProviderSummarizer struct {
	plans             *policy.PlanResolver
	executor          *dispatch.Executor
	provider          string
	model             string
	timeout           time.Duration
	maxTokens         int
	compactionTimeout time.Duration
	compactionModel   string
}

func NewProviderSummarizer(plans *policy.PlanResolver, executor *dispatch.Executor, provider, model string, timeout time.Duration) *ProviderSummarizer {
	if provider == "" {
		provider = providers.ProviderAnthropic
	}
	if model == "" {
		model = policy.HandoverSummaryDefaultModel
	}
	if timeout <= 0 {
		timeout = DefaultHandoverTimeout
	}
	return &ProviderSummarizer{
		plans:             plans,
		executor:          executor,
		provider:          provider,
		model:             model,
		timeout:           timeout,
		maxTokens:         DefaultHandoverMaxTokens,
		compactionTimeout: DefaultCompactionTimeout,
	}
}

func (s *ProviderSummarizer) WithCompactionTimeout(d time.Duration) *ProviderSummarizer {
	if d > 0 {
		s.compactionTimeout = d
	}
	return s
}

func (s *ProviderSummarizer) WithCompactionModel(model string) *ProviderSummarizer {
	s.compactionModel = model
	return s
}

func (s *ProviderSummarizer) WithMaxTokens(n int) *ProviderSummarizer {
	if n > 0 {
		s.maxTokens = n
	}
	return s
}

func (s *ProviderSummarizer) Provider() string {
	return s.provider
}

var ErrEmptySummary = errors.New("handover: upstream returned no summary text")

func (s *ProviderSummarizer) Summarize(ctx context.Context, env *translate.RequestEnvelope, scope router.Request) (string, handover.Usage, error) {
	plan, err := s.resolve(ctx, policy.ResolutionRequest{
		Purpose:       policy.PurposeHandoverSummary,
		RouterRequest: summarizerRequest(scope, env),
		Overrides:     []policy.TargetOverride{{Source: policy.OverrideSourceDeployment, CatalogID: s.model, Provider: s.provider}},
	})
	if err != nil {
		return "", handover.Usage{}, err
	}
	return s.run(ctx, env, plan, handoverInstruction, s.maxTokens, s.timeout)
}

func (s *ProviderSummarizer) CompactionHandover() handover.Summarizer {
	return compactionHandoverSummarizer{s}
}

type compactionHandoverSummarizer struct{ *ProviderSummarizer }

func (c compactionHandoverSummarizer) Summarize(ctx context.Context, env *translate.RequestEnvelope, scope router.Request) (string, handover.Usage, error) {
	request := policy.ResolutionRequest{
		Purpose:       policy.PurposeCompactionHandoverSummary,
		RouterRequest: summarizerRequest(scope, env),
	}
	if c.compactionModel != "" {
		request.Overrides = []policy.TargetOverride{{Source: policy.OverrideSourceDeployment, CatalogID: c.compactionModel, Provider: c.provider}}
	}
	plan, err := c.resolve(ctx, request)
	if err != nil {
		return "", handover.Usage{}, err
	}
	return c.run(ctx, env, plan, handoverInstruction, c.maxTokens, c.compactionTimeout)
}

func summarizerRequest(scope router.Request, env *translate.RequestEnvelope) router.Request {
	request := router.Request{
		EnabledProviders: scope.EnabledProviders,
		Candidates: router.Candidates{
			ExcludedModels: scope.Candidates.ExcludedModels,
		},
		GatewayProviders: scope.GatewayProviders,
		CustomBindings:   scope.CustomBindings,
	}
	if env != nil {
		request.EstimatedInputTokens = env.ContextOverflowTokenEstimate()
	}
	return request
}

func (s *ProviderSummarizer) resolve(ctx context.Context, request policy.ResolutionRequest) (policy.ResolvedPlan, error) {
	if s.plans == nil || s.executor == nil {
		return policy.ResolvedPlan{}, errors.New("handover: summarizer has no plan resolver or executor")
	}
	if subscriptionStateModelsEnabled(ctx) {
		paidCtx := context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, modelSet(installationSubscriptionModelsWhenInactiveFromContext(ctx)))
		allowed := allowedModelsForRequest(paidCtx)
		request.RouterRequest.Candidates.ExcludedModels = cloneStringSet(request.RouterRequest.Candidates.ExcludedModels)
		if request.RouterRequest.Candidates.ExcludedModels == nil {
			request.RouterRequest.Candidates.ExcludedModels = make(map[string]struct{})
		}
		for _, model := range catalog.Models {
			if _, permitted := allowed[model.ID]; !permitted {
				request.RouterRequest.Candidates.ExcludedModels[model.ID] = struct{}{}
			}
		}
	}
	plan, err := s.plans.Resolve(request)
	if err != nil {
		observability.FromContext(ctx).Warn("Summarizer plan resolution failed", "purpose", string(request.Purpose), "err", err)
		return policy.ResolvedPlan{}, fmt.Errorf("handover: resolve plan: %w", err)
	}
	return plan, nil
}

func (s *ProviderSummarizer) run(ctx context.Context, env *translate.RequestEnvelope, plan policy.ResolvedPlan, instruction string, maxTokens int, timeout time.Duration) (string, handover.Usage, error) {
	log := observability.FromContext(ctx)
	if env == nil {
		return "", handover.Usage{}, errors.New("handover: nil envelope")
	}
	if budget := plan.Budget().TimeoutMillis; budget > 0 && time.Duration(budget)*time.Millisecond < timeout {
		timeout = time.Duration(budget) * time.Millisecond
	}
	if budget := plan.Budget().MaxOutputTokens; budget > 0 && budget < maxTokens {
		maxTokens = budget
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		text  string
		usage handover.Usage
	)
	transport := dispatch.Buffered{
		Reason: string(plan.Purpose()),
		Prepare: func(prepareCtx context.Context, attempt dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
			return prepareSummaryCall(prepareCtx, env, attempt.Target, instruction, maxTokens)
		},
		Consume: func(_ context.Context, attempt dispatch.Attempt, resp *http.Response) (err error) {
			text, usage, err = summaryFromResponse(resp, attempt.Target)
			return err
		},
	}.Transport()
	result, err := s.executor.Run(callCtx, inference.InvocationRequest{Purpose: plan.Purpose(), RequestID: observability.RequestIDFromContext(ctx)}, plan, transport)
	if err != nil {
		log.Warn("Summarizer upstream call failed", "purpose", string(plan.Purpose()), "err", err, "model", plan.SelectedTarget().CatalogID, "provider", plan.SelectedTarget().Provider, "fallback_reason", result.Summary.FallbackReason)
		return "", handover.Usage{}, err
	}
	return text, usage, nil
}

func prepareSummaryCall(ctx context.Context, env *translate.RequestEnvelope, target inference.Target, instruction string, maxTokens int) (providers.PreparedRequest, *http.Request, error) {
	body, err := buildSummaryRequestBody(env, target.CatalogID, instruction, maxTokens)
	if err != nil {
		return providers.PreparedRequest{}, nil, fmt.Errorf("build summary request: %w", err)
	}
	prep := providers.PreparedRequest{Body: body, Headers: make(http.Header)}
	prep.Headers.Set("anthropic-version", "2023-06-01")
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	return prep, req, nil
}

func summaryFromResponse(resp *http.Response, target inference.Target) (string, handover.Usage, error) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return "", handover.Usage{}, fmt.Errorf("handover: %w", &providers.UpstreamStatusError{Status: resp.StatusCode})
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", handover.Usage{}, fmt.Errorf("read summary response: %w", err)
	}
	text := extractAnthropicAssistantText(respBody)
	if text == "" {
		return "", handover.Usage{}, ErrEmptySummary
	}
	usage := extractAnthropicUsage(respBody)
	usage.Model = target.CatalogID
	usage.Provider = target.Provider
	return text, usage, nil
}

func extractAnthropicUsage(body []byte) handover.Usage {
	if !gjson.ValidBytes(body) {
		return handover.Usage{}
	}
	facts := usage.ParseAnthropic(gjson.GetBytes(body, "usage"))
	return handover.Usage{
		InputTokens:     facts.Input.Value,
		OutputTokens:    facts.Output.Value,
		CacheCreation:   facts.CacheWrite.Value,
		CacheRead:       facts.CacheRead.Value,
		CacheCreation1h: facts.CacheWrite1h.Value,
		InferenceGeo:    string(facts.InferenceGeo),
	}
}

func buildSummaryRequestBody(env *translate.RequestEnvelope, model, instruction string, maxTokens int) ([]byte, error) {
	prep, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: model})
	if err != nil {
		return nil, fmt.Errorf("prepare anthropic body: %w", err)
	}
	body := prep.Body

	body, err = sjson.SetBytes(body, "model", model)
	if err != nil {
		return nil, fmt.Errorf("set model: %w", err)
	}
	body, err = sjson.SetBytes(body, "stream", false)
	if err != nil {
		return nil, fmt.Errorf("set stream: %w", err)
	}
	body, err = sjson.SetBytes(body, "max_tokens", maxTokens)
	if err != nil {
		return nil, fmt.Errorf("set max_tokens: %w", err)
	}

	body, err = appendUserInstruction(body, instruction)
	if err != nil {
		return nil, fmt.Errorf("append instruction: %w", err)
	}

	for _, key := range []string{"tools", "tool_choice", "thinking", "context_management", "effort", "output_config", "metadata"} {
		body, _ = sjson.DeleteBytes(body, key)
	}

	return body, nil
}

func appendUserInstruction(body []byte, text string) ([]byte, error) {
	msg := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "messages.-1", raw)
}

func extractAnthropicAssistantText(body []byte) string {
	if !gjson.ValidBytes(body) {
		return ""
	}
	content := gjson.GetBytes(body, "content")
	if !content.IsArray() {
		return ""
	}
	var out bytes.Buffer
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "text" {
			return true
		}
		text := block.Get("text").String()
		if text == "" {
			return true
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(text)
		return true
	})
	return out.String()
}

var _ handover.Summarizer = (*ProviderSummarizer)(nil)

func (s *Service) runCompactionHandover(ctx context.Context, env *translate.RequestEnvelope, reqHeaders http.Header, decisionModel string, scope router.Request) dispatch.HandoverOutcome {
	log := observability.FromContext(ctx)
	var out dispatch.HandoverOutcome
	out.Invoked = true

	summarizer := s.summarizer
	if s.compactionHandoverSummarizer != nil {
		summarizer = s.compactionHandoverSummarizer
	}
	var (
		sumProvider       string
		sumCreds          *Credentials
		canCallSummarizer bool
	)
	if summarizer != nil {
		sumProvider = summarizer.Provider()
		sumCreds = s.credentials.SummarizerCredentials(ctx, sumProvider, reqHeaders)
		nonDepCreds := s.credentials.RequestUsesNonDeploymentCreds(ctx, reqHeaders)
		canCallSummarizer = sumCreds != nil || !nonDepCreds
	}

	switch {
	case summarizer == nil:
		out.FallbackToFullHistory = true
		log.Info("Compaction handover: summarizer not wired; preserved compacted body instead", "decision_model", decisionModel)
	case !canCallSummarizer:
		out.FallbackToFullHistory = true
		log.Info("Compaction handover: summarizer skipped (tenant boundary); preserved compacted body instead", "decision_model", decisionModel)
	default:
		var summCtx context.Context
		if sumCreds != nil {
			summCtx = context.WithValue(ctx, CredentialsContextKey{}, sumCreds)
		} else {
			summCtx = clearCredentials(ctx)
		}
		start := s.clock.Now()
		summary, summaryUsage, sumErr := summarizer.Summarize(summCtx, env, scope)
		out.LatencyMS = s.clock.Now().Sub(start).Milliseconds()
		switch {
		case sumErr != nil:
			out.FallbackToFullHistory = true
			log.Warn("Compaction handover: summarizer failed; preserved compacted body instead", "err", sumErr, "decision_model", decisionModel)
		case summary == "":
			out.FallbackToFullHistory = true
			log.Warn("Compaction handover: summarizer returned empty; preserved compacted body instead", "decision_model", decisionModel)
		default:
			handover.RewriteEnvelope(env, summary)
			out.SummaryTokens = estimateSummaryTokens(summary)
			out.SummaryUsage = summaryUsage
			log.Info("Compaction handover: context rewritten with summary", "summary_len", len(summary), "decision_model", decisionModel)
		}
	}
	return out
}
