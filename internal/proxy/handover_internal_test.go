package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

// fakeHandoverProvider is the minimal providers.Client surface needed by
// the summarizer test: it can return a canned non-streaming body, sleep
// past a deadline, or surface a non-2xx status.
type fakeHandoverProvider struct {
	respBody    string
	respStatus  int
	sleep       time.Duration
	upstreamErr error
	calls       int
	wireModels  []string
	decisions   []router.Decision
}

func (f *fakeHandoverProvider) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	f.calls++
	f.decisions = append(f.decisions, decision)
	f.wireModels = append(f.wireModels, gjson.GetBytes(prep.Body, "model").String())
	if f.sleep > 0 {
		select {
		case <-time.After(f.sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.upstreamErr != nil {
		return f.upstreamErr
	}
	if f.respStatus == 0 {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(f.respStatus)
	}
	_, _ = io.WriteString(w, f.respBody)
	return nil
}

func (f *fakeHandoverProvider) Passthrough(_ context.Context, _ providers.PreparedRequest, _ http.ResponseWriter, _ *http.Request) error {
	return nil
}

// newTestSummarizer wires fake as the only Anthropic client behind a plan
// resolver whose deployed set contains model, mirroring composition.
func newTestSummarizer(t *testing.T, fake providers.Client, model string, timeout time.Duration) *ProviderSummarizer {
	t.Helper()
	if model == "" {
		model = DefaultHandoverModel
	}
	available := map[string]struct{}{providers.ProviderAnthropic: {}}
	deployed := map[string]struct{}{model: {}}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		deployed, available, func(m catalog.Model) string { return m.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	executor, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: fake}))
	require.NoError(t, err)
	return NewProviderSummarizer(plans, executor, providers.ProviderAnthropic, model, timeout)
}

// sampleConversation is the test fixture used across the cases. Both
// system and a couple of message turns so buildHandoverRequestBody has
// real content to flatten.
const sampleConversation = `{
  "model": "claude-opus-4-7",
  "system": "You are a helpful assistant.",
  "messages": [
    {"role": "user", "content": "Step 1?"},
    {"role": "assistant", "content": "Done."},
    {"role": "user", "content": "Step 2?"}
  ]
}`

// canonicalAnthropicResponse is what a real non-streaming Anthropic
// /v1/messages response looks like. The summarizer's job is to extract
// the text from the content[] blocks.
const canonicalAnthropicResponse = `{
  "id": "msg_test_001",
  "type": "message",
  "role": "assistant",
  "model": "claude-haiku-4-5",
  "stop_reason": "end_turn",
  "content": [
    {"type": "text", "text": "Refactor in progress: step 1 done, step 2 pending."}
  ],
  "usage": {"input_tokens": 42, "output_tokens": 17}
}`

func TestProviderSummarizer_SuccessReturnsAssistantText(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	fake := &fakeHandoverProvider{
		respBody:   canonicalAnthropicResponse,
		respStatus: http.StatusOK,
	}
	s := newTestSummarizer(t, fake, "", 200*time.Millisecond)

	got, _, err := s.Summarize(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "Refactor in progress: step 1 done, step 2 pending.", got)
}

func TestProviderSummarizer_TimeoutReturnsError(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	fake := &fakeHandoverProvider{
		respBody: canonicalAnthropicResponse,
		// Sleep longer than the summarizer's timeout.
		sleep: 200 * time.Millisecond,
	}
	s := newTestSummarizer(t, fake, "", 25*time.Millisecond)

	got, _, err := s.Summarize(context.Background(), env)
	require.Error(t, err)
	assert.Empty(t, got)
	// Either the ctx.Err() bubble or the fake's own ctx-aware return both
	// surface DeadlineExceeded.
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "expected DeadlineExceeded, got %v", err)
}

func TestProviderSummarizer_Non2xxReturnsError(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	fake := &fakeHandoverProvider{
		respBody:   `{"error":"oops"}`,
		respStatus: http.StatusInternalServerError,
	}
	s := newTestSummarizer(t, fake, "", 200*time.Millisecond)

	got, _, err := s.Summarize(context.Background(), env)
	require.Error(t, err)
	assert.Empty(t, got)
	assert.True(t, strings.Contains(err.Error(), "500"), "error must mention upstream status 500; got %v", err)
}

func TestProviderSummarizer_EmptyContentReturnsErrEmptySummary(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	// Successful 200 but no text blocks (e.g. truncated, or a stop
	// reason with empty content[]).
	fake := &fakeHandoverProvider{
		respBody:   `{"id":"msg_empty","content":[]}`,
		respStatus: http.StatusOK,
	}
	s := newTestSummarizer(t, fake, "", 200*time.Millisecond)

	got, _, err := s.Summarize(context.Background(), env)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptySummary)
	assert.Empty(t, got)
}

func TestProviderSummarizer_NilEnvelopeReturnsError(t *testing.T) {
	t.Parallel()

	fake := &fakeHandoverProvider{}
	s := newTestSummarizer(t, fake, "", 200*time.Millisecond)

	_, _, err := s.Summarize(context.Background(), nil)
	require.Error(t, err)
}

func TestProviderSummarizer_WireModelMatchesPlanTarget(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	fake := &fakeHandoverProvider{respBody: canonicalAnthropicResponse, respStatus: http.StatusOK}
	s := newTestSummarizer(t, fake, "", 200*time.Millisecond)

	_, usage, err := s.Summarize(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, 1, fake.calls)
	assert.Equal(t, []string{DefaultHandoverModel}, fake.wireModels)
	assert.Equal(t, DefaultHandoverModel, fake.decisions[0].Model)
	assert.Equal(t, providers.ProviderAnthropic, fake.decisions[0].Provider)
	assert.Equal(t, DefaultHandoverModel, usage.Model)
	assert.Equal(t, providers.ProviderAnthropic, usage.Provider)
}

func TestProviderSummarizer_UnreviewedModelFailsBeforeIO(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	fake := &fakeHandoverProvider{respBody: canonicalAnthropicResponse, respStatus: http.StatusOK}
	// claude-opus-4-7 is deployed but not in the handover policy's reviewed set.
	s := newTestSummarizer(t, fake, "claude-opus-4-7", 200*time.Millisecond)

	got, _, err := s.Summarize(context.Background(), env)
	require.Error(t, err)
	var resolution *policy.ResolutionError
	require.ErrorAs(t, err, &resolution)
	assert.Equal(t, policy.ResolutionErrorInvalidOverride, resolution.Code)
	assert.Empty(t, got)
	assert.Equal(t, 0, fake.calls, "no upstream I/O after a failed plan resolution")
}

func TestProviderSummarizer_Non2xxIsNotRetried(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	fake := &fakeHandoverProvider{respBody: `{"error":"busy"}`, respStatus: http.StatusServiceUnavailable}
	s := newTestSummarizer(t, fake, "", 2*time.Second)

	_, _, err = s.Summarize(context.Background(), env)
	require.Error(t, err)
	assert.Equal(t, 1, fake.calls, "policy budget allows one attempt")
}

func TestProviderSummarizer_AttemptEventsCarryRequestID(t *testing.T) {
	t.Parallel()

	env, err := translate.ParseAnthropic([]byte(sampleConversation))
	require.NoError(t, err)

	fake := &fakeHandoverProvider{respBody: canonicalAnthropicResponse, respStatus: http.StatusOK}
	available := map[string]struct{}{providers.ProviderAnthropic: {}}
	deployed := map[string]struct{}{DefaultHandoverModel: {}}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		deployed, available, func(m catalog.Model) string { return m.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)
	var events []inference.AttemptEvent
	executor, err := dispatch.NewExecutor(
		dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: fake}),
		dispatch.WithAttemptSink(dispatch.AttemptSinkFunc(func(_ context.Context, event inference.AttemptEvent) {
			events = append(events, event)
		})),
	)
	require.NoError(t, err)
	s := NewProviderSummarizer(plans, executor, providers.ProviderAnthropic, DefaultHandoverModel, 200*time.Millisecond)

	ctx := observability.WithRequestID(context.Background(), "req-handover-1")
	_, _, err = s.Summarize(ctx, env)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "req-handover-1", events[0].RequestID)
}
