package translate_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// Claude Code appends a <system-reminder> nudging the model toward the
// TaskCreate/TaskUpdate tools whenever they go unused. Non-Anthropic models
// obey it literally and spend whole turns on task-list bookkeeping, so when
// those tools are stripped on cross-vendor emit the nudge must go with them.

const taskReminderText = "<system-reminder>\nThe task tools haven't been used recently. Consider updating task status or creating tasks.\nHere are the existing tasks:\n- #1 [in_progress] Fix the bug\n</system-reminder>"

const taskReminderBody = `{
	"model":"claude-sonnet-5",
	"system":"You are Claude Code.",
	"messages":[
		{"role":"user","content":[{"type":"text","text":"fix the failing test"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"FAIL: TestFoo"},
			{"type":"text","text":"` + "<system-reminder>\\nThe task tools haven't been used recently. Consider updating task status or creating tasks.\\nHere are the existing tasks:\\n- #1 [in_progress] Fix the bug\\n</system-reminder>" + `"}
		]},
		{"role":"assistant","content":[{"type":"text","text":"Looking at TestFoo."}]},
		{"role":"user","content":[
			{"type":"text","text":"also the task tools haven't been used recently by anyone, per the user"},
			{"type":"text","text":"` + "<system-reminder>\\nThe task tools haven't been used recently. Consider updating task status or creating tasks.\\n</system-reminder>" + `"}
		]}
	],
	"tools":[
		{"name":"Bash","description":"b","input_schema":{"type":"object"}},
		{"name":"TaskCreate","description":"","input_schema":{"type":"object"}},
		{"name":"TaskUpdate","description":"","input_schema":{"type":"object"}}
	],
	"max_tokens":256
}`

func assertTaskReminderStripped(t *testing.T, body []byte) {
	t.Helper()
	s := string(body)
	assert.Equal(t, 0, strings.Count(s, "Consider updating task status"), "task-tool reminder blocks must not reach a non-Anthropic upstream")
	assert.Contains(t, s, "fix the failing test", "user prompt survives")
	assert.Contains(t, s, "FAIL: TestFoo", "tool_result survives")
	assert.Contains(t, s, "also the task tools haven't been used recently by anyone", "user-authored text mentioning the phrase survives")
	assert.Contains(t, s, "Looking at TestFoo.", "assistant history survives")
}

func TestTaskReminderStripped_OpenAIChat(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(taskReminderBody))
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna", KeepCrossVendorOrchestrationTools: true})
	require.NoError(t, err)

	assertTaskReminderStripped(t, out.Body)
	assert.ElementsMatch(t, []string{"Bash"}, emittedToolNames(t, out.Body))
	assert.Equal(t, 2, out.Stats.CCOnlyToolsStripped)
	assert.Equal(t, 2, out.Stats.CCTaskRemindersStripped)
}

func TestTaskReminderStripped_OpenAIResponses(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(taskReminderBody))
	require.NoError(t, err)

	out, err := env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-5.6-sol", KeepCrossVendorOrchestrationTools: true})
	require.NoError(t, err)

	assertTaskReminderStripped(t, out.Body)
	assert.ElementsMatch(t, []string{"Bash"}, emittedResponsesToolNames(t, out.Body))
	assert.Equal(t, 2, out.Stats.CCTaskRemindersStripped)
}

func TestTaskReminderStripped_Gemini(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(taskReminderBody))
	require.NoError(t, err)

	// A 2.x target: 3.x rejects history whose tool calls lack thoughtSignatures.
	out, err := env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-2.5-pro", KeepCrossVendorOrchestrationTools: true})
	require.NoError(t, err)

	assertTaskReminderStripped(t, out.Body)
	assert.ElementsMatch(t, []string{"Bash"}, emittedGeminiToolNames(t, out.Body))
	assert.Equal(t, 2, out.Stats.CCTaskRemindersStripped)
}

func TestTaskReminderKept_AnthropicPassthrough(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(taskReminderBody))
	require.NoError(t, err)

	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5"})
	require.NoError(t, err)

	assert.Equal(t, 2, strings.Count(string(out.Body), "Consider updating task status"),
		"Anthropic passthrough keeps the task tools and therefore the reminder")
	assert.Equal(t, 0, out.Stats.CCTaskRemindersStripped)
}

func TestTaskReminderKept_WhenNoTaskToolsToStrip(t *testing.T) {
	// The reminder is only dropped alongside the tools it refers to. A body
	// that carries the reminder but no task-list tool schemas (e.g. a client
	// that already removed them) is left alone.
	body := strings.Replace(taskReminderBody,
		`{"name":"TaskCreate","description":"","input_schema":{"type":"object"}},
		{"name":"TaskUpdate","description":"","input_schema":{"type":"object"}}`,
		`{"name":"Skill","description":"","input_schema":{"type":"object"}}`, 1)
	require.NotEqual(t, taskReminderBody, body)
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna"})
	require.NoError(t, err)

	assert.Equal(t, 1, out.Stats.CCOnlyToolsStripped, "Skill is stripped")
	assert.Equal(t, 0, out.Stats.CCTaskRemindersStripped)
	assert.Equal(t, 2, strings.Count(string(out.Body), "Consider updating task status"))
}

// Claude Code 2.1.x appends the reminder to the trailing tool_result's
// content rather than emitting a separate text block.
const taskReminderInToolResultBody = `{
	"model":"claude-sonnet-5",
	"messages":[
		{"role":"user","content":"check the go version"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go version"}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"go version go1.25.9 linux/amd64\n\n<system-reminder>\nThe task tools haven't been used recently. Consider using TaskCreate or TaskUpdate.\n</system-reminder>"}
		]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"go.mod"}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_2","content":[
				{"type":"text","text":"module weave-os/router\n\n<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"},
				{"type":"text","text":"<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"}
			]}
		]},
		{"role":"assistant","content":[{"type":"text","text":"Go 1.25.9."}]},
		{"role":"user","content":"thanks\n\n<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"}
	],
	"tools":[
		{"name":"Bash","description":"b","input_schema":{"type":"object"}},
		{"name":"Read","description":"r","input_schema":{"type":"object"}},
		{"name":"TaskCreate","description":"","input_schema":{"type":"object"}}
	],
	"max_tokens":256
}`

func TestTaskReminderStripped_InsideToolResultContent(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(taskReminderInToolResultBody))
	require.NoError(t, err)

	for name, prepare := range map[string]func() (providers.PreparedRequest, error){
		"OpenAI chat": func() (providers.PreparedRequest, error) {
			return env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna"})
		},
		"OpenAI Responses": func() (providers.PreparedRequest, error) {
			return env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-5.6-sol"})
		},
		"Gemini": func() (providers.PreparedRequest, error) {
			return env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-2.5-pro"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := prepare()
			require.NoError(t, err)
			s := string(out.Body)
			assert.NotContains(t, s, "task tools haven't been used recently")
			assert.NotContains(t, s, "system-reminder")
			assert.Contains(t, s, "go version go1.25.9 linux/amd64", "tool output before the reminder survives")
			assert.Contains(t, s, "module weave-os/router")
			assert.Contains(t, s, "thanks", "user text before the reminder survives")
			assert.NotContains(t, s, "linux/amd64\\n\\n", "trailing padding is cut with the reminder")
			assert.Equal(t, 4, out.Stats.CCTaskRemindersStripped)
		})
	}
}

// A quiet tool (e.g. a Write, or a Bash with no stdout) leaves the reminder as
// the tool_result's entire content. Empty tool output is valid on every
// cross-vendor emit, so the reminder is still cut rather than kept.
const taskReminderIsWholeToolResultBody = `{
	"model":"claude-sonnet-5",
	"messages":[
		{"role":"user","content":"write the file"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"touch a"}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"}
		]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"touch b"}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_2","content":[
				{"type":"text","text":"\n<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"}
			]}
		]}
	],
	"tools":[
		{"name":"Bash","description":"b","input_schema":{"type":"object"}},
		{"name":"TaskCreate","description":"","input_schema":{"type":"object"}}
	],
	"max_tokens":256
}`

func TestTaskReminderStripped_WhenItIsTheWholeToolResult(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(taskReminderIsWholeToolResultBody))
	require.NoError(t, err)

	for name, prepare := range map[string]func() (providers.PreparedRequest, error){
		"OpenAI chat": func() (providers.PreparedRequest, error) {
			return env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna"})
		},
		"OpenAI Responses": func() (providers.PreparedRequest, error) {
			return env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-5.6-sol"})
		},
		"Gemini": func() (providers.PreparedRequest, error) {
			return env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-2.5-pro"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := prepare()
			require.NoError(t, err)
			s := string(out.Body)
			assert.NotContains(t, s, "task tools haven't been used recently")
			assert.NotContains(t, s, "system-reminder")
			assert.Contains(t, s, "write the file")
			assert.Contains(t, s, "touch b", "both tool calls survive with empty results")
			assert.Equal(t, 2, out.Stats.CCTaskRemindersStripped)
		})
	}
}

// Some Claude Code builds emit the reminder as its own role:"system" message
// between turns instead of appending it to a user message. A system message
// that is nothing but the reminder is dropped; one that also carries other
// instructions keeps them.
const taskReminderAsSystemMessageBody = `{
	"model":"claude-sonnet-5",
	"system":"You are Claude Code.",
	"messages":[
		{"role":"user","content":[{"type":"text","text":"check the go version"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go version"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"go version go1.25.9 linux/amd64"}]},
		{"role":"system","content":[{"type":"text","text":"<system-reminder>\nThe task tools haven't been used recently. Consider using TaskCreate or TaskUpdate.\n</system-reminder>"}]},
		{"role":"assistant","content":[{"type":"text","text":"Go 1.25.9."}]},
		{"role":"user","content":"and the module path?"},
		{"role":"system","content":"<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"go.mod"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"module weave-os/router"}]},
		{"role":"system","content":[
			{"type":"text","text":"<system-reminder>\nThe user is in plan mode.\n</system-reminder>"},
			{"type":"text","text":"<system-reminder>\nThe task tools haven't been used recently.\n</system-reminder>"}
		]}
	],
	"tools":[
		{"name":"Bash","description":"b","input_schema":{"type":"object"}},
		{"name":"Read","description":"r","input_schema":{"type":"object"}},
		{"name":"TaskCreate","description":"","input_schema":{"type":"object"}}
	],
	"max_tokens":256
}`

func TestTaskReminderStripped_AsSystemMessage(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(taskReminderAsSystemMessageBody))
	require.NoError(t, err)

	for name, prepare := range map[string]func() (providers.PreparedRequest, error){
		"OpenAI chat": func() (providers.PreparedRequest, error) {
			return env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna"})
		},
		"OpenAI Responses": func() (providers.PreparedRequest, error) {
			return env.PrepareOpenAIResponses(nil, translate.EmitOptions{TargetModel: "gpt-5.6-sol"})
		},
		"Gemini": func() (providers.PreparedRequest, error) {
			return env.PrepareGemini(http.Header{}, translate.EmitOptions{TargetModel: "gemini-2.5-pro"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := prepare()
			require.NoError(t, err)
			s := string(out.Body)
			assert.NotContains(t, s, "task tools haven't been used recently")
			if name != "Gemini" { // the Gemini emitter drops mid-conversation system messages wholesale
				assert.Contains(t, s, "The user is in plan mode.", "other reminders in the same system message survive")
			}
			assert.Contains(t, s, "go version go1.25.9 linux/amd64")
			assert.Contains(t, s, "and the module path?")
			assert.Contains(t, s, "module weave-os/router")
			assert.Contains(t, s, "Go 1.25.9.")
			assert.Equal(t, 3, out.Stats.CCTaskRemindersStripped)
		})
	}
}

func TestTaskReminderKept_WhenItIsTheWholeUserMessage(t *testing.T) {
	// Never leave a user message with empty content.
	body := `{
		"model":"claude-sonnet-5",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[{"type":"text","text":"hello"}]},
			{"role":"user","content":[{"type":"text","text":"` + "<system-reminder>\\nThe task tools haven't been used recently.\\n</system-reminder>" + `"}]}
		],
		"tools":[{"name":"TaskCreate","description":"","input_schema":{"type":"object"}}],
		"max_tokens":64
	}`
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)

	out, err := env.PrepareOpenAI(nil, translate.EmitOptions{TargetModel: "gpt-5.6-luna"})
	require.NoError(t, err)

	assert.Equal(t, 0, out.Stats.CCTaskRemindersStripped)
	assert.Contains(t, string(out.Body), "task tools haven't been used recently")
}
