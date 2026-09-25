package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/compactioncheckpoint"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fakeCompactionSummarizer struct {
	summary    string
	usage      handover.Usage
	usages     []handover.Usage
	err        error
	calls      int
	lastModel  string
	lastSource policy.OverrideSource
	failOnCall int
}

type memoryCompactionCheckpoints struct {
	checkpoint compactioncheckpoint.Checkpoint
	writes     int
	reads      int
	getErr     error
	putErr     error
}

func (m *memoryCompactionCheckpoints) Get(_ context.Context, identity string, key [16]byte, endpoint string) (compactioncheckpoint.Checkpoint, bool, error) {
	m.reads++
	if m.getErr != nil {
		return compactioncheckpoint.Checkpoint{}, false, m.getErr
	}
	cp := m.checkpoint
	return cp, m.writes > 0 && cp.CredentialIdentity == identity && cp.SessionKey == key && cp.Endpoint == endpoint && cp.ExpiresAt.After(time.Now()), nil
}

func (m *memoryCompactionCheckpoints) Upsert(_ context.Context, cp compactioncheckpoint.Checkpoint) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.checkpoint = cp
	m.writes++
	return nil
}

func (f *fakeCompactionSummarizer) SummarizeForCompaction(_ context.Context, _ *translate.RequestEnvelope, target CompactionTarget, _ router.Request, _ int) (string, handover.Usage, error) {
	f.calls++
	f.lastModel = target.CatalogID
	f.lastSource = target.Source
	usage := f.usage
	if f.calls <= len(f.usages) {
		usage = f.usages[f.calls-1]
	}
	if f.calls == f.failOnCall {
		return "", usage, errors.New("summary provider unavailable")
	}
	return f.summary, usage, f.err
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

func incidentShapeAnthropicBody() []byte {
	var sb strings.Builder
	sb.WriteString(`{"model":"claude-opus-5-5","messages":[`)
	for i := range 575 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"role":"assistant","content":[{"type":"text","text":"`)
		sb.WriteString(strings.Repeat("a", 8000))
		sb.WriteString(`"},{"type":"tool_use","id":"`)
		sb.WriteString(fmt.Sprintf("t%d", i))
		sb.WriteString(`","name":"read","input":{}}`)
		if i < 33 {
			sb.WriteString(`,{"type":"tool_use","id":"`)
			sb.WriteString(fmt.Sprintf("extra%d", i))
			sb.WriteString(`","name":"read","input":{}}`)
		}
		sb.WriteString(`]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"`)
		sb.WriteString(fmt.Sprintf("t%d", i))
		sb.WriteString(`","content":"`)
		sb.WriteString(strings.Repeat("b", 1000))
		sb.WriteString(`"}`)
		if i < 33 {
			sb.WriteString(`,{"type":"tool_result","tool_use_id":"`)
			sb.WriteString(fmt.Sprintf("extra%d", i))
			sb.WriteString(`","content":"`)
			sb.WriteString(strings.Repeat("b", 1000))
			sb.WriteString(`"}`)
		}
		sb.WriteString(`]}`)
	}
	sb.WriteString(`,{"role":"user","content":"Continue with the task."}]}`)
	return []byte(sb.String())
}

func TestMaybeCompact_IncidentShapeNoSilentHistoryLoss(t *testing.T) {
	body := incidentShapeAnthropicBody()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	cleaned := env.Clone()
	require.Equal(t, 603, cleaned.ClearOldToolResults(5))
	window := cleaned.ContextOverflowTokenEstimate() - 373
	require.Greater(t, cleaned.ContextOverflowTokenEstimate(), 1_000_000)

	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct}
	res, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: window, Headers: http.Header{},
	})
	require.ErrorIs(t, err, ErrContextWindowExceeded)
	assert.Zero(t, res.TrimmedToRecent)
	assert.Equal(t, cleaned.RoutingFeatures(false).MessageCount, env.RoutingFeatures(false).MessageCount)
}

func TestMaybeCompact_IncidentShapeSummarizesInChunks(t *testing.T) {
	body := incidentShapeAnthropicBody()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	cleaned := env.Clone()
	require.Equal(t, 603, cleaned.ClearOldToolResults(5))
	window := cleaned.ContextOverflowTokenEstimate() - 373

	fake := &fakeCompactionSummarizer{summary: "Preserved decisions and tool results"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	result, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode, MaxWindow: window, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.True(t, result.Summarized)
	assert.GreaterOrEqual(t, fake.calls, 2)
	assert.Zero(t, result.TrimmedToRecent)
	assert.LessOrEqual(t, result.FinalEstimate, window)
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
	require.NoError(t, err)
	messages := gjson.GetBytes(prepared.Body, "messages").Array()
	assert.Less(t, len(messages), len(gjson.GetBytes(body, "messages").Array()))
	assert.Contains(t, string(prepared.Body), "Preserved decisions and tool results")
	assert.Contains(t, string(prepared.Body), "Continue with the task.")
}

func TestMaybeCompact_ReusesOnlyMatchingSessionPrefixAndPolicy(t *testing.T) {
	body := alternatingAnthropicBody(32, 400)
	first, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	window := first.ContextOverflowTokenEstimate() + first.ContextOverflowTokenEstimate()/10
	store := &memoryCompactionCheckpoints{}
	fake := &fakeCompactionSummarizer{summary: "Decisions from preceding turns"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake, compactionCheckpoints: store}
	in := compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode,
		CredentialIdentity: "key-a", SessionKey: [16]byte{1},
		Endpoint: string(router.EndpointAnthropicMessages), MaxWindow: window,
		Headers: http.Header{},
	}
	firstResult, err := s.maybeCompact(context.Background(), first, in)
	require.NoError(t, err)
	require.True(t, firstResult.Summarized)
	require.Equal(t, 1, store.writes)
	require.Positive(t, store.checkpoint.Boundary)
	initial := store.checkpoint
	s = &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake, compactionCheckpoints: store}

	resume := strings.TrimSuffix(string(body), `]}`) + `,{"role":"user","content":"next request"}]}`
	second, err := translate.ParseAnthropic([]byte(resume))
	require.NoError(t, err)
	secondResult, err := s.maybeCompact(context.Background(), second, in)
	require.NoError(t, err)
	require.True(t, secondResult.CheckpointReused)
	assert.Equal(t, 1, fake.calls)
	prepared, err := second.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
	require.NoError(t, err)
	assert.Contains(t, string(prepared.Body), "Decisions from preceding turns")
	assert.Contains(t, string(prepared.Body), "next request")

	for _, tc := range []struct {
		name       string
		body       string
		edit       func(*compactionInput)
		checkpoint func(*memoryCompactionCheckpoints)
	}{
		{name: "forked prefix", body: strings.Replace(resume, `"system":"sys"`, `"system":"changed"`, 1)},
		{name: "other credential", body: resume, edit: func(in *compactionInput) { in.CredentialIdentity = "key-b" }},
		{name: "other session", body: resume, edit: func(in *compactionInput) { in.SessionKey = [16]byte{2} }},
		{name: "other endpoint", body: resume, edit: func(in *compactionInput) { in.Endpoint = string(router.EndpointOpenAIChat) }},
		{name: "other policy", body: resume, edit: func(in *compactionInput) { in.Scope.ExcludedModels = map[string]struct{}{"claude-haiku-4-5": {}} }},
		{name: "expired", body: resume, checkpoint: func(store *memoryCompactionCheckpoints) { store.checkpoint.ExpiresAt = time.Now().Add(-time.Second) }},
		{name: "invalid boundary", body: resume, checkpoint: func(store *memoryCompactionCheckpoints) { store.checkpoint.Boundary++ }},
		{name: "empty summary", body: resume, checkpoint: func(store *memoryCompactionCheckpoints) { store.checkpoint.Summary = " " }},
		{name: "store failure", body: resume, checkpoint: func(store *memoryCompactionCheckpoints) { store.getErr = errors.New("db unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.checkpoint = initial
			store.getErr = nil
			if tc.checkpoint != nil {
				tc.checkpoint(store)
			}
			candidate, parseErr := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, parseErr)
			request := in
			if tc.edit != nil {
				tc.edit(&request)
			}
			summaryCalls := fake.calls
			result, compactErr := s.maybeCompact(context.Background(), candidate, request)
			require.NoError(t, compactErr)
			assert.False(t, result.CheckpointReused)
			assert.Greater(t, fake.calls, summaryCalls)
		})
	}
}

func TestMaybeCompact_CheckpointKeepsToolResultPrefixStable(t *testing.T) {
	base := []byte(strings.Replace(string(toolHeavyAnthropicBody(32, 500)),
		`{"role":"assistant","content":[{"type":"tool_use","id":"t26"`,
		`{"role":"user","content":"Continue reading."},{"role":"assistant","content":[{"type":"tool_use","id":"t26"`, 1))
	cleaned, err := translate.ParseAnthropic(base)
	require.NoError(t, err)
	require.Positive(t, cleaned.ClearOldToolResults(5))
	store := &memoryCompactionCheckpoints{}
	fake := &fakeCompactionSummarizer{summary: "Preserved decisions"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake, compactionCheckpoints: store}
	in := compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppCodex,
		CredentialIdentity: "key-a", SessionKey: [16]byte{1},
		Endpoint:  string(router.EndpointAnthropicMessages),
		MaxWindow: cleaned.ContextOverflowTokenEstimate() + 100,
		Headers:   http.Header{},
	}
	appendPair := func(body []byte, id int) []byte {
		return []byte(strings.TrimSuffix(string(body), `]}`) +
			fmt.Sprintf(`,{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"%s"}]}]}`, id, id, strings.Repeat("y", 500)))
	}
	messages := func(env *translate.RequestEnvelope) []gjson.Result {
		prepared, prepErr := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
		require.NoError(t, prepErr)
		return gjson.GetBytes(prepared.Body, "messages").Array()
	}

	first, err := translate.ParseAnthropic(base)
	require.NoError(t, err)
	result, err := s.maybeCompact(context.Background(), first, in)
	require.NoError(t, err)
	require.True(t, result.Summarized)
	require.Equal(t, 1, store.writes)
	firstMessages := messages(first)
	require.Greater(t, len(firstMessages), 6)

	second, err := translate.ParseAnthropic(appendPair(base, 32))
	require.NoError(t, err)
	result, err = s.maybeCompact(context.Background(), second, in)
	require.NoError(t, err)
	require.True(t, result.CheckpointReused)
	secondMessages := messages(second)
	require.Equal(t, firstMessages[:len(firstMessages)-1], secondMessages[:len(firstMessages)-1])

	third, err := translate.ParseAnthropic(appendPair(appendPair(base, 32), 33))
	require.NoError(t, err)
	result, err = s.maybeCompact(context.Background(), third, in)
	require.NoError(t, err)
	require.True(t, result.CheckpointReused)
	thirdMessages := messages(third)
	require.Equal(t, secondMessages[:len(secondMessages)-1], thirdMessages[:len(secondMessages)-1])
	assert.Equal(t, 1, fake.calls)
	assert.Equal(t, 1, store.writes)
}

func TestMaybeCompact_GeminiCheckpointRejectsChangedSystemInstruction(t *testing.T) {
	var body strings.Builder
	body.WriteString(`{"systemInstruction":{"parts":[{"text":"original policy"}]},"contents":[`)
	for i := range 32 {
		if i > 0 {
			body.WriteByte(',')
		}
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		body.WriteString(`{"role":"` + role + `","parts":[{"text":"` + strings.Repeat("x", 400) + `"}]}`)
	}
	body.WriteString(`]}`)
	first, err := translate.ParseGemini([]byte(body.String()))
	require.NoError(t, err)
	store := &memoryCompactionCheckpoints{}
	fake := &fakeCompactionSummarizer{summary: "Earlier Gemini context"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake, compactionCheckpoints: store}
	in := compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppGeminiCLI,
		CredentialIdentity: "key-a", SessionKey: [16]byte{1},
		Endpoint:  string(router.EndpointGeminiGenerate),
		MaxWindow: first.ContextOverflowTokenEstimate() * 11 / 10,
		Headers:   http.Header{},
	}
	result, err := s.maybeCompact(context.Background(), first, in)
	require.NoError(t, err)
	require.True(t, result.Summarized)
	require.Equal(t, 1, store.writes)

	matching, err := translate.ParseGemini([]byte(body.String()))
	require.NoError(t, err)
	result, err = s.maybeCompact(context.Background(), matching, in)
	require.NoError(t, err)
	assert.True(t, result.CheckpointReused)
	require.Equal(t, 1, fake.calls)

	changed, err := translate.ParseGemini([]byte(strings.Replace(body.String(), "original policy", "updated policy", 1)))
	require.NoError(t, err)
	result, err = s.maybeCompact(context.Background(), changed, in)
	require.NoError(t, err)
	assert.False(t, result.CheckpointReused)
	assert.Equal(t, 2, fake.calls)
}

func TestMaybeCompact_UnverifiedClientsKeepOriginalHistory(t *testing.T) {
	for _, clientApp := range []string{"", "unknown", "pi", ClientAppCursor} {
		t.Run(clientApp, func(t *testing.T) {
			body := alternatingAnthropicBody(32, 400)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			before, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
			require.NoError(t, err)
			fake := &fakeCompactionSummarizer{summary: "unverified summary"}
			s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
			result, err := s.maybeCompact(context.Background(), env, compactionInput{
				TurnType: turntype.MainLoop, ClientApp: clientApp,
				MaxWindow: env.ContextOverflowTokenEstimate() / 2, Headers: http.Header{},
			})
			require.ErrorIs(t, err, ErrContextWindowExceeded)
			assert.False(t, result.Summarized)
			assert.Zero(t, fake.calls)
			prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
			require.NoError(t, err)
			assert.JSONEq(t, string(before.Body), string(prepared.Body))
		})
	}
}

func TestMaybeCompact_MidConversationInstructionsStayInPlace(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"initial"},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"developer","content":"new constraint"},{"role":"user","content":"second"}]}`)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	fake := &fakeCompactionSummarizer{summary: "Earlier answer"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	result, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppOpencode, MaxWindow: 10, Headers: http.Header{},
	})
	require.ErrorIs(t, err, ErrContextWindowExceeded)
	assert.False(t, result.Summarized)
	assert.Zero(t, fake.calls)
	prepared, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-4o"})
	require.NoError(t, err)
	assert.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(prepared.Body, "messages").Raw)
}

func TestMaybeCompact_FourteenPercentOverflowSummarizes(t *testing.T) {
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(40, 400))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()
	window := before * 100 / 114
	fake := &fakeCompactionSummarizer{summary: "Preserved previous decisions"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	result, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode,
		MaxWindow: window, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.True(t, result.Summarized)
	assert.Equal(t, 1, fake.calls)
	assert.LessOrEqual(t, result.FinalEstimate, window)
}

func TestMaybeCompact_CheckpointFailuresDoNotLoseHistory(t *testing.T) {
	body := alternatingAnthropicBody(32, 400)
	source, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	store := &memoryCompactionCheckpoints{putErr: errors.New("db unavailable")}
	fake := &fakeCompactionSummarizer{summary: "Remember the decisions"}
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake, compactionCheckpoints: store}
	in := compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode,
		CredentialIdentity: "key-a", SessionKey: [16]byte{1},
		Endpoint:  string(router.EndpointAnthropicMessages),
		MaxWindow: source.ContextOverflowTokenEstimate() + source.ContextOverflowTokenEstimate()/10,
		Headers:   http.Header{},
	}
	result, err := s.maybeCompact(context.Background(), source, in)
	require.NoError(t, err)
	assert.True(t, result.Summarized)
	assert.Zero(t, store.writes)

	store.putErr = nil
	retry, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = s.maybeCompact(context.Background(), retry, in)
	require.NoError(t, err)
	require.Equal(t, 1, store.writes)

	in.MaxWindow = 100
	oversized, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	before, err := oversized.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
	require.NoError(t, err)
	result, err = s.maybeCompact(context.Background(), oversized, in)
	require.ErrorIs(t, err, ErrContextWindowExceeded)
	assert.False(t, result.CheckpointReused)
	after, err := oversized.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
	require.NoError(t, err)
	assert.JSONEq(t, string(before.Body), string(after.Body))
}

func TestMaybeCompact_PartialChunkFailureRetainsHistoryAndUsage(t *testing.T) {
	env, err := translate.ParseAnthropic(incidentShapeAnthropicBody())
	require.NoError(t, err)
	before, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
	require.NoError(t, err)
	cleaned := env.Clone()
	require.Equal(t, 603, cleaned.ClearOldToolResults(5))

	fake := &fakeCompactionSummarizer{
		summary:    "Preserved task state",
		usage:      handover.Usage{InputTokens: 12, OutputTokens: 3},
		failOnCall: 2,
	}
	service := &Service{compactionTriggerPct: DefaultCompactionTriggerPct, compactionSummarizer: fake}
	result, err := service.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode, MaxWindow: cleaned.ContextOverflowTokenEstimate() - 373,
		Headers: http.Header{},
	})
	require.ErrorIs(t, err, ErrContextWindowExceeded)
	assert.Equal(t, 2, fake.calls)
	assert.Equal(t, 24, result.SummaryUsage.InputTokens)
	assert.Equal(t, 6, result.SummaryUsage.OutputTokens)
	after, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-opus-5-5"})
	require.NoError(t, err)
	assert.JSONEq(t, string(before.Body), string(after.Body))
}

func TestMaybeCompact_EmptyFailedChunkStillBillsPriorUsage(t *testing.T) {
	env, err := translate.ParseAnthropic(incidentShapeAnthropicBody())
	require.NoError(t, err)
	cleaned := env.Clone()
	require.Equal(t, 603, cleaned.ClearOldToolResults(5))
	firstUsage := auxTestUsage()
	fake := &fakeCompactionSummarizer{
		summary:    "Preserved task state",
		usages:     []handover.Usage{firstUsage, {}},
		failOnCall: 2,
	}
	s, billingRepo, telemetryRepo := auxTestService(t)
	s.compactionTriggerPct = DefaultCompactionTriggerPct
	s.compactionSummarizer = fake
	result, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode,
		MaxWindow: cleaned.ContextOverflowTokenEstimate() - 373,
		Headers:   http.Header{},
	})
	require.ErrorIs(t, err, ErrContextWindowExceeded)
	require.Equal(t, 2, fake.calls)
	assert.Equal(t, firstUsage.Model, result.SummaryUsage.Model)
	assert.Equal(t, firstUsage.Provider, result.SummaryUsage.Provider)
	s.billCompactionSummaries(auxTestContext(uuid.New(), auxTestSessionID), auxTestRequestID, auxTestOrgID, result.SummaryUsages)
	debits := billingRepo.snapshot()
	require.Len(t, debits, 1)
	assert.Equal(t, firstUsage.Model, debits[0].RouterModel)
	assert.Equal(t, auxTestRequestID+auxSuffixPrecompactionSummary, debits[0].RouterRequestID)
	assert.Len(t, telemetryRepo.waitForRows(1), 1)
}

func TestCompactionSummaryChunk_PreparedAnthropicStartsWithUser(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		parse func([]byte) (*translate.RequestEnvelope, error)
	}{
		{"anthropic", `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"second"}]}`, translate.ParseAnthropic},
		{"openai", `{"messages":[{"role":"system","content":"rules"},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"second"}]}`, translate.ParseOpenAI},
		{"gemini", `{"contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"second"}]}]}`, translate.ParseGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := tc.parse([]byte(tc.body))
			require.NoError(t, err)
			boundaries := env.CompactionBoundaries()
			chunk, err := env.CompactionSummaryChunk(boundaries[1], boundaries[len(boundaries)-1], "prior decisions")
			require.NoError(t, err)
			prepared, err := buildSummaryRequestBody(chunk, policy.PrecompactionDefaultModel, compactionInstruction, DefaultCompactionMaxTokens)
			require.NoError(t, err)
			assert.Equal(t, "user", gjson.GetBytes(prepared, "messages.0.role").String())
			assert.Contains(t, string(prepared), "prior decisions")
		})
	}
}

func TestSelectCompactionSummarizer_MeasuresPreparedProjection(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","tools":[{"name":"read","description":"` +
		strings.Repeat("x", 1_000_000) +
		`","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"Keep this"}]}`)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	require.Greater(t, env.ContextOverflowTokenEstimate(), 200_000)
	model := (&Service{}).selectCompactionSummarizer(env, "", nil)
	assert.Equal(t, policy.PrecompactionDefaultModel, model)
	projected, err := compactionSummaryEstimate(env, model)
	require.NoError(t, err)
	assert.Less(t, projected, 2_000)
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
	res, err := s.maybeCompact(context.Background(), env, compactionInput{TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode, OutputReserve: 0, MaxWindow: 900, Headers: http.Header{}})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.True(t, res.Summarized)
	assert.Equal(t, policy.PrecompactionDefaultModel, res.SummaryModel, "no warm Anthropic pin → Sonnet-class default")
	assert.Equal(t, 1, fake.calls)
	assert.Equal(t, policy.PrecompactionDefaultModel, fake.lastModel)
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

func TestMaybeCompact_AuthoritativePolicyAllowsAuxiliarySummarizer(t *testing.T) {
	strategy := router.Strategy("authoritative-compaction-test")
	fake := &fakeCompactionSummarizer{summary: "Preserved context"}
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

	result, err := s.maybeCompact(ctx, env, compactionInput{TurnType: turntype.MainLoop, ClientApp: ClientAppClaudeCode, OutputReserve: 100, MaxWindow: 1_200, Headers: http.Header{}})

	require.NoError(t, err)
	assert.Equal(t, 1, fake.calls)
	assert.True(t, result.Summarized)
	assert.Zero(t, result.TrimmedToRecent)
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
	small, err := translate.ParseAnthropic(alternatingAnthropicBody(2, 2_000))
	require.NoError(t, err)
	large, err := translate.ParseAnthropic(alternatingAnthropicBody(2, 600_000))
	require.NoError(t, err)
	tooLarge, err := translate.ParseAnthropic(alternatingAnthropicBody(2, 2_100_000))
	require.NoError(t, err)
	assert.Equal(t, policy.PrecompactionDefaultModel, s.selectCompactionSummarizer(small, "", nil))
	assert.Equal(t, "claude-opus-5-5", s.selectCompactionSummarizer(large, "", nil))
	assert.Empty(t, s.selectCompactionSummarizer(tooLarge, "", nil))

	assert.Equal(t, "claude-opus-5-5", s.selectCompactionSummarizer(small, "claude-opus-4-8", nil))
	assert.Equal(t, policy.PrecompactionDefaultModel, s.selectCompactionSummarizer(small, "claude-haiku-4-5", nil))
	assert.Equal(t, policy.PrecompactionDefaultModel, s.selectCompactionSummarizer(small, "gpt-5.5", nil))
	assert.Equal(t, "claude-opus-5-5", s.selectCompactionSummarizer(large, "claude-opus-4-8", nil))

	custom := &Service{compactionModel: "claude-sonnet-4-5"}
	assert.Equal(t, "claude-sonnet-5", custom.selectCompactionSummarizer(small, "", nil), "ROUTER_COMPACTION_MODEL selects a family, not an obsolete version")

	excluded := map[string]struct{}{policy.PrecompactionDefaultModel: {}}
	assert.Equal(t, "claude-sonnet-4-6", s.selectCompactionSummarizer(small, "claude-sonnet-4-5", excluded))
	excluded[policy.PrecompactionLargeWindowModel] = struct{}{}
	assert.Equal(t, "claude-opus-5-5", s.selectCompactionSummarizer(large, "claude-sonnet-4-5", excluded), "excluding one family member still upgrades to a newer one")
	excluded["claude-opus-5-5"] = struct{}{}
	assert.Empty(t, s.selectCompactionSummarizer(large, "claude-sonnet-4-5", excluded))
}

func TestCompactionTargetFor_TypesTheCascadeChoice(t *testing.T) {
	s := &Service{}
	assert.Equal(t, CompactionTarget{CatalogID: "claude-opus-4-8", Source: policy.OverrideSourceSession}, s.compactionTargetFor("claude-opus-4-8", "claude-opus-4-8"))
	assert.Equal(t, CompactionTarget{CatalogID: policy.PrecompactionLargeWindowModel, Source: policy.OverrideSourceSession}, s.compactionTargetFor(policy.PrecompactionLargeWindowModel, "claude-opus-4-8"))
	assert.Equal(t, CompactionTarget{CatalogID: policy.PrecompactionDefaultModel, Source: policy.OverrideSourceDeployment}, s.compactionTargetFor(policy.PrecompactionDefaultModel, "claude-opus-4-8"))
	assert.Equal(t, CompactionTarget{CatalogID: policy.PrecompactionLargeWindowModel}, s.compactionTargetFor(policy.PrecompactionLargeWindowModel, ""))
	custom := &Service{compactionModel: "claude-sonnet-4-5"}
	assert.Equal(t, CompactionTarget{CatalogID: "claude-sonnet-4-5", Source: policy.OverrideSourceDeployment}, custom.compactionTargetFor("claude-sonnet-4-5", ""))
	assert.Equal(t, CompactionTarget{CatalogID: policy.PrecompactionDefaultModel, Source: policy.OverrideSourceDeployment}, custom.compactionTargetFor(policy.PrecompactionDefaultModel, ""))
	assert.Equal(t, CompactionTarget{}, s.compactionTargetFor("", ""))
}

func TestPrecompactionPolicyReviewsEveryCascadeCandidate(t *testing.T) {
	spec, ok := policy.DefaultRegistry().Spec(policy.PurposePrecompactionSummary)
	require.True(t, ok)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionDefaultModel)
	assert.Contains(t, spec.FixedCatalogModels, policy.PrecompactionLargeWindowModel)
	for _, m := range catalog.Models {
		anthropic := false
		for _, b := range m.Providers {
			anthropic = anthropic || b.Provider == providers.ProviderAnthropic
		}
		if anthropic && m.Tier != catalog.TierLow {
			assert.Contains(t, spec.FixedCatalogModels, m.ID, "warm-pin candidate %s must be a reviewed summarizer", m.ID)
		}
	}
}

func TestCompactionPolicyFor(t *testing.T) {
	assert.True(t, compactionPolicyFor(ClientAppClaudeCode).DeferToClient, "Claude Code auto-compacts itself")
	assert.False(t, compactionPolicyFor(ClientAppCodex).DeferToClient, "Codex gets the router cascade")
	assert.False(t, compactionPolicyFor(ClientAppGeminiCLI).DeferToClient)
	assert.Equal(t, defaultCompactionPolicy, compactionPolicyFor(""), "unknown client → default policy")
	assert.Equal(t, defaultCompactionPolicy, compactionPolicyFor("some-new-harness"))
}

func TestCompactionSummaryHonorsAllowlistOutsideRoutingPool(t *testing.T) {
	env, err := translate.ParseAnthropic(alternatingAnthropicBody(4, 100))
	require.NoError(t, err)
	for _, test := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"unrestricted auxiliary model", context.Background(), policy.PrecompactionDefaultModel},
		{"org allowlist", ctxWithAllowedModels(testSol), ""},
		{"request subset", ctxWithRequestSubset(context.Background(), testSol), ""},
		{"allowed large-window fallback", ctxWithAllowedModels(testSol, testOpus), testOpus},
	} {
		t.Run(test.name, func(t *testing.T) {
			summarizer := &fakeCompactionSummarizer{summary: "preserved session context"}
			s := &Service{compactionSummarizer: summarizer, availableModels: map[string]struct{}{testSol: {}}}
			summary, _, _, model, ok := s.runCompactionSummary(test.ctx, env, "", router.Request{}, nil)
			assert.Equal(t, test.want != "", ok)
			assert.Equal(t, test.want, model)
			if test.want == "" {
				assert.Empty(t, summary)
				assert.Zero(t, summarizer.calls)
			} else {
				assert.Equal(t, "preserved session context", summary)
			}
		})
	}
}

func TestClientWouldCompact(t *testing.T) {
	cc := compactionPolicyFor(ClientAppClaudeCode)
	// Pool serves the 200K window the client sizes against: the client's
	// own auto-compact (at 167K) fires before the router needs to.
	assert.True(t, clientWouldCompact(cc, smallClientBudget(), 200_000))
	// Pool's largest window is below the client's compaction point: router
	// must compact or the request dead-ends.
	assert.False(t, clientWouldCompact(cc, smallClientBudget(), 128_000))
	assert.False(t, clientWouldCompact(compactionPolicyFor(ClientAppCodex), smallClientBudget(), 1_000_000), "non-deferring harness never defers")
	assert.False(t, clientWouldCompact(cc, router.ClientBudget{}, 200_000), "unknown requested model → no deferral")
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
		TurnType: turntype.MainLoop, MaxWindow: 200_000, ClientBudget: smallClientBudget(), ClientApp: ClientAppClaudeCode, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.True(t, res.DeferredToClient, "Claude Code compacts itself at the default threshold; pool serves that window")
	assert.False(t, res.Applied)
	assert.Equal(t, before, env.ContextOverflowTokenEstimate(), "env must be untouched when deferred")

	// Same shape from Codex: the router owns compaction.
	env2, err := translate.ParseAnthropic(toolHeavyAnthropicBody(20, 300))
	require.NoError(t, err)
	res, err = s.maybeCompact(context.Background(), env2, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: 200_000, ClientApp: ClientAppCodex, Headers: http.Header{},
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
		TurnType: turntype.MainLoop, MaxWindow: 128_000, ClientBudget: smallClientBudget(), ClientApp: ClientAppClaudeCode, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.False(t, res.DeferredToClient)
	assert.True(t, res.Applied)
}

func TestMaybeCompact_BudgetOnlyVersionDoesNotDeferOnOverflow(t *testing.T) {
	s := &Service{compactionTriggerPct: DefaultCompactionTriggerPct}
	env, err := translate.ParseAnthropic(toolHeavyAnthropicBody(20, 300))
	require.NoError(t, err)
	before := env.ContextOverflowTokenEstimate()
	res, err := s.maybeCompact(context.Background(), env, compactionInput{
		TurnType: turntype.MainLoop, MaxWindow: before * 3 / 4, ClientBudget: smallClientBudget(), ClientApp: ClientAppClaudeCode, Headers: http.Header{},
	})
	require.NoError(t, err)
	assert.False(t, res.DeferredToClient)
	assert.Positive(t, res.ToolResultsCleared)
}

func TestMaybeCompact_ClaudeCodeVerifiedOverflowRecovery(t *testing.T) {
	fake := &fakeCompactionSummarizer{summary: "SUMMARY"}
	s := &Service{compactionTriggerPct: 0.01, compactionSummarizer: fake}
	body := toolHeavyAnthropicBody(20, 300)
	budget := resolveClientBudget(ClientIdentity{ClientApp: ClientAppClaudeCode, UserAgent: "claude-cli/2.1.282 (external, sdk-ts)"}, nil, testOpus, false)
	require.Equal(t, "2.1.282", budget.Version)
	for _, tt := range []struct {
		name     string
		window   func(int) int
		overflow bool
	}{
		{"fits", func(needed int) int { return needed + 1 }, false},
		{"overflow", func(needed int) int { return needed / 2 }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			before := env.ContextOverflowTokenEstimate()
			res, err := s.maybeCompact(context.Background(), env, compactionInput{
				TurnType: turntype.MainLoop, MaxWindow: tt.window(before), ClientBudget: budget, ClientApp: ClientAppClaudeCode, Headers: http.Header{},
			})
			if tt.overflow {
				require.ErrorIs(t, err, ErrClientCompactionRequired)
				cls, classified := ClassifyDispatchError(err)
				require.True(t, classified)
				assert.Equal(t, http.StatusRequestEntityTooLarge, cls.Status)
				assert.Regexp(t, `^prompt is too long`, cls.Message)
			} else {
				require.NoError(t, err)
			}
			assert.True(t, res.DeferredToClient)
			assert.False(t, res.Applied)
			assert.Equal(t, before, env.ContextOverflowTokenEstimate())
			assert.Zero(t, fake.calls)
		})
	}

	for _, version := range []string{"2.1.281", "2.1.283"} {
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		unknown := router.ClientBudget{Version: version}
		res, _ := s.maybeCompact(context.Background(), env, compactionInput{
			TurnType: turntype.MainLoop, MaxWindow: env.ContextOverflowTokenEstimate() / 2,
			ClientBudget: unknown, ClientApp: ClientAppClaudeCode, Headers: http.Header{},
		})
		assert.False(t, res.DeferredToClient, "unknown version %s must use router compaction", version)
		assert.Positive(t, res.ToolResultsCleared)
	}
}

func TestCompactionHardPin(t *testing.T) {
	s := &Service{compactionHardPinEnabled: true}
	var key [sessionpin.SessionKeyLen]byte
	ctx := context.Background()

	p, m, source, ok := s.compactionHardPin(ctx, key, "", router.Request{})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, policy.PrecompactionDefaultModel, m, "no pin → Sonnet-class default")
	assert.Equal(t, policy.OverrideSourceDeployment, source, "the deployment's compaction model fixed the turn")

	_, _, _, ok = s.compactionHardPin(ctx, key, "", router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}})
	assert.False(t, ok, "Anthropic disabled for the tenant → fall back to generic hard-pin")

	_, _, _, ok = s.compactionHardPin(ctx, key, "", router.Request{GatewayProviders: map[string]struct{}{providers.ProviderOpenRouter: {}}})
	assert.False(t, ok, "gateway-exclusive tenant → fall back to generic hard-pin")

	_, _, _, ok = s.compactionHardPin(ctx, key, "", router.Request{ExcludedModels: map[string]struct{}{policy.PrecompactionDefaultModel: {}}})
	assert.False(t, ok, "excluded default with no pin → fall back to generic hard-pin")

	unavailable := &Service{compactionHardPinEnabled: true, availableModels: map[string]struct{}{"claude-haiku-4-5": {}}}
	_, _, _, ok = unavailable.compactionHardPin(ctx, key, "", router.Request{})
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
	// turn stays in the Sol family (upgraded to its newest version) instead of
	// crossing to the Anthropic summarizer.
	hmmServed := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s := &Service{compactionHardPinEnabled: true, pinStore: hmmServed, clients: dispatch.NewClients(openAIProviders)}
	p, m, source, ok := s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenAI, p)
	assert.Equal(t, "gpt-6-sol", m)
	assert.Equal(t, policy.OverrideSourceSession, source, "the session's own model fixed the turn")

	// Claude Code's compaction turn is Anthropic-format: the same history keeps
	// the Sonnet-class summarizer.
	p, m, source, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, router.Request{ClientApp: ClientAppClaudeCode})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, policy.PrecompactionDefaultModel, m)
	assert.Equal(t, policy.OverrideSourceDeployment, source)

	// The most recently served model wins when the thread pin and HMM history disagree.
	switched := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole:                 {Provider: providers.ProviderOpenAI, Model: "gpt-5.6-terra", LastServedModel: "gpt-5.6-terra", LastTurnEndedAt: time.Now().Add(-time.Minute), PinnedUntil: live},
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderOpenAI, LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: switched, clients: dispatch.NewClients(openAIProviders)}
	_, m, _, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, "gpt-6-sol", m)

	// An expired thread pin no longer speaks for the session.
	expired := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", LastServedModel: "gpt-5.6-sol", LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(-time.Hour)},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: expired, clients: dispatch.NewClients(openAIProviders)}
	p, m, _, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, policy.PrecompactionDefaultModel, m)

	// A tenant that turned OpenAI off cannot keep the thread there.
	s = &Service{compactionHardPinEnabled: true, pinStore: switched, clients: dispatch.NewClients(openAIProviders)}
	_, m, _, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}}}))
	require.True(t, ok)
	assert.Equal(t, policy.PrecompactionDefaultModel, m, "served vendor disabled → Sonnet-class default")

	// Org exclusions and the deployment-wide automatic disable still apply.
	solFamily := map[string]struct{}{"gpt-5.6-sol": {}, "gpt-6-sol": {}}
	_, m, _, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{ExcludedModels: solFamily}))
	require.True(t, ok)
	assert.Equal(t, policy.PrecompactionDefaultModel, m)
	_, m, _, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{AutomaticExcludedModels: solFamily}))
	require.True(t, ok)
	assert.Equal(t, policy.PrecompactionDefaultModel, m)

	// An older low-tier model may have a newer mid-tier successor.
	lowServed := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {Provider: providers.ProviderOpenAI, LastServedModel: "gpt-4.1-mini", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: lowServed, clients: dispatch.NewClients(openAIProviders)}
	p, m, _, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenAI, p)
	assert.Equal(t, "gpt-5.5-mini", m)

	// An Anthropic-served Codex session keeps its own model, as before.
	anthropicServed := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: {Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", LastServedModel: "claude-opus-4-8", LastTurnEndedAt: time.Now(), PinnedUntil: live},
	}}
	s = &Service{compactionHardPinEnabled: true, pinStore: anthropicServed, clients: dispatch.NewClients(openAIProviders)}
	p, m, _, ok = s.compactionHardPin(ctx, key, sessionpin.DefaultRole, codex(router.Request{}))
	require.True(t, ok)
	assert.Equal(t, providers.ProviderAnthropic, p)
	assert.Equal(t, "claude-opus-5-5", m)
}

func TestCompactionHardPin_CodexEffortQualifiedSessionModel(t *testing.T) {
	const sessionModel = "gpt-5.6-luna"
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {
			Provider: providers.ProviderOpenAI, LastServedModel: sessionModel + ":xhigh",
			LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
		},
	}}
	s := &Service{
		compactionHardPinEnabled: true,
		pinStore:                 store,
		clients:                  dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: nil}),
		availableModels:          map[string]struct{}{sessionModel: {}},
	}

	provider, model, source, ok := s.compactionHardPin(context.Background(), [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, router.Request{ClientApp: ClientAppCodex})
	require.True(t, ok)
	assert.Equal(t, providers.ProviderOpenAI, provider)
	assert.Equal(t, sessionModel, model)
	assert.Equal(t, policy.OverrideSourceSession, source)
}

func TestCompactionHardPin_FamilyUpgradeHonorsRestrictions(t *testing.T) {
	const olderModel = "z-ai/glm-5.2"
	const newerModel = "z-ai/glm-5.3"
	ctx := context.Background()
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(sessionpin.DefaultRole): {
			Provider: providers.ProviderFireworks, LastServedModel: olderModel,
			LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
		},
	}}
	s := &Service{pinStore: store, clients: dispatch.NewClients(map[string]providers.Client{providers.ProviderFireworks: nil})}
	for _, test := range []struct {
		name string
		req  router.Request
		want string
	}{
		{"upgrade", router.Request{}, newerModel},
		{"org exclusion", router.Request{ExcludedModels: map[string]struct{}{newerModel: {}}}, olderModel},
		{"automatic exclusion", router.Request{AutomaticExcludedModels: map[string]struct{}{newerModel: {}}}, olderModel},
		{"provider disabled", router.Request{EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, model, ok := s.compactionSessionModel(ctx, [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, test.req)
			assert.Equal(t, test.want != "", ok)
			assert.Equal(t, test.want, model)
		})
	}
	s.availableModels = map[string]struct{}{olderModel: {}}
	_, model, ok := s.compactionSessionModel(ctx, [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, router.Request{})
	require.True(t, ok)
	assert.Equal(t, olderModel, model)
}

func TestCompactionHardPin_CodexKeepsUntieredSessionFamily(t *testing.T) {
	for _, sessionModel := range []string{"gpt-4o", "gpt-5-chat"} {
		t.Run(sessionModel, func(t *testing.T) {
			store := &rolePinStore{byRole: map[string]sessionpin.Pin{
				hmmHistoryRole(sessionpin.DefaultRole): {
					Provider: providers.ProviderOpenAI, LastServedModel: sessionModel,
					LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
				},
			}}
			s := &Service{
				pinStore:        store,
				clients:         dispatch.NewClients(map[string]providers.Client{providers.ProviderOpenAI: nil}),
				availableModels: map[string]struct{}{sessionModel: {}},
			}
			provider, model, _, ok := s.compactionHardPin(context.Background(), [sessionpin.SessionKeyLen]byte{}, sessionpin.DefaultRole, router.Request{ClientApp: ClientAppCodex})
			require.True(t, ok)
			assert.Equal(t, providers.ProviderOpenAI, provider)
			assert.Equal(t, sessionModel, model)
		})
	}
}

func TestTurnLoop_CompactionReadsClientIdentityBeforeHardPin(t *testing.T) {
	const sessionModel = "gpt-6-sol"
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		hmmHistoryRole(roleForTier(catalog.TierMid)): {
			Provider: providers.ProviderOpenAI, LastServedModel: sessionModel,
			LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour),
		},
	}}
	s := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: nil, providers.ProviderAnthropic: nil}, nil, false, nil, store, false, providers.ProviderAnthropic, policy.PrecompactionDefaultModel, nil)
	s.compactionHardPinEnabled = true
	env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a summary."}],"max_tokens":4096}`))
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	features := env.RoutingFeatures(false)
	turn, err := s.runTurnLoop(ctx, env, features, "test-key", uuid.Nil, "", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, providers.ProviderOpenAI, turn.Decision.Provider)
	assert.Equal(t, sessionModel, turn.Decision.Model)
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
	assert.Equal(t, policy.PrecompactionDefaultModel, res.SummaryModel, "summary call is still billed")
	assert.Equal(t, before, env.ContextOverflowTokenEstimate(), "history restored")
}
