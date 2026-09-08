package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLatestInFamily(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		eligible []string
		want     string
	}{
		{"major beats minor", "claude-sonnet-4-6", []string{"claude-sonnet-4-6", "claude-sonnet-5", "claude-opus-5"}, "claude-sonnet-5"},
		{"dated Anthropic alias", "claude-sonnet-4-5-20250929", []string{"claude-sonnet-4-6", "claude-sonnet-5"}, "claude-sonnet-5"},
		{"dated OpenAI alias", "gpt-4.1-mini-2025-04-14", []string{"gpt-5.5-mini"}, "gpt-5.5-mini"},
		{"dated alias cannot bypass eligibility", "claude-sonnet-4-5-20250929", []string{"claude-sonnet-4-5-20250929"}, ""},
		{"Gemini versions", "gemini-3.5-flash", []string{"gemini-3.7-flash", "gemini-3.8-flash", "gemini-3.5-flash-lite"}, "gemini-3.8-flash"},
		{"GLM keeps variant", "z-ai/glm-5.1", []string{"z-ai/glm-5.2", "z-ai/glm-5.3", "z-ai/glm-5.3-flash"}, "z-ai/glm-5.3"},
		{"Kimi major", "moonshotai/kimi-k2.5", []string{"moonshotai/kimi-k2.7", "moonshotai/kimi-k3"}, "moonshotai/kimi-k3"},
		{"OpenAI keeps size", "gpt-5.4-mini", []string{"gpt-5.5-mini", "gpt-5.6-luna", "gpt-6-astra"}, "gpt-5.5-mini"},
		{"newest unavailable", "claude-sonnet-4-5", []string{"claude-sonnet-4-5", "claude-sonnet-4-6"}, "claude-sonnet-4-6"},
		{"no downgrade", "claude-sonnet-5", []string{"claude-sonnet-4-6"}, ""},
		{"unknown ID", "unknown-1", []string{"unknown-1"}, ""},
		{"unversioned catalog ID", "gpt-4o", []string{"gpt-4o", "gpt-5.5"}, "gpt-4o"},
		{"dated unversioned alias", "gpt-4o-2024-08-06", []string{"gpt-4o", "gpt-5.5"}, "gpt-4o"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eligible := make(map[string]struct{}, len(test.eligible))
			for _, model := range test.eligible {
				eligible[model] = struct{}{}
			}
			assert.Equal(t, test.want, LatestInFamily(test.model, func(model string) bool {
				_, allowed := eligible[model]
				return allowed
			}))
		})
	}
}

func TestFamilyAndVersion(t *testing.T) {
	tests := []struct {
		id      string
		family  string
		version [2]int
		ok      bool
	}{
		{"claude-sonnet-4-5", "claude-sonnet", [2]int{4, 5}, true},
		{"claude-sonnet-4-6", "claude-sonnet", [2]int{4, 6}, true},
		{"claude-sonnet-5", "claude-sonnet", [2]int{5, 0}, true},
		{"claude-opus-4-6", "claude-opus", [2]int{4, 6}, true},
		{"claude-opus-4-7", "claude-opus", [2]int{4, 7}, true},
		{"claude-opus-4-8", "claude-opus", [2]int{4, 8}, true},
		{"claude-haiku-4-5", "claude-haiku", [2]int{4, 5}, true},
		{"claude-fable-5", "claude-fable", [2]int{5, 0}, true},
		{"gpt-4.1", "gpt", [2]int{4, 1}, true},
		{"gpt-4.1-mini", "gpt-mini", [2]int{4, 1}, true},
		{"gpt-4.1-nano", "gpt-nano", [2]int{4, 1}, true},
		{"gpt-5", "gpt", [2]int{5, 0}, true},
		{"gpt-5-mini", "gpt-mini", [2]int{5, 0}, true},
		{"gpt-5-nano", "gpt-nano", [2]int{5, 0}, true},
		{"gpt-5-chat", "gpt-chat", [2]int{5, 0}, true},
		{"gpt-5.4", "gpt", [2]int{5, 4}, true},
		{"gpt-5.4-mini", "gpt-mini", [2]int{5, 4}, true},
		{"gpt-5.4-pro", "gpt-pro", [2]int{5, 4}, true},
		{"gpt-5.5", "gpt", [2]int{5, 5}, true},
		{"gpt-5.5-mini", "gpt-mini", [2]int{5, 5}, true},
		{"gpt-5.5-pro", "gpt-pro", [2]int{5, 5}, true},
		{"gpt-4o", "", [2]int{}, false},
		{"gpt-4o-mini", "", [2]int{}, false},
		{"gemini-2.0-flash", "gemini-flash", [2]int{2, 0}, true},
		{"gemini-2.5-flash", "gemini-flash", [2]int{2, 5}, true},
		{"gemini-3.5-flash", "gemini-flash", [2]int{3, 5}, true},
		{"gemini-3-pro-preview", "gemini-pro-preview", [2]int{3, 0}, true},
		{"gemini-3.1-pro-preview", "gemini-pro-preview", [2]int{3, 1}, true},
		{"z-ai/glm-5", "z-ai/glm", [2]int{5, 0}, true},
		{"z-ai/glm-5.1", "z-ai/glm", [2]int{5, 1}, true},
		{"z-ai/glm-5.2", "z-ai/glm", [2]int{5, 2}, true},
		{"z-ai/glm-5.3", "z-ai/glm", [2]int{5, 3}, true},
		{"z-ai/glm-5.3-flash", "z-ai/glm-flash", [2]int{5, 3}, true},
		{"moonshotai/kimi-k2.5", "moonshotai/kimi-k", [2]int{2, 5}, true},
		{"moonshotai/kimi-k2.6", "moonshotai/kimi-k", [2]int{2, 6}, true},
		{"moonshotai/kimi-k2.7", "moonshotai/kimi-k", [2]int{2, 7}, true},
		{"minimax/minimax-m2.7", "minimax/minimax-m", [2]int{2, 7}, true},
		{"minimax/minimax-m3", "minimax/minimax-m", [2]int{3, 0}, true},
		{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v-pro", [2]int{4, 0}, true},
		{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v-flash", [2]int{4, 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			family, version, ok := FamilyAndVersion(tt.id)
			if ok != tt.ok {
				t.Fatalf("FamilyAndVersion(%q) ok = %v, want %v", tt.id, ok, tt.ok)
			}
			if !ok {
				return
			}
			if family != tt.family {
				t.Errorf("FamilyAndVersion(%q) family = %q, want %q", tt.id, family, tt.family)
			}
			if version != tt.version {
				t.Errorf("FamilyAndVersion(%q) version = %v, want %v", tt.id, version, tt.version)
			}
		})
	}
}

func TestFamilyDuplicates(t *testing.T) {
	ids := []string{
		"claude-haiku-4-5",
		"claude-sonnet-4-6",
		"claude-sonnet-5",
		"claude-opus-4-7",
		"claude-opus-4-8",
		"moonshotai/kimi-k2.6",
		"moonshotai/kimi-k2.7",
		"gpt-5.5",
	}
	dups := FamilyDuplicates(ids)
	got := make(map[string]string, len(dups))
	for _, d := range dups {
		got[d.Superseded] = d.SupersededBy
	}
	want := map[string]string{
		"claude-sonnet-4-6":    "claude-sonnet-5",
		"claude-opus-4-7":      "claude-opus-4-8",
		"moonshotai/kimi-k2.6": "moonshotai/kimi-k2.7",
	}
	if len(got) != len(want) {
		t.Fatalf("FamilyDuplicates(%v) = %v, want %v", ids, dups, want)
	}
	for supersededID, wantBy := range want {
		gotBy, ok := got[supersededID]
		if !ok {
			t.Errorf("expected %q to be flagged as superseded, was not", supersededID)
			continue
		}
		if gotBy != wantBy {
			t.Errorf("FamilyDuplicates: %q superseded by %q, want %q", supersededID, gotBy, wantBy)
		}
	}
}

func TestFamilyDuplicates_NoFalsePositiveOnDistinctSizes(t *testing.T) {
	ids := []string{
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-v4-pro",
		"gpt-5.5-mini",
	}
	if dups := FamilyDuplicates(ids); len(dups) != 0 {
		t.Errorf("FamilyDuplicates(%v) = %v, want no duplicates", ids, dups)
	}
}
