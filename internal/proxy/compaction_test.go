package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCompactionSummarizer struct {
	summary   string
	usage     handover.Usage
	err       error
	calls     int
	lastModel string
}

func (f *fakeCompactionSummarizer) SummarizeForCompaction(_ context.Context, _ *translate.RequestEnvelope, model string, _ int) (string, handover.Usage, error) {
	f.calls++
	f.lastModel = model
	return f.summary, f.usage, f.err
}

func (f *fakeCompactionSummarizer) Provider() string { return providers.ProviderAnthropic }

// alternatingAnthropicBody builds an Anthropic body of nMsgs user/assistant
// messages (starting with user), each padded to ~perMsgPad content bytes.
func alternatingAnthropicBody(nMsgs, perMsgPad int) []byte {
	pad := strings.Repeat("x", perMsgPad)
	var sb strings.Builder
	sb.WriteString(`{"model":"claude-opus-4-8","system":"sys","messages":[`)
	for i := range nMsgs {
		if i > 0 {
			sb.WriteString(",")
		}
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		sb.WriteString(`{"role":"` + role + `","content":"` + pad + `"}`)
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

// toolHeavyAnthropicBody builds nPairs of (assistant tool_use, user tool_result)
// with each tool_result carrying contentBytes of payload.
func toolHeavyAnthropicBody(nPairs, contentBytes int) []byte {
	pad := strings.Repeat("y", contentBytes)
	var sb strings.Builder
	sb.WriteString(`{"model":"claude-opus-4-8","messages":[`)
	for i := range nPairs {
		if i > 0 {
			sb.WriteString(",")
		}
		id := fmt.Sprintf("t%d", i)
		sb.WriteString(`{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]},`)
		sb.WriteString(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + pad + `"}]}`)
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

func TestMaybeCompact_UnderThresholdIsNoop(t *testing.T) {
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: &fakeCompactionSummarizer{}}
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(2, 20))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()

	res, err := s.maybeCompact(context.Background(), env, compactionInput{TurnType: turntype.MainLoop, OutputReserve: 0, MaxWindow: 1_000_000, Headers: http.Header{}})
	require.NoError(t, err)
	assert.False(t, res.Applied, "a small request must not be compacted")
	assert.Equal(t, before, env.ContextOverflowTokenEstimate(), "env must be untouched below threshold")
}

func TestMaybeCompact_DisabledWhenPctZero(t *testing.T) {
	s := &Service{} // compactionTriggerPct == 0 disables the cascade
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(20, 200))
	require.NoError(t, err)
	res, err := s.maybeCompact(context.Background(), env, compactionInput{TurnType: turntype.MainLoop, OutputReserve: 0, MaxWindow: 500, Headers: http.Header{}})
	require.NoError(t, err)
	assert.False(t, res.Applied)
}

func TestMaybeCompact_Tier1ClearsToolResults(t *testing.T) {
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct} // nil summarizer
	env, err := translate.ParseAnthropic(toolHeavyAnthropicBody(20, 300))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()
	// maxWindow between post-Tier-1 and pre-Tier-1 estimates so Tier-1 alone fits.
	maxWindow := before * 3 / 4

	res, err := s.maybeCompact(context.Background(), env, compactionInput{TurnType: turntype.MainLoop, OutputReserve: 0, MaxWindow: maxWindow, Headers: http.Header{}})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Positive(t, res.ToolResultsCleared, "old tool results should be cleared")
	assert.False(t, res.Summarized, "nil summarizer must not summarize")
	assert.LessOrEqual(t, env.ContextOverflowTokenEstimate(), maxWindow, "must fit after Tier-1")
}

func TestMaybeCompact_Tier3Summarizes(t *testing.T) {
	fake := &fakeCompactionSummarizer{summary: "SHORT STRUCTURED SUMMARY"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(20, 200))
	require.NoError(t, err)

	// Window that Tier-1 (no tool results here) can't satisfy but a
	// summarize + recent-12 rewrite can.
	res, err := s.maybeCompact(context.Background(), env, compactionInput{TurnType: turntype.MainLoop, OutputReserve: 0, MaxWindow: 900, Headers: http.Header{}})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.True(t, res.Summarized)
	assert.Equal(t, DefaultCompactionModel, res.SummaryModel, "no warm Anthropic pin → Sonnet-class default")
	assert.Equal(t, 1, fake.calls)
	assert.Equal(t, DefaultCompactionModel, fake.lastModel)
}

func TestMaybeCompact_ExceedsFloorReturnsSentinel(t *testing.T) {
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct} // nil summarizer
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(4, 400))
	require.NoError(t, err)

	// A window so small that even trimming to a single (large) message overflows.
	_, err = s.maybeCompact(context.Background(), env, compactionInput{TurnType: turntype.MainLoop, OutputReserve: 0, MaxWindow: 30, Headers: http.Header{}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrContextWindowExceeded))
}

func TestMaybeCompact_SkipsHardPinnedTurns(t *testing.T) {
	fake := &fakeCompactionSummarizer{summary: "x"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(20, 200))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()

	// A Compaction turn is Claude Code's own compaction request — the router
	// must not rewrite it, even when it's over threshold.
	res, err := s.maybeCompact(context.Background(), env, compactionInput{TurnType: turntype.Compaction, OutputReserve: 0, MaxWindow: 900, Headers: http.Header{}})
	require.NoError(t, err)
	assert.False(t, res.Applied, "hard-pinned turns must skip compaction")
	assert.Equal(t, 0, fake.calls, "summarizer must not be called for a Compaction turn")
	assert.Equal(t, before, env.ContextOverflowTokenEstimate(), "env must be untouched")
}

func TestMaybeCompact_AuthoritativePolicyNeverCallsSummarizer(t *testing.T) {
	strategy := router.Strategy("authoritative-compaction-test")
	fake := &fakeCompactionSummarizer{summary: "must not run"}
	s := (&Service{
		compactionTriggerPct: DefaultCompactionTriggerPct,
		compactionSummarizer: fake,
	}).WithPolicyStrategy(policy.StrategySpec{
		Strategy: strategy,
		Router:   &authoritativeTestRouter{},
		Capabilities: policy.Capabilities{
			AuthoritativePerTurnSelection: true,
		},
	})
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(20, 200))
	require.NoError(t, err)
	ctx := router.WithStrategy(context.Background(), strategy)

	result, _ := s.maybeCompact(ctx, env, compactionInput{TurnType: turntype.MainLoop, OutputReserve: 100, MaxWindow: 700, Headers: http.Header{}})

	assert.Equal(t, 0, fake.calls)
	assert.False(t, result.Summarized)
	assert.Positive(t, result.TrimmedToRecent, "authoritative routing must still rescue-trim when cleanup does not fit")
}

func TestWithCompaction_ZeroPctDisables(t *testing.T) {
	// ROUTER_COMPACTION_PCT=0 must disable, not fall back to the default.
	s := (&Service{}).WithCompaction(nil, 0)
	assert.Equal(t, 0.0, s.compactionTriggerPct)
	// A negative/out-of-range value falls back to the default.
	s = (&Service{}).WithCompaction(nil, -1)
	assert.Equal(t, DefaultCompactionTriggerPct, s.compactionTriggerPct)
	s = (&Service{}).WithCompaction(nil, 2)
	assert.Equal(t, DefaultCompactionTriggerPct, s.compactionTriggerPct)
}

func TestSelectCompactionSummarizer_WindowAware(t *testing.T) {
	s := &Service{}
	assert.Equal(t, DefaultCompactionModel, s.selectCompactionSummarizer(1_000, ""), "small history → Sonnet-class default")
	assert.Equal(t, largeWindowSummarizerModel, s.selectCompactionSummarizer(300_000, ""), "history over the default's window → large-window model")
	assert.Equal(t, "", s.selectCompactionSummarizer(5_000_000, ""), "history over every window → none")

	assert.Equal(t, "claude-opus-4-8", s.selectCompactionSummarizer(1_000, "claude-opus-4-8"), "warm Anthropic pin summarizes its own session")
	assert.Equal(t, DefaultCompactionModel, s.selectCompactionSummarizer(1_000, "claude-haiku-4-5"), "low-tier pin is not reused as summarizer")
	assert.Equal(t, DefaultCompactionModel, s.selectCompactionSummarizer(1_000, "gpt-5.5"), "non-Anthropic pin is not reused as summarizer")
	assert.Equal(t, largeWindowSummarizerModel, s.selectCompactionSummarizer(300_000, "claude-opus-4-8"), "pin that can't ingest the history is skipped")

	custom := &Service{compactionModel: "claude-sonnet-4-5"}
	assert.Equal(t, "claude-sonnet-4-5", custom.selectCompactionSummarizer(1_000, ""), "ROUTER_COMPACTION_MODEL overrides the default")
}

func TestCompactionPolicyFor(t *testing.T) {
	assert.True(t, compactionPolicyFor(ClientAppClaudeCode).DeferToClient, "Claude Code auto-compacts itself")
	assert.Equal(t, claudeCodeAutoCompactBuffer, compactionPolicyFor(ClientAppClaudeCode).ClientBuffer)
	assert.False(t, compactionPolicyFor(ClientAppCodex).DeferToClient, "Codex gets the router cascade")
	assert.False(t, compactionPolicyFor(ClientAppGeminiCLI).DeferToClient)
	assert.Equal(t, defaultCompactionPolicy, compactionPolicyFor(""), "unknown client → default policy")
	assert.Equal(t, defaultCompactionPolicy, compactionPolicyFor("some-new-harness"))
}

func TestClientWouldCompact(t *testing.T) {
	cc := compactionPolicyFor(ClientAppClaudeCode)
	// Pool serves the 200K window the client sizes against: the client's
	// own auto-compact (at 200K-13K) fires before the router needs to.
	assert.True(t, clientWouldCompact(cc, "claude-opus-4-8", 200_000))
	// Pool's largest window is below the client's compaction point: router
	// must compact or the request dead-ends.
	assert.False(t, clientWouldCompact(cc, "claude-opus-4-8", 128_000))
	assert.False(t, clientWouldCompact(compactionPolicyFor(ClientAppCodex), "gpt-5.5", 1_000_000), "non-deferring harness never defers")
	assert.False(t, clientWouldCompact(cc, "", 200_000), "unknown requested model → no deferral")
}

func TestMaybeCompact_ClaudeCodeDefersWhenPoolServesClientWindow(t *testing.T) {
	fake := &fakeCompactionSummarizer{summary: "x"}
	// Tiny trigger so a small fixture is "over threshold" against a 200K pool
	// that matches the requested model's window.
	s := &Service{compactionTriggerPct: 0.001, compactionSummarizer: fake}
	env, err := translate.ParseAnthropic(toolHeavyAnthropicBody(20, 300))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()

	res, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: 200_000, RequestedModel: "claude-opus-4-8", ClientApp: ClientAppClaudeCode, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.True(t, res.DeferredToClient, "Claude Code compacts itself at window-13K; pool serves that window")
	assert.False(t, res.Applied)
	assert.Equal(t, before, env.ContextOverflowTokenEstimate(), "env must be untouched when deferred")

	// Same shape from Codex: the router owns compaction.
	env2, err := translate.ParseAnthropic(toolHeavyAnthropicBody(20, 300))
	require.NoError(t, err)
	res, err = s.maybeCompact(context.Background(), env2, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: 200_000, RequestedModel: "gpt-5.5", ClientApp: ClientAppCodex, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.False(t, res.DeferredToClient)
	assert.True(t, res.Applied, "Codex gets Tier-1 tool-result cleanup")
	assert.Positive(t, res.ToolResultsCleared)

	// Claude Code against a pool smaller than its believed window: the
	// client's own compaction would fire too late, so the router compacts.
	env3, err := translate.ParseAnthropic(toolHeavyAnthropicBody(20, 300))
	require.NoError(t, err)
	res, err = s.maybeCompact(context.Background(), env3, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: 128_000, RequestedModel: "claude-opus-4-8", ClientApp: ClientAppClaudeCode, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.False(t, res.DeferredToClient)
	assert.True(t, res.Applied)
}

func TestMaybeCompact_OverflowNeverDefers(t *testing.T) {
	// Even a deferring harness must be compacted when the request already
	// overflows the pool: the client can't help on this turn.
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct}
	env, err := translate.ParseAnthropic(toolHeavyAnthropicBody(20, 300))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()
	res, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: before * 3 / 4, RequestedModel: "claude-opus-4-8", ClientApp: ClientAppClaudeCode, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.False(t, res.DeferredToClient)
	assert.Positive(t, res.ToolResultsCleared)
}

func TestCompactionHardPin(t *testing.T) {
	s := &Service{compactionHardPinEnabled: true}
	var key [sessionpin.SessionKeyLen]byte
	ctx := context.Background()

	p, m, ok := s.compactionHardPin(ctx, key, "", router.Request{})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, DefaultCompactionModel, m, "no pin → Sonnet-class default")

	_, _, ok = s.compactionHardPin(ctx, key, "", router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}})
	assert.False(t, ok, "Anthropic disabled for the tenant → fall back to generic hard-pin")

	_, _, ok = s.compactionHardPin(ctx, key, "", router.Request{GatewayProviders: map[string]struct{}{providers.ProviderOpenRouter: {}}})
	assert.False(t, ok, "gateway-exclusive tenant → fall back to generic hard-pin")

	_, _, ok = s.compactionHardPin(ctx, key, "", router.Request{ExcludedModels: map[string]struct{}{DefaultCompactionModel: {}}})
	assert.False(t, ok, "excluded default with no pin → fall back to generic hard-pin")

	unavailable := &Service{compactionHardPinEnabled: true, availableModels: map[string]struct{}{"claude-haiku-4-5": {}}}
	_, _, ok = unavailable.compactionHardPin(ctx, key, "", router.Request{})
	assert.False(t, ok, "default not routable in this deployment → fall back to generic hard-pin")
}

// rolePinStore serves a distinct pin per role so the thread pin and the
// _hmm_history row can disagree.
type rolePinStore struct {
	stubPinStore
	byRole map[string]sessionpin.Pin
}

func (s *rolePinStore) Get(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	pin, found := s.byRole[role]
	return pin, found, nil
}

func TestCompactionHardPin_CodexKeepsNonAnthropicSessionModel(t *testing.T) {
	var key [sessionpin.SessionKeyLen]byte
	ctx := context.Background()
	live := time.Now().Add(time.Hour)
	openAIProviders := map[string]providers.Client{providers.ProviderOpenAI: nil, providers.ProviderAnthropic: nil}
	codex := func(req router.Request) router.Request {
		req.ClientApp = ClientAppCodex
		return req
	}

	// A Codex thread the HMM has been serving on gpt-5.6-sol: its compaction
	// turn stays on Sol instead of crossing to the Anthropic summarizer.
	hmmServed := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s := &Service{compactionHardPinEnabled: true, pinStore: hmmServed, providers: openAIProviders}
	p, m, ok := s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenAI, p)
	assert.Equal(t, "gpt-5.6-sol", m)

	// Claude Code's compaction turn is Anthropic-format: the same history keeps
	// the Sonnet-class summarizer.
	p, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, router.Request{ClientApp: ClientAppClaudeCode})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, DefaultCompactionModel, m)

	// The most recently served model wins when the thread pin and HMM history disagree.
	switched := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole:                 {Provider: providers.ProviderOpenAI, Model: "gpt-5.6-terra", LastServedModel: "gpt-5.6-terra", LastTurnEndedAt: time.Now().Add(-time.Minute), PinnedUntil: live},
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: switched, providers: openAIProviders}
	_, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, "gpt-5.6-sol", m)

	// An expired thread pin no longer speaks for the session.
	expired := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(-time.Hour)},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: expired, providers: openAIProviders}
	p, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, DefaultCompactionModel, m)

	// A tenant that turned OpenAI off cannot keep the thread there.
	s = &Service{compactionHardPinEnabled: true, pinStore: switched, providers: openAIProviders}
	_, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}))
	require.True(t, ok)
	assert.Equal(t, DefaultCompactionModel, m, "served vendor disabled → Sonnet-class default")

	// Org exclusions and the deployment-wide automatic disable still apply.
	_, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{ExcludedModels: map[string]struct{}{"gpt-5.6-sol": {}}}))
	require.True(t, ok)
	assert.Equal(t, DefaultCompactionModel, m)
	_, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{AutomaticExcludedModels: map[string]struct{}{"gpt-5.6-sol": {}}}))
	require.True(t, ok)
	assert.Equal(t, DefaultCompactionModel, m)

	// A low-tier session model is what the cascade moves away from.
	lowServed := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderOpenAI, LastServedModel: "gpt-4.1-mini", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: lowServed, providers: openAIProviders}
	p, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, DefaultCompactionModel, m)

	// An Anthropic-served Codex session keeps its own model, as before.
	anthropicServed := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", LastServedModel: "claude-opus-4-8", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: anthropicServed, providers: openAIProviders}
	p, m, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, "claude-opus-4-8", m)
}

func TestMaxEligibleContextWindow(t *testing.T) {
	s := &Service{availableModels: map[string]struct{}{"claude-haiku-4-5": {}}}
	assert.Equal(t, 200_000, s.maxEligibleContextWindow(nil, nil, 0))
	assert.Equal(t, 200_000, s.maxEligibleContextWindow(nil, nil, 5_000), "Anthropic (signature-keeping) models ignore signature savings")
	assert.Equal(t, 0, s.maxEligibleContextWindow(map[string]struct{}{"claude-haiku-4-5": {}}, nil, 0), "policy-excluding the only model leaves no window")

	// A signature-stripping (non-Anthropic) model gets sigSavings added to its
	// effective window, matching the context-overflow pre-filter's discount.
	sStrip := &Service{availableModels: map[string]struct{}{"gpt-5.5": {}}}
	assert.Equal(t, 1_050_000, sStrip.maxEligibleContextWindow(nil, nil, 0))
	assert.Equal(t, 1_050_000+5_000, sStrip.maxEligibleContextWindow(nil, nil, 5_000), "stripping model gains signature savings as headroom")
}

func TestClassifyDispatchError_ContextWindowExceeded(t *testing.T) {
	cls, ok := ClassifyDispatchError(fmt.Errorf("wrapped: %w", ErrContextWindowExceeded))
	require.True(t, ok)
	assert.Equal(t, http.StatusRequestEntityTooLarge, cls.Status)
	assert.Equal(t, DispatchErrorContextWindowExceeded, cls.Kind)
	assert.True(t, cls.Kind.IsClientError())
}

func TestMaybeCompact_Tier3RunsAboveTriggerEvenWhenFitting(t *testing.T) {
	// A history over the trigger but still under the window must be
	// summarized now — waiting until it overflows means no summarizer can
	// ingest it any more (Tier-3 was unreachable against a 1M pool).
	fake := &fakeCompactionSummarizer{summary: "SUMMARY"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(20, 200))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()

	res, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: before + before/10, ClientApp: ClientAppCodex, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.True(t, res.Summarized, "over trigger, under window → summarize")
	assert.Equal(t, 1, fake.calls)
	assert.Zero(t, res.TrimmedToRecent, "fitting request must not be rescue-trimmed")
}

func TestMaybeCompact_Tier3RevertsWhenSummaryRewriteOverflows(t *testing.T) {
	// A fitting request whose tail is nearly the whole window: prepending a
	// summary would push it over, so the rewrite is discarded instead of
	// falling through to rescue trimming.
	fake := &fakeCompactionSummarizer{summary: strings.Repeat("SUMMARY ", 2_000)}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(4, 400))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()

	res, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: before + 10, ClientApp: ClientAppCodex, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, fake.calls)
	assert.False(t, res.Summarized)
	assert.Zero(t, res.TrimmedToRecent)
	assert.Equal(t, DefaultCompactionModel, res.SummaryModel, "summary call is still billed")
	assert.Equal(t, before, env.ContextOverflowTokenEstimate(), "history restored")
}
