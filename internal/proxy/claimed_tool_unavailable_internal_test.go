package proxy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"workweave/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claimedToolFakeStore captures RouterFeedbackEvents for method-level tests;
// clusters, so the dedupe path (one insert per [session, role, tool]) is
// directly observable. err supplies insert failures on demand.
type claimedToolFakeStore struct {
	mu     sync.Mutex
	events []RouterFeedbackEvent
	err    error
}

func (f *claimedToolFakeStore) InsertRouterFeedback(ctx context.Context, p RouterFeedbackEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, p)
	return nil
}

func (f *claimedToolFakeStore) snap() []RouterFeedbackEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RouterFeedbackEvent(nil), f.events...)
}

func claimedToolHost() (uuid.UUID, [sessionpin.SessionKeyLen]byte) {
	return uuid.New(), sessionKeyFromString("claimed-tool-test")
}

func claimedToolService(store RouterFeedbackStore) *Service {
	if store == nil {
		store = &claimedToolFakeStore{}
	}
	return &Service{
		feedbackStore:      store,
		claimedToolTracker: newClaimedToolTracker(),
	}
}

func TestDetectClaimedToolUnavailable_Positives(t *testing.T) {
	cases := []struct {
		name string
		text string
		tool string
	}{
		{"no-tool-in-toolset", "There is no ToolSearch tool in my available toolset", "ToolSearch"},
		{"not-directly-callable", "EnterPlanMode is not directly callable, so I'll just edit the file", "EnterPlanMode"},
		{"dont-have-access", "I don't have access to the Read tool right now", "Read"},
		{"isn't-available", "The Bash tool isn't available in this environment", "Bash"},
		{"there-is-no-precede", "sorry, there is no WebSearch tool that I can call", "WebSearch"},
		{"not-exposed", "The Read tool is not exposed in this toolset", "Read"},
		{"tool-name-in-backticks", "the `WebSearch` tool is not callable here", "WebSearch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := detectClaimedToolUnavailable(tc.text, []string{tc.tool})
			assert.Equal(t, []string{tc.tool}, found)
		})
	}
}

func TestDetectClaimedToolUnavailable_WindowBoundary(t *testing.T) {
	// Pins both sides of the ±claimedToolWindowBytes window. With two filler
	// sentences (~100 bytes) the claim phrase starts ~140 bytes after the name
	// and is inside the window; with four (~200 bytes) it falls outside and
	// must not fire.
	filler := "I will run the search and summarize what I find. "
	inside := `That's fine — ToolSearch for the workspace is active, ` +
		strings.Repeat(filler, 2) +
		`but it is not available from this agent, so I'll skip it`
	assert.Equal(t, []string{"ToolSearch"}, detectClaimedToolUnavailable(inside, []string{"ToolSearch"}))

	outside := `That's fine — ToolSearch for the workspace is active, ` +
		strings.Repeat(filler, 4) +
		`but it is not available from this agent, so I'll skip it`
	assert.Empty(t, detectClaimedToolUnavailable(outside, []string{"ToolSearch"}))
}

func TestDetectClaimedToolUnavailable_Negatives(t *testing.T) {
	cases := []struct {
		name string
		text string
		tool string
	}{
		{"tool-praised-normally", "I'll use ToolSearch to load the needed files", "ToolSearch"},
		{"name-as-substring", "MyToolSearchWrapper is not available in this build", "ToolSearch"},
		{"phrase-mismatched-case", "there is no toolsearch tool in my available toolset", "ToolSearch"},
		{"name-flanked-by-ass,no-claim", "ToolSearch_extra is fine", "ToolSearch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := detectClaimedToolUnavailable(tc.text, []string{tc.tool})
			assert.Empty(t, found)
		})
	}
}

func TestDetectClaimedToolUnavailable_PhraseAboutUndeclaredTool(t *testing.T) {
	// "Bash" is claimed unavailable, but only ToolSearch was declared — the
	// scan matches declared names exclusively, so it must not fire.
	found := detectClaimedToolUnavailable(
		"I don't have access to the Bash tool here", []string{"ToolSearch"})
	assert.Empty(t, found)
}

func TestDetectClaimedToolUnavailable_PhraseBeyondWindow(t *testing.T) {
	// The name and the claim are separated by far more than 160 bytes.
	padding := strings.Repeat("lorem ipsum dolor sit amet consectetur ", 60)
	text := "ToolSearch can be tried via Bash. " + padding + "it is not available today."
	found := detectClaimedToolUnavailable(text, []string{"ToolSearch"})
	assert.Empty(t, found)
}

func TestDetectClaimedToolUnavailable_EmptyText(t *testing.T) {
	assert.Empty(t, detectClaimedToolUnavailable("", []string{"ToolSearch"}))
	assert.Empty(t, detectClaimedToolUnavailable("no claim here", nil))
}

func TestDetectClaimedToolUnavailable_DedupesAndCaps(t *testing.T) {
	tools := []string{"Read", "Write", "WebSearch", "Bash", "Task"}
	text := `I don't have access to Read, Write, WebSearch, Bash, or Task here.`
	found := detectClaimedToolUnavailable(text, tools)
	// All five were declared and (within 160 bytes of each name) claimed
	// unavailable — but findings are deduped by name and capped at 4.
	assert.Len(t, found, claimedToolMaxFindings)
	seen := map[string]bool{}
	for _, f := range found {
		assert.False(t, seen[f], "duplicate finding %q", f)
		seen[f] = true
	}
}

func TestClaimedToolUnavailableFromBody_SSEAcrossDeltas(t *testing.T) {
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'd like to use "}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"the ToolSearch "}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tool, but there is "}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"no such tool here."}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"shrug"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	found := claimedToolUnavailableFromBody([]byte(sse), true, []string{"ToolSearch"})
	// The claim is split across four text_delta frames; extraction
	// concatenates them, so the phrase is still detected.
	assert.Equal(t, []string{"ToolSearch"}, found)
}

func TestClaimedToolUnavailableFromBody_SSEMalformedFailsOpen(t *testing.T) {
	// Truncated mid-frame JSON and a bare data line (no space) must not error
	// or panic, and must produce no findings.
	bad := []byte("event: content_block_delta\ndata: {\"type\":\"content_")
	require.NotPanics(t, func() {
		assert.Empty(t, claimedToolUnavailableFromBody(bad, true, []string{"ToolSearch"}))
	})
	assert.Empty(t, claimedToolUnavailableFromBody(
		[]byte("data:{\"type\":\"content_block_delta\"}\ndata: [DONE]"), true, []string{"ToolSearch"}))
}

func TestClaimedToolUnavailableFromBody_NonStreaming(t *testing.T) {
	body := `{
		"id": "msg_2",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "text", "text": "I would normally use this."},
			{"type": "text", "text": " The EnterPlanMode tool is not directly callable from my toolset."},
			{"type": "tool_use", "id": "toolu_1", "name": "Edit", "input": {}}
		]
	}`
	found := claimedToolUnavailableFromBody([]byte(body), false, []string{"EnterPlanMode", "Edit"})
	assert.Equal(t, []string{"EnterPlanMode"}, found)
}

func TestClaimedToolUnavailableFromBody_NonStreamingMalformed(t *testing.T) {
	assert.Empty(t, claimedToolUnavailableFromBody([]byte(`{"content": "notanarray"`), false, []string{"Read"}))
	assert.Empty(t, claimedToolUnavailableFromBody([]byte(`{truncated`), false, []string{"Read"}))
}

func TestMaybeReportClaimedToolUnavailable_InsertsOneEvent(t *testing.T) {
	store := &claimedToolFakeStore{}
	svc := claimedToolService(store)
	installationID, sessionKey := claimedToolHost()
	body := []byte(`{"content":[{"type":"text","text":"There is no ToolSearch tool in my available toolset."}]}`)

	svc.maybeReportClaimedToolUnavailable(
		context.Background(), body, false, []string{"ToolSearch"},
		installationID, sessionKey, "default_high", "claude-sonnet-5", "qwen3",
		"req-1", "route-1", ClientIdentity{ClientApp: ClientAppClaudeCode, SessionID: "sess-1"},
	)

	events := store.snap()
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, installationID.String(), ev.InstallationID)
	assert.Equal(t, []byte(sessionKey[:]), ev.SessionKey)
	assert.Equal(t, "default_high", ev.Role)
	assert.Equal(t, "claude-sonnet-5", ev.RequestedModel)
	assert.Equal(t, "qwen3", ev.ServedModel)
	assert.Equal(t, "down", ev.Rating)
	assert.Equal(t, "claimed-tool-unavailable:ToolSearch", ev.Feedback)
	assert.Equal(t, RouterFeedbackSourceAuto, ev.Source)
	assert.Equal(t, "req-1", ev.RequestID)
	assert.Equal(t, "route-1", ev.RouteID)
	assert.Equal(t, ClientAppClaudeCode, ev.ClientApp)
	assert.Equal(t, "sess-1", ev.SessionID)
}

func TestMaybeReportClaimedToolUnavailable_DedupesSecondCall(t *testing.T) {
	store := &claimedToolFakeStore{}
	svc := claimedToolService(store)
	installationID, sessionKey := claimedToolHost()
	body := []byte(`{"content":[{"type":"text","text":"I don't have access to the Read tool."}]}`)

	svc.maybeReportClaimedToolUnavailable(
		context.Background(), body, false, []string{"Read"},
		installationID, sessionKey, "default_high", "a", "b", "req-1", "route-1", ClientIdentity{})
	svc.maybeReportClaimedToolUnavailable(
		context.Background(), body, false, []string{"Read"},
		installationID, sessionKey, "default_high", "a", "b", "req-2", "route-1", ClientIdentity{})

	events := store.snap()
	require.Len(t, events, 1, "same (session, role, tool) must fire once")
	assert.Equal(t, "req-1", events[0].RequestID)
}

func TestMaybeReportClaimedToolUnavailable_InsertErrorLeavesLRUUnset(t *testing.T) {
	store := &claimedToolFakeStore{}
	svc := claimedToolService(store)
	installationID, sessionKey := claimedToolHost()
	body := []byte(`{"content":[{"type":"text","text":"The Task tool is not callable here."}]}`)

	store.err = errors.New("db down")
	svc.maybeReportClaimedToolUnavailable(
		context.Background(), body, false, []string{"Task"},
		installationID, sessionKey, "default_high", "a", "b", "req-1", "route-1", ClientIdentity{})
	require.Empty(t, store.snap(), "failed insert must not persist a row")

	// Insert failure leaves the LRU unset, so a later turn retries and lands.
	store.err = nil
	svc.maybeReportClaimedToolUnavailable(
		context.Background(), body, false, []string{"Task"},
		installationID, sessionKey, "default_high", "a", "b", "req-2", "route-1", ClientIdentity{})
	events := store.snap()
	require.Len(t, events, 1)
	assert.Equal(t, "claimed-tool-unavailable:Task", events[0].Feedback)
}

func TestMaybeReportClaimedToolUnavailable_NilStoreShortCircuits(t *testing.T) {
	svc := &Service{feedbackStore: nil, claimedToolTracker: newClaimedToolTracker()}
	installationID, sessionKey := claimedToolHost()
	// No store: must not panic, must produce no events.
	require.NotPanics(t, func() {
		svc.maybeReportClaimedToolUnavailable(
			context.Background(), []byte(`{"content":[{"type":"text","text":"no Read tool!"}]}`), false,
			[]string{"Read"}, installationID, sessionKey, "default_high", "a", "b",
			"req-1", "route-1", ClientIdentity{})
	})
}

func TestMaybeReportClaimedToolUnavailable_NilInstallationShortCircuits(t *testing.T) {
	store := &claimedToolFakeStore{}
	svc := claimedToolService(store)
	_, sessionKey := claimedToolHost()
	svc.maybeReportClaimedToolUnavailable(
		context.Background(), []byte(`{"content":[{"type":"text","text":"Read is not available."}]}`), false,
		[]string{"Read"}, uuid.Nil, sessionKey, "default_high", "a", "b",
		"req-1", "route-1", ClientIdentity{})
	assert.Empty(t, store.snap(), "uuid.Nil installation must not persist feedback")
}

func TestMaybeReportClaimedToolUnavailable_NoAvailableToolsShortCircuits(t *testing.T) {
	store := &claimedToolFakeStore{}
	svc := claimedToolService(store)
	installationID, sessionKey := claimedToolHost()
	svc.maybeReportClaimedToolUnavailable(
		context.Background(), []byte(`{"content":[{"type":"text","text":"Read is not available."}]}`), false,
		nil, installationID, sessionKey, "default_high", "a", "b",
		"req-1", "route-1", ClientIdentity{})
	assert.Empty(t, store.snap(), "no declared tools must not produce a finding")
}
