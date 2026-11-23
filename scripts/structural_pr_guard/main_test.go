package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsStructural(t *testing.T) {
	tests := []struct {
		title  string
		labels []string
		want   bool
	}{
		{title: "refactor: move pins", want: true},
		{title: "refactor(proxy): move pins", want: true},
		{title: "refactor(proxy)!: move pins", want: true},
		{title: "  refactor: leading space", want: true},
		{title: "fix: pins", labels: []string{"structural"}, want: true},
		{title: "fix: pins", labels: []string{"bug"}, want: false},
		{title: "refactoring: not the prefix", want: false},
		{title: "Refactor: case differs", want: false},
		{title: "feat: refactor later", want: false},
		{title: "refactor(proxy) missing colon", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			assert.Equal(t, tt.want, isStructural(tt.title, tt.labels))
		})
	}
}

func TestProtectedChanges(t *testing.T) {
	changed := []string{
		"internal/proxy/golden_credentials_test.go",
		"internal/proxy/golden_test.go",
		"internal/proxy/golden_fakes_test.go",
		"internal/proxy/conformance_golden_test.go",
		"internal/proxy/testdata/golden/anthropic/basic.json",
		"internal/proxy/testdata/conformance/anthropic/basic_text.golden",
		"internal/proxy/testdata/paid_fallback_rules.golden",
		"internal/proxy/service.go",
		"internal/proxy/conformance_test.go",
		"internal/proxy/testdata/contracts/x.json",
		"internal/proxy/sub/golden_credentials_test.go",
		"docs/golden.md",
	}
	assert.Equal(t, changed[:7], protectedChanges(changed))
}

func TestReportFailsOnlyForStructuralGoldenChanges(t *testing.T) {
	changedFiles := []string{"internal/proxy/service.go", "internal/proxy/golden_credentials_test.go"}
	var out strings.Builder
	assert.False(t, checkStructuralGoldens(&out, "refactor(proxy): split service", nil, changedFiles))
	assert.Contains(t, out.String(), "internal/proxy/golden_credentials_test.go")
	assert.NotContains(t, out.String(), "service.go\n")
	assert.False(t, checkStructuralGoldens(&strings.Builder{}, "fix: pins", []string{"structural"}, changedFiles))
	assert.True(t, checkStructuralGoldens(&strings.Builder{}, "fix: pins", nil, changedFiles))
	assert.True(t, checkStructuralGoldens(&strings.Builder{}, "refactor: split", nil, changedFiles[:1]))
}

func TestReadPathsKeepsUnusualFilenamesVerbatim(t *testing.T) {
	paths, err := readPaths(strings.NewReader("internal/proxy/testdata/golden/caf\u00e9.json\x00internal/proxy/testdata/golden/a b\tc.json\x00"))
	require.NoError(t, err)
	assert.Equal(t, []string{"internal/proxy/testdata/golden/caf\u00e9.json", "internal/proxy/testdata/golden/a b\tc.json"}, paths)
	assert.Equal(t, paths, protectedChanges(paths))
}

func TestReportQuotesFilenamesSoTheyCannotForgeWorkflowCommands(t *testing.T) {
	var out strings.Builder
	assert.False(t, checkStructuralGoldens(&out, "refactor: split", nil, []string{"internal/proxy/testdata/golden/x\n::error::forged.json"}))
	assert.NotContains(t, out.String(), "\n::error::")
	assert.Contains(t, out.String(), `"internal/proxy/testdata/golden/x\n::error::forged.json"`)
}
