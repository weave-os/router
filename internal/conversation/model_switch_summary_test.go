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

type switchScopeKey struct{}

type switchSummarizer struct {
	summary  string
	err      error
	calls    int
	sawScope string
}

func (f *switchSummarizer) Summarize(ctx context.Context, _ *translate.RequestEnvelope, _ router.Request) (string, handover.Usage, error) {
	f.calls++
	f.sawScope, _ = ctx.Value(switchScopeKey{}).(string)
	return f.summary, handover.Usage{OutputTokens: 9}, f.err
}

func (*switchSummarizer) Provider() string { return "provider-b" }

type switchAccess struct {
	allowed   bool
	providers []string
}

func (f *switchAccess) SummaryContext(ctx context.Context, provider string) (context.Context, bool) {
	f.providers = append(f.providers, provider)
	return context.WithValue(ctx, switchScopeKey{}, "scoped"), f.allowed
}

func runSwitchSummary(t *testing.T, summarizer handover.Summarizer, access conversation.SummarizerAccess) (conversation.HandoverOutcome, bool, *translate.RequestEnvelope) {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{"model":"m","messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"}]}`))
	require.NoError(t, err)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := func() time.Time {
		at = at.Add(time.Second)
		return at
	}
	out, rewritten := conversation.ModelSwitchSummary{Summarizer: summarizer, Access: access, Now: now}.Run(context.Background(), env, router.Request{}, "pin-model", "fresh-model")
	return out, rewritten, env
}

func TestModelSwitchSummaryWithoutSummarizerKeepsHistory(t *testing.T) {
	access := &switchAccess{allowed: true}
	out, rewritten, _ := runSwitchSummary(t, nil, access)
	assert.True(t, out.Invoked)
	assert.True(t, out.FallbackToFullHistory)
	assert.False(t, rewritten)
	assert.Empty(t, access.providers)
}

func TestModelSwitchSummaryTenantBoundarySkipsSummarizer(t *testing.T) {
	summarizer := &switchSummarizer{summary: "earlier"}
	access := &switchAccess{}
	out, rewritten, _ := runSwitchSummary(t, summarizer, access)
	assert.True(t, out.FallbackToFullHistory)
	assert.False(t, rewritten)
	assert.Zero(t, summarizer.calls)
	assert.Equal(t, []string{"provider-b"}, access.providers)
}

func TestModelSwitchSummaryRewritesEnvelope(t *testing.T) {
	summarizer := &switchSummarizer{summary: "the summary text"}
	out, rewritten, env := runSwitchSummary(t, summarizer, &switchAccess{allowed: true})
	assert.True(t, rewritten)
	assert.False(t, out.FallbackToFullHistory)
	assert.Equal(t, "scoped", summarizer.sawScope)
	assert.Equal(t, int64(1000), out.LatencyMS)
	assert.Equal(t, 4, out.SummaryTokens)
	assert.Equal(t, 9, out.SummaryUsage.OutputTokens)
	prep, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "fresh-model"})
	require.NoError(t, err)
	assert.Contains(t, string(prep.Body), "the summary text")
}

func TestModelSwitchSummaryFailureKeepsHistory(t *testing.T) {
	for name, summarizer := range map[string]*switchSummarizer{
		"error": {err: errors.New("upstream down")},
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			out, rewritten, _ := runSwitchSummary(t, summarizer, &switchAccess{allowed: true})
			assert.True(t, out.Invoked)
			assert.True(t, out.FallbackToFullHistory)
			assert.False(t, rewritten)
		})
	}
}
