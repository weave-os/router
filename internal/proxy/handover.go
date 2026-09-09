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

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

// DefaultHandoverModel summarizes prior conversation before a mid-session
// model switch. Haiku-class is intentional: summarization is cheap.
const DefaultHandoverModel = "claude-haiku-4-5"

// DefaultHandoverTimeout bounds the summarizer call. Tuned for ~5-7s p95
// observed for haiku-class summarization of ~20k-token sessions.
const DefaultHandoverTimeout = 8 * time.Second

// DefaultHandoverMaxTokens caps the synthesized summary length.
const DefaultHandoverMaxTokens = 800

// handoverInstruction elicits the summary as a final user message, explicit
// about what must survive the switch (decisions, paths, latest intent).
const handoverInstruction = "Summarize the conversation so far in <= 800 tokens. " +
	"Preserve all decisions, file paths, code snippets, and the user's latest intent. " +
	"Output only the summary text — no preamble, no closing remark."

// DefaultCompactionMaxTokens caps the structured compaction summary. Larger
// than the switch handover cap because the compaction summary is the ONLY
// record of the elided history the model keeps — it must carry task state, not
// just a gist.
const DefaultCompactionMaxTokens = 4000

// DefaultCompactionTimeout bounds one compaction summary call. A Sonnet-class
// summary over a near-full context window routinely takes tens of seconds;
// the client is blocked on the compaction either way.
const DefaultCompactionTimeout = 90 * time.Second

// compactionInstruction elicits Claude Code's 9-section structured summary used
// when a long session is compacted to fit a context window. Unlike the terse
// switch-handover instruction, this preserves enough task state (pending work,
// current file, next step) that the model can continue seamlessly, and quotes
// user-stated constraints verbatim so they keep applying after the elision.
const compactionInstruction = "The conversation is being compacted to fit the model's context window. " +
	"Produce a detailed structured summary of everything above under these numbered sections, " +
	"prioritizing technical accuracy and completeness:\n" +
	"1. Primary Request and Intent — every explicit user request, in detail.\n" +
	"2. Key Technical Concepts — technologies, frameworks, and patterns in play.\n" +
	"3. Files and Code Sections — files examined/modified, with key snippets and why they matter.\n" +
	"4. Errors and Fixes — problems hit, fixes applied, and user feedback received.\n" +
	"5. Problem Solving — approaches tried and why each was chosen, not just outcomes.\n" +
	"6. All User Messages (verbatim) — quote every non-tool user message exactly, especially any stated constraints or policies; do not paraphrase.\n" +
	"7. Pending Tasks — requested work not yet completed.\n" +
	"8. Current Work — precisely what was being done just before this summary, with filenames and state.\n" +
	"9. Next Step — the immediate next action, aligned to the user's most recent request.\n" +
	"Output only the summary text — no preamble, no closing remark."

// ProviderSummarizer implements handover.Summarizer by resolving a reviewed
// policy plan for the summary purpose and running the Anthropic Messages
// request through the dispatch executor. Only the executor touches the
// provider client; this type never selects a provider on its own.
type ProviderSummarizer struct {
	plans     *policy.PlanResolver
	executor  *dispatch.Executor
	provider  string
	model     string
	timeout   time.Duration
	maxTokens int
	// compactionTimeout bounds SummarizeForCompaction separately: a
	// Sonnet-class summary of a near-full window is far slower than the
	// 800-token handover call.
	compactionTimeout time.Duration
	// compactionModel is the deployment override recorded on the compaction
	// policies (ROUTER_COMPACTION_MODEL); empty means the policy default.
	compactionModel string
}

// NewProviderSummarizer constructs the summarizer over the plan resolver and
// executor. provider/model are the deployment override recorded on the
// handover-summary policy; empty/zero args fall back to defaults.
func NewProviderSummarizer(plans *policy.PlanResolver, executor *dispatch.Executor, provider, model string, timeout time.Duration) *ProviderSummarizer {
	if provider == "" {
		provider = providers.ProviderAnthropic
	}
	if model == "" {
		model = DefaultHandoverModel
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

// WithCompactionTimeout overrides the hard timeout for compaction summaries
// (ROUTER_COMPACTION_TIMEOUT_MS). Zero/negative leaves the default.
func (s *ProviderSummarizer) WithCompactionTimeout(d time.Duration) *ProviderSummarizer {
	if d > 0 {
		s.compactionTimeout = d
	}
	return s
}

// WithCompactionModel records the deployment's compaction summarizer model,
// used as the deployment override for the compaction-handover purpose.
func (s *ProviderSummarizer) WithCompactionModel(model string) *ProviderSummarizer {
	s.compactionModel = model
	return s
}

// WithMaxTokens overrides the per-summary output cap. Zero/negative
// leaves the default.
func (s *ProviderSummarizer) WithMaxTokens(n int) *ProviderSummarizer {
	if n > 0 {
		s.maxTokens = n
	}
	return s
}

// Provider returns the upstream provider this summarizer dispatches to.
func (s *ProviderSummarizer) Provider() string {
	return s.provider
}

// ErrEmptySummary is returned when the upstream call succeeded but no
// assistant text was extractable.
var ErrEmptySummary = errors.New("handover: upstream returned no summary text")

// Summarize implements handover.Summarizer: resolves the handover-summary
// plan and runs the Anthropic Messages call through the executor under the
// plan's budget, returning summary text plus usage for a separate ledger row.
// On failure returns ("", zero Usage, err) so the caller falls back to the
// full prior history.
func (s *ProviderSummarizer) Summarize(ctx context.Context, env *translate.RequestEnvelope) (string, handover.Usage, error) {
	plan, err := s.resolve(ctx, policy.ResolutionRequest{
		Purpose:   policy.PurposeHandoverSummary,
		Overrides: []policy.TargetOverride{{Source: policy.OverrideSourceDeployment, CatalogID: s.model, Provider: s.provider}},
	})
	if err != nil {
		return "", handover.Usage{}, err
	}
	return s.run(ctx, env, plan, handoverInstruction, s.maxTokens, s.timeout)
}

// CompactionHandover returns a Summarizer for the compaction-handover purpose
// (a client-compacted turn routed off Anthropic). It runs the handover prompt
// on the compaction policy's reviewed model under the compaction timeout: the
// elided turns are gone client-side, so the summary is the only record.
func (s *ProviderSummarizer) CompactionHandover() handover.Summarizer {
	return compactionHandoverSummarizer{s}
}

type compactionHandoverSummarizer struct{ *ProviderSummarizer }

func (c compactionHandoverSummarizer) Summarize(ctx context.Context, env *translate.RequestEnvelope) (string, handover.Usage, error) {
	request := policy.ResolutionRequest{Purpose: policy.PurposeCompactionHandoverSummary}
	if c.compactionModel != "" {
		request.Overrides = []policy.TargetOverride{{Source: policy.OverrideSourceDeployment, CatalogID: c.compactionModel, Provider: c.provider}}
	}
	plan, err := c.resolve(ctx, request)
	if err != nil {
		return "", handover.Usage{}, err
	}
	return c.run(ctx, env, plan, handoverInstruction, c.maxTokens, c.compactionTimeout)
}

// CompactionTarget names the window-aware summarizer model the compaction
// cascade chose and where the choice came from: the session's warm pin, the
// deployment's configured model, or (empty Source) the policy's own default.
type CompactionTarget struct {
	CatalogID string
	Source    policy.OverrideSource
}

// SummarizeForCompaction summarizes env with the structured 9-section
// compaction prompt against target (the window-aware selection happens in the
// caller) and a larger output cap. The target must be a reviewed member of
// the precompaction policy; an unreviewed one fails resolution before any
// upstream I/O. Same failure contract as Summarize.
func (s *ProviderSummarizer) SummarizeForCompaction(ctx context.Context, env *translate.RequestEnvelope, target CompactionTarget, maxTokens int) (string, handover.Usage, error) {
	if target.CatalogID == "" {
		target.CatalogID = s.model
	}
	if maxTokens <= 0 {
		maxTokens = DefaultCompactionMaxTokens
	}
	request := policy.ResolutionRequest{
		Purpose:       policy.PurposePrecompactionSummary,
		RouterRequest: router.Request{AllowedModels: map[string]struct{}{target.CatalogID: {}}},
	}
	if target.Source != "" {
		request.Overrides = []policy.TargetOverride{{Source: target.Source, CatalogID: target.CatalogID, Provider: s.provider}}
	}
	plan, err := s.resolve(ctx, request)
	if err != nil {
		return "", handover.Usage{}, err
	}
	return s.run(ctx, env, plan, compactionInstruction, maxTokens, s.compactionTimeout)
}

func (s *ProviderSummarizer) resolve(ctx context.Context, request policy.ResolutionRequest) (policy.ResolvedPlan, error) {
	if s.plans == nil || s.executor == nil {
		return policy.ResolvedPlan{}, errors.New("handover: summarizer has no plan resolver or executor")
	}
	plan, err := s.plans.Resolve(request)
	if err != nil {
		observability.FromContext(ctx).Warn("Summarizer plan resolution failed", "purpose", string(request.Purpose), "err", err)
		return policy.ResolvedPlan{}, fmt.Errorf("handover: resolve plan: %w", err)
	}
	return plan, nil
}

// run executes one buffered Anthropic Messages summary through the executor.
// The policy budget tightens (never loosens) the configured timeout and
// output cap. On any failure returns ("", zero, err) so callers fall back.
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
		Prepare: func(_ context.Context, attempt dispatch.Attempt) (providers.PreparedRequest, *http.Request, error) {
			return prepareSummaryCall(env, attempt.Target, instruction, maxTokens)
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

// prepareSummaryCall builds the non-streaming Anthropic Messages request for
// target; dispatch checks the wire model against the plan before any I/O.
func prepareSummaryCall(env *translate.RequestEnvelope, target inference.Target, instruction string, maxTokens int) (providers.PreparedRequest, *http.Request, error) {
	body, err := buildSummaryRequestBody(env, target.CatalogID, instruction, maxTokens)
	if err != nil {
		return providers.PreparedRequest{}, nil, fmt.Errorf("build summary request: %w", err)
	}
	prep := providers.PreparedRequest{Body: body, Headers: make(http.Header)}
	prep.Headers.Set("anthropic-version", "2023-06-01")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	return prep, req, nil
}

// summaryFromResponse extracts the assistant text and usage from a buffered
// non-streaming Anthropic response, reporting non-2xx as an upstream status
// error so the executor can classify it.
func summaryFromResponse(resp *http.Response, target inference.Target) (string, handover.Usage, error) {
	defer resp.Body.Close()
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

// extractAnthropicUsage pulls the usage block from an Anthropic non-streaming
// Messages response. Missing fields are zero; we don't distinguish "absent".
func extractAnthropicUsage(body []byte) handover.Usage {
	if !gjson.ValidBytes(body) {
		return handover.Usage{}
	}
	usage := gjson.GetBytes(body, "usage")
	if !usage.IsObject() {
		return handover.Usage{}
	}
	return handover.Usage{
		InputTokens:   int(usage.Get("input_tokens").Int()),
		OutputTokens:  int(usage.Get("output_tokens").Int()),
		CacheCreation: int(usage.Get("cache_creation_input_tokens").Int()),
		CacheRead:     int(usage.Get("cache_read_input_tokens").Int()),
	}
}

// buildSummaryRequestBody builds a non-streaming Anthropic Messages request
// from the envelope's prior conversation, injecting the given summary
// instruction and overriding model/max_tokens/stream.
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

// appendUserInstruction appends a role=user text message to the messages array.
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

// extractAnthropicAssistantText pulls concatenated text from an Anthropic
// Messages non-streaming response. All text blocks are joined with newlines.
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

// runCompactionHandover rewrites env in-place with a handover summary when
// Claude Code context compaction is detected on a non-Anthropic route —
// compaction already dropped old turns, so without this the non-Anthropic
// model loses awareness of edits/decisions from those elided turns.
//
// On any failure it logs and passes the compacted body through unchanged
// (never trims it further, which would discard Claude Code's own compaction
// summary). Returns a handoverOutcome so the caller can bill the summary call.
func (s *Service) runCompactionHandover(ctx context.Context, env *translate.RequestEnvelope, reqHeaders http.Header, decisionModel string) handoverOutcome {
	log := observability.FromContext(ctx)
	var out handoverOutcome
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
		sumCreds = resolveSummarizerCreds(ctx, sumProvider, reqHeaders)
		nonDepCreds := s.requestUsesNonDeploymentCreds(ctx, reqHeaders)
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
		summCtx := ctx
		if sumCreds != nil {
			summCtx = context.WithValue(ctx, CredentialsContextKey{}, sumCreds)
		} else {
			// Strip any request credential (e.g. subscription OAuth token) so
			// this synthetic call runs on the deployment key instead of
			// inheriting one that could 401 or cross a tenant boundary.
			summCtx = clearCredentials(ctx)
		}
		start := time.Now()
		summary, summaryUsage, sumErr := summarizer.Summarize(summCtx, env)
		out.LatencyMS = time.Since(start).Milliseconds()
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
