package translate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestLastAnnouncedMessagesModel(t *testing.T) {
	const legacy = "✦ **Weave Router** → first-model · best pick for this turn\n\n"
	const marked = codexResponsesBadgeSentinelForTest + "✦ **Weave Router** → second-model · request classified as low difficulty\n\n"
	body := func(messages ...map[string]any) []byte {
		raw, err := json.Marshal(map[string]any{"model": "auto", "messages": messages})
		require.NoError(t, err)
		return raw
	}
	user := func(content any) map[string]any { return map[string]any{"role": "user", "content": content} }
	assistant := func(content any) map[string]any { return map[string]any{"role": "assistant", "content": content} }
	blocks := func(texts ...string) []map[string]any {
		content := []map[string]any{{"type": "thinking", "thinking": legacy}}
		for _, text := range texts {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		return append(content, map[string]any{"type": "tool_use", "id": "t1", "name": "bash", "input": map[string]any{}})
	}
	for _, test := range []struct {
		name string
		body []byte
		want string
	}{
		{name: "no history", body: body(user("hi"))},
		{name: "chat string content", body: body(user("hi"), assistant(legacy+"done")), want: "first-model"},
		{name: "whitespace-trimmed marker block", body: body(user("hi"), assistant(blocks(strings.TrimSpace(legacy)))), want: "first-model"},
		{name: "unterminated marker followed by prose", body: body(user("hi"), assistant("✦ **Weave Router** → first-model · best\nmore prose"))},
		{name: "messages text block after thinking", body: body(user("hi"), assistant(blocks(legacy+"looking"))), want: "first-model"},
		{name: "provenance-marked notice", body: body(user("hi"), assistant(blocks(marked))), want: "second-model"},
		{name: "latest assistant wins", body: body(user("hi"), assistant(legacy), user("more"), assistant(blocks(marked))), want: "second-model"},
		{name: "unannounced latest falls back to earlier", body: body(user("hi"), assistant(legacy), user("more"), assistant(blocks("plain"))), want: "first-model"},
		{name: "quoted prose", body: body(user("hi"), assistant("Docs say: "+legacy))},
		{name: "only the first text block", body: body(user("hi"), assistant(blocks("plain", legacy)))},
		{name: "user echo", body: body(user(legacy))},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, translate.LastAnnouncedMessagesModel(test.body))
		})
	}
}

func TestLastReplayedResponsesModel(t *testing.T) {
	const legacy = "✦ **Weave Router** → first-model · best pick for this turn\n\n"
	body := func(text string) []byte {
		raw, err := json.Marshal(map[string]any{"model": "auto", "input": []map[string]any{
			{"role": "user", "content": "hi"},
			{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}},
		}})
		require.NoError(t, err)
		return raw
	}
	assert.Equal(t, "first-model", translate.LastReplayedResponsesModel(body(legacy)))
	assert.Equal(t, "first-model", translate.LastReplayedResponsesModel(body(codexResponsesBadgeSentinelForTest+legacy)))
	assert.Empty(t, translate.LastAnnouncedResponsesModel(body(legacy)), "terminal history trusts only provenance-marked badges")
	assert.Empty(t, translate.LastReplayedResponsesModel(body("Docs say: "+legacy)))
}

func TestStripRoutingMarkerFromWhitespaceTrimmedReplay(t *testing.T) {
	const marker = "✦ **Weave Router** → first-model · best pick for this turn"
	body, err := json.Marshal(map[string]any{"messages": []map[string]any{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": []map[string]any{
			{"type": "text", "text": marker},
			{"type": "tool_use", "id": "t1", "name": "bash", "input": map[string]any{}},
		}},
		{"role": "user", "content": "Quoting it: " + marker},
	}})
	require.NoError(t, err)
	stripped, err := translate.StripRoutingMarkerFromMessages(body)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(stripped), marker), "only the marker-only assistant block is router-owned")
	assert.Contains(t, string(stripped), "Quoting it: "+marker)
}
