package translate_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/translate"
)

func TestLastAnnouncedResponsesModel(t *testing.T) {
	const first = codexResponsesBadgeSentinelForTest + "✦ **Weave Router** → first-model · best pick for this turn\n\n"
	const second = codexResponsesBadgeSentinelForTest + "✦ **Weave Router** → second-model · request classified as low difficulty\n\n"
	const warning = codexResponsesBadgeSentinelForTest + "A funding warning.\n\n"
	assistant := func(content any) map[string]any {
		return map[string]any{"type": "message", "role": "assistant", "content": content}
	}
	parts := func(texts ...string) []map[string]string {
		var content []map[string]string
		for _, text := range texts {
			content = append(content, map[string]string{"type": "output_text", "text": text})
		}
		return content
	}
	for _, test := range []struct {
		name  string
		input any
		want  string
	}{
		{name: "no history", input: "a request"},
		{name: "string content", input: []any{assistant(first)}, want: "first-model"},
		{name: "only first text part is provenance", input: []any{assistant(parts(first, second))}, want: "first-model"},
		{name: "latest message", input: []any{assistant(first), assistant(second)}, want: "second-model"},
		{name: "coalesced corrections", input: []any{assistant(first + warning + second + "answer")}, want: "second-model"},
		{name: "ignore later warning", input: []any{assistant(first), assistant(warning)}, want: "first-model"},
		{name: "ignore later answer", input: []any{assistant(first), assistant("answer")}, want: "first-model"},
		{name: "ignore user quotation", input: []any{map[string]any{"type": "message", "role": "user", "content": first}}},
		{name: "ignore tool output", input: []any{map[string]any{"type": "function_call_output", "output": first}}},
		{name: "ignore unmarked prose", input: []any{assistant("✦ **Weave Router** → first-model\n\n")}},
		{name: "ignore embedded quotation", input: []any{assistant("Here is documentation: " + first)}},
		{name: "ignore truncated notice", input: []any{assistant(codexResponsesBadgeSentinelForTest + "✦ **Weave Router** → first-model")}},
		{name: "ignore missing model", input: []any{assistant(codexResponsesBadgeSentinelForTest + "✦ **Weave Router** → \n\nprovider answer")}},
		{name: "legacy notice", input: []any{assistant(codexResponsesBadgeSentinelForTest + "**Weave Router** — first-model ← old-model\n\n")}, want: "first-model"},
		{name: "keep effort identity", input: []any{assistant(codexResponsesBadgeSentinelForTest + "✦ **Weave Router** → first-model:high\n\n")}, want: "first-model:high"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"input": test.input})
			require.NoError(t, err)
			assert.Equal(t, test.want, translate.LastAnnouncedResponsesModel(body))
			if test.want != "" {
				cleaned, err := translate.StripRoutingBadgeFromResponsesInput(body)
				require.NoError(t, err)
				assert.Empty(t, translate.LastAnnouncedResponsesModel(cleaned), "every recognized leading announcement must be removed before dispatch")
			}
		})
	}
}
