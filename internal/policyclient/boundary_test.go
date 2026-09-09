package policyclient

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
)

func TestRouteMessagesPreserveTaskAtEveryPosition(t *testing.T) {
	for _, count := range []int{95, 96, 97, 193} {
		for position := 0; position < count; position++ {
			t.Run(fmt.Sprintf("messages_%d_boundary_%d", count, position), func(t *testing.T) {
				messages := make([]router.ConversationMessage, count)
				for i := range messages {
					messages[i] = router.ConversationMessage{Role: "assistant", Text: strings.Repeat("context", 500)}
				}
				messages[position] = router.ConversationMessage{Role: "user", Text: "Fix the actual task 界🙂"}
				got := routeMessages(messages)
				assert.Equal(t, "Fix the actual task 界🙂", latestUserText(got))
				assert.LessOrEqual(t, len(got), 96)
				bytes := 0
				for _, message := range got {
					bytes += len(message.Text)
					assert.LessOrEqual(t, len(message.Text), 3000)
					assert.True(t, utf8.ValidString(message.Text), "invalid UTF-8 at boundary %d", position)
				}
				assert.LessOrEqual(t, bytes, 48000)
			})
		}
	}
}

func TestClipRouteTextPreservesUTF8(t *testing.T) {
	for _, test := range []struct {
		text  string
		limit int
		want  string
	}{
		{"界🙂x", 1, ""},
		{"界🙂x", 3, "界"},
		{"界🙂x", 6, "界"},
		{"界🙂x", 7, "界🙂"},
		{" a界 z ", 3, "a"},
	} {
		assert.Equal(t, test.want, clipRouteText(test.text, test.limit))
	}
}

func TestRouteMessagesDoNotInventUserText(t *testing.T) {
	messages := []router.ConversationMessage{
		{Role: "system", Text: "not the task"},
		{Role: "user", Text: "  "},
		{Role: "assistant", Text: "assistant context"},
		{Role: "user", ToolResults: []router.ConversationToolResult{{ToolUseID: "call", Text: "not a user instruction"}}},
	}
	assert.Empty(t, latestUserText(routeMessages(messages)))
	messages[3].Text = "Actual mixed-content task"
	assert.Equal(t, "Actual mixed-content task", latestUserText(routeMessages(messages)))
}

func FuzzRouteMessagesPreserveBoundary(f *testing.F) {
	f.Add(uint8(37), "Restore the task 界🙂")
	f.Add(uint8(0), "x")
	f.Fuzz(func(t *testing.T, index uint8, task string) {
		if !utf8.ValidString(task) || strings.TrimSpace(task) == "" {
			t.Skip()
		}
		messages := make([]router.ConversationMessage, 97)
		for i := range messages {
			messages[i] = router.ConversationMessage{Role: "assistant", Text: strings.Repeat("a", 3000)}
		}
		messages[int(index)%len(messages)] = router.ConversationMessage{Role: "user", Text: task}
		want := clipRouteText(task, 3000)
		assert.Equal(t, want, latestUserText(routeMessages(messages)))
	})
}
