package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type summaryCtxKey struct{}

type fakeHandoverSummarizer struct {
	summary  string
	err      error
	calls    int
	sawScope string
}

func (f *fakeHandoverSummarizer) Summarize(ctx context.Context, _ *translate.RequestEnvelope, _ router.Request) (string, handover.Usage, error) {
	f.calls++
	f.sawScope, _ = ctx.Value(summaryCtxKey{}).(string)
	return f.summary, handover.Usage{InputTokens: 7}, f.err
}

func (*fakeHandoverSummarizer) Provider() string { return "provider-a" }

type fakeSummarizerAccess struct {
	allowed   bool
	providers []string
}

func (f *fakeSummarizerAccess) SummaryContext(ctx context.Context, provider string) (context.Context, bool) {
	f.providers = append(f.providers, provider)
	return context.WithValue(ctx, summaryCtxKey{}, "scoped"), f.allowed
}

func handoverEnvelope(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{"model":"m","messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"}]}`))
	require.NoError(t, err)
	return env
}

func runHandover(t *testing.T, summarizer handover.Summarizer, access conversation.SummarizerAccess) conversation.HandoverOutcome {
	t.Helper()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := func() time.Time {
		at = at.Add(250 * time.Millisecond)
		return at
	}
	return conversation.CompactionHandover{Summarizer: summarizer, Access: access, Now: now}.Run(context.Background(), handoverEnvelope(t), "decision-model", router.Request{})
}

func TestCompactionHandoverWithoutSummarizerKeepsHistory(t *testing.T) {
	access := &fakeSummarizerAccess{allowed: true}
	out := runHandover(t, nil, access)
	assert.True(t, out.Invoked)
	assert.True(t, out.FallbackToFullHistory)
	assert.Empty(t, access.providers, "no summarizer means no credential lookup")
}

func TestCompactionHandoverTenantBoundarySkipsSummarizer(t *testing.T) {
	summarizer := &fakeHandoverSummarizer{summary: "earlier"}
	access := &fakeSummarizerAccess{}
	out := runHandover(t, summarizer, access)
	assert.True(t, out.FallbackToFullHistory)
	assert.Zero(t, summarizer.calls)
	assert.Equal(t, []string{"provider-a"}, access.providers)
}

func TestCompactionHandoverSummarizesInScopedContext(t *testing.T) {
	summarizer := &fakeHandoverSummarizer{summary: "earlier work summarized"}
	out := runHandover(t, summarizer, &fakeSummarizerAccess{allowed: true})
	assert.False(t, out.FallbackToFullHistory)
	assert.Equal(t, 1, summarizer.calls)
	assert.Equal(t, "scoped", summarizer.sawScope)
	assert.Equal(t, int64(250), out.LatencyMS)
	assert.Equal(t, 5, out.SummaryTokens)
	assert.Equal(t, 7, out.SummaryUsage.InputTokens)
}

func TestCompactionHandoverSummarizerFailureKeepsHistory(t *testing.T) {
	for name, summarizer := range map[string]*fakeHandoverSummarizer{
		"error": {err: errors.New("upstream down")},
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			out := runHandover(t, summarizer, &fakeSummarizerAccess{allowed: true})
			assert.True(t, out.FallbackToFullHistory)
			assert.Zero(t, out.SummaryTokens)
		})
	}
}

func TestEstimateSummaryTokens(t *testing.T) {
	assert.Zero(t, conversation.EstimateSummaryTokens(""))
	assert.Equal(t, 3, conversation.EstimateSummaryTokens("twelve chars"))
}
