package policyclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

// turnFactsQuery populates every Go-owned turn fact so the wire fixture below
// records the complete v4 field set (omitempty fields included).
func turnFactsQuery() policy.Query {
	priorOutput := 128
	return policy.Query{
		SchemaVersion:  policy.SchemaVersionV4,
		Strategy:       router.StrategyHMM,
		ExecutionMode:  policy.ExecutionModeServing,
		RouteID:        "route-facts",
		OrganizationID: "org-1",
		InstallationID: "inst-1",
		ClientApp:      "claude-code",
		RolloutID:      "rollout-1",
		PromptText:     "system\nfirst ask\nsecond ask",
		ConversationMessages: []router.ConversationMessage{
			{Role: "system", Text: "You are a coding agent."},
			{Role: "user", Text: "first ask"},
			{Role: "assistant", ToolCalls: []router.ConversationToolCall{{Name: "Read"}, {Name: "Bash"}}},
			{Role: "user", ToolResults: []router.ConversationToolResult{{ToolUseID: "t1", Text: "ok"}}},
			{Role: "assistant", Text: "done", ToolCalls: []router.ConversationToolCall{{Name: "Read"}}},
			{Role: "user", Text: "  second ask  "},
		},
		AvailableTools:  []string{"Bash", "Edit", "Bash"},
		Tools:           []router.ToolDescriptor{{Name: "Bash"}},
		FeedbackKey:     "fk",
		FeedbackRole:    "user",
		ClientSessionID: "sess-1",
		TurnContext: &router.PolicyTurnContext{
			VisibleTurnIndex:    7,
			SessionTurnCount:    9,
			SessionEverSwitched: true,
			TurnType:            "sub_agent_dispatch",
			CacheState:          "warm",
			PriorOutputTokens:   &priorOutput,
		},
		EstimatedInputTokens: 42,
		HasTools:             true,
		HasImages:            false,
		TrainingAllowed:      true,
		CaptureMode:          "full",
		DebugEnabled:         true,
	}
}

func TestClassifierRequestV4CarriesGoOwnedTurnFacts(t *testing.T) {
	body, err := marshalRouteRequest(turnFactsQuery())
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))

	assert.JSONEq(t, `"claude_code"`, string(wire["harness"]))
	assert.JSONEq(t, `"second ask"`, string(wire["latest_user_text"]))
	assert.JSONEq(t, `7`, string(wire["turn_index"]))
	assert.JSONEq(t, `true`, string(wire["is_subagent"]))
	assert.JSONEq(t, `["Bash","Edit","Read"]`, string(wire["available_tools"]))
	assert.JSONEq(t, `["Read","Bash"]`, string(wire["invoked_tools"]))
}

func TestClassifierRequestV4TurnFactsAlwaysPresent(t *testing.T) {
	body, err := marshalRouteRequest(policy.Query{
		SchemaVersion: policy.SchemaVersionV4,
		Strategy:      router.StrategyHMM,
		ConversationMessages: []router.ConversationMessage{
			{Role: "user", ToolResults: []router.ConversationToolResult{{ToolUseID: "t1", Text: "ok"}}},
		},
	})
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))

	assert.JSONEq(t, `"unknown"`, string(wire["harness"]))
	assert.JSONEq(t, `""`, string(wire["latest_user_text"]))
	assert.JSONEq(t, `0`, string(wire["turn_index"]))
	assert.JSONEq(t, `false`, string(wire["is_subagent"]))
	assert.NotContains(t, wire, "available_tools")
	assert.NotContains(t, wire, "invoked_tools")
}

func TestLegacyRouteRequestCarriesGoOwnedTurnFacts(t *testing.T) {
	query := turnFactsQuery()
	query.SchemaVersion = policy.SchemaVersionV3
	body, err := marshalRouteRequest(query)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))

	assert.JSONEq(t, `"claude_code"`, string(wire["harness"]))
	assert.JSONEq(t, `["Bash","Edit","Read"]`, string(wire["available_tools"]))
	assert.JSONEq(t, `["Read","Bash"]`, string(wire["invoked_tools"]))
}

// The fixture is the reviewed list of top-level fields the router places on the
// v4 classifier request. Sidecars import it to prove every field has a reader;
// adding or removing a field here without updating the fixture fails this test.
func TestClassifierRequestV4FieldsMatchFixture(t *testing.T) {
	body, err := marshalRouteRequest(turnFactsQuery())
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &wire))
	got := make([]string, 0, len(wire))
	for field := range wire {
		got = append(got, field)
	}
	sort.Strings(got)

	raw, err := os.ReadFile(filepath.Join("testdata", "classifier_request_v4_fields.txt"))
	require.NoError(t, err)
	var want []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			want = append(want, line)
		}
	}
	assert.Equal(t, want, got, "update testdata/classifier_request_v4_fields.txt and the sidecar reader together")
}
