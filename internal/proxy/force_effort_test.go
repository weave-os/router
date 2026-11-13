package proxy

import "testing"

func TestForcedReasoningEffort(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"grok-4.6", "low"}, // bare pin must not fall to xAI's non-disableable high default
		{"grok-4.5", "low"},
		{"gpt-5.5", ""},
		{"gemini-3.1-pro-preview", ""},
		{"claude-opus-4-8", ""},
		{"deepseek/deepseek-v4-pro", ""},
	}
	for _, tc := range cases {
		if got := forcedReasoningEffort(tc.model); got != tc.want {
			t.Errorf("forcedReasoningEffort(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}
