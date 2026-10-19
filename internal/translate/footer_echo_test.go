package translate_test

import (
	"encoding/json"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolResultBlock(id string) map[string]any {
	return map[string]any{"type": "tool_result", "tool_use_id": id, "content": "done"}
}

func TestFeedbackFooterSinceLastHumanTurn_ToolResultContinuation(t *testing.T) {
	body := buildBody(t, []map[string]any{
		{"role": "user", "content": []any{textBlock("do the thing")}},
		{"role": "assistant", "content": []any{textBlock("all done" + sampleFooter())}},
		{"role": "user", "content": []any{
			toolResultBlock("toolu_bg1"),
			textBlock("<system-reminder>background task completed</system-reminder>"),
		}},
	})
	assert.True(t, translate.FeedbackFooterSinceLastHumanTurn(body))
}

func TestFeedbackFooterSinceLastHumanTurn_ClaudeCodeTaskNotification(t *testing.T) {
	// Claude Code can append a quoted task result as a text block in a user
	// message after the assistant has already emitted its feedback footer. This
	// wrapper is harness output, not a new human turn, so the footer must remain
	// suppressed for the continuation.
	for _, testCase := range []struct {
		name         string
		notification string
	}{
		{name: "task_notification_tag", notification: "<task-notification><task-id>1</task-id></task-notification>"},
		{name: "quoted_result_wrapper", notification: "</summary>\n<result>background task completed</result>\n<usage><total_tokens>0</total_tokens></usage>"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := buildBody(t, []map[string]any{
				{"role": "user", "content": []any{textBlock("do the thing")}},
				{"role": "assistant", "content": []any{textBlock("all done" + sampleFooter())}},
				{"role": "user", "content": []any{textBlock(testCase.notification)}},
			})
			assert.True(t, translate.FeedbackFooterSinceLastHumanTurn(body))
		})
	}
}

func TestWithoutLeadingClientInjectedText_QuotedResultWrapper(t *testing.T) {
	quotedResult := "</summary>\n<result>background task completed</result>\n<usage><total_tokens>0</total_tokens></usage>"
	assert.Empty(t, translate.WithoutLeadingClientInjectedText(quotedResult))
	assert.Equal(t, "please run the tests", translate.WithoutLeadingClientInjectedText(quotedResult+"please run the tests"))
}

func TestFeedbackFooterSinceLastHumanTurn_NewHumanTurnResets(t *testing.T) {
	body := buildBody(t, []map[string]any{
		{"role": "assistant", "content": []any{textBlock("all done" + sampleFooter())}},
		{"role": "user", "content": []any{textBlock("now do something else")}},
	})
	assert.False(t, translate.FeedbackFooterSinceLastHumanTurn(body))
}

func TestFeedbackFooterSinceLastHumanTurn_SkipsFooterlessContinuations(t *testing.T) {
	// A suppressed continuation response between the footer-bearing answer and
	// the current request must not re-enable the hint.
	body := buildBody(t, []map[string]any{
		{"role": "user", "content": []any{textBlock("do the thing")}},
		{"role": "assistant", "content": []any{textBlock("all done" + sampleFooter())}},
		{"role": "user", "content": []any{toolResultBlock("toolu_bg1")}},
		{"role": "assistant", "content": []any{textBlock("noted the background result")}},
		{"role": "user", "content": []any{toolResultBlock("toolu_bg2")}},
	})
	assert.True(t, translate.FeedbackFooterSinceLastHumanTurn(body))
}

func TestFeedbackFooterSinceLastHumanTurn_NoFooterInHistory(t *testing.T) {
	body := buildBody(t, []map[string]any{
		{"role": "user", "content": []any{textBlock("do the thing")}},
		{"role": "assistant", "content": []any{textBlock("working"), map[string]any{
			"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{},
		}}},
		{"role": "user", "content": []any{toolResultBlock("toolu_1")}},
	})
	assert.False(t, translate.FeedbackFooterSinceLastHumanTurn(body))
}

func TestFeedbackFooterSinceLastHumanTurn_OpenAIStringContent(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5",
		"messages": []map[string]any{
			{"role": "user", "content": "do the thing"},
			{"role": "assistant", "content": "all done" + sampleFooter()},
			{"role": "tool", "tool_call_id": "call_1", "content": "done"},
		},
	})
	require.NoError(t, err)
	assert.True(t, translate.FeedbackFooterSinceLastHumanTurn(body))
}

func TestFeedbackFooterSinceLastHumanTurn_EmptyAndMissing(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"x","messages":[]}`),
		[]byte(`{"model":"x"}`),
		[]byte(`{"model":"x","contents":[]}`),
	} {
		assert.False(t, translate.FeedbackFooterSinceLastHumanTurn(body))
	}
}

func TestFeedbackFooterSinceLastHumanTurnInResponses(t *testing.T) {
	body := []byte("{\"input\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\\n\\n_Weave Router feedback:_ `$rf +` x\"}]}]}")
	assert.True(t, translate.FeedbackFooterSinceLastHumanTurnInResponses(body))
	assert.True(t, translate.FeedbackFooterSinceLastHumanTurn(body))
}
