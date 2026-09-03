package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkdownTargets(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"docs/guide.md#setup", "path with spaces.md"}, markdownTargets(
		`Read [the guide](docs/guide.md#setup) and [the notes](<path with spaces.md>).`,
	))
	assert.Empty(t, markdownTargets(`[^1]: A citation with prose (not-a-link).`))
}

func TestCheckRelativeTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "guide.md"), nil, 0o644))
	assert.Empty(t, checkRelativeTarget(root, "docs/README.md", "../guide.md#intro", exceptionSet{}))
	assert.Contains(t, checkRelativeTarget(root, "docs/README.md", "missing.md", exceptionSet{}), "does not exist")
	assert.Empty(t, checkRelativeTarget(root, "docs/README.md", "https://example.com/missing.md", exceptionSet{}))
}

func TestCheckSymbolReference(t *testing.T) {
	t.Parallel()
	packages := map[string]map[string]struct{}{"proxy": {"Service": {}, "Route": {}}}
	assert.Empty(t, checkSymbolReference("docs/guide.md", "proxy.Service.Route", packages, exceptionSet{}))
	assert.Contains(t, checkSymbolReference("docs/guide.md", "proxy.OnUpstreamMeta", packages, exceptionSet{}), "does not exist")
	assert.Empty(t, checkSymbolReference("docs/guide.md", "http.Client", packages, exceptionSet{}))
}

func TestCollectDeclarationSymbols(t *testing.T) {
	t.Parallel()
	parsed, err := parser.ParseFile(token.NewFileSet(), "sample.go", `package sample
type Service struct { ExportedField string; hiddenField string }
const ExportedConstant = "value"
func (s *Service) ExportedMethod() {}
`, 0)
	require.NoError(t, err)
	symbols := map[string]struct{}{}
	for _, declaration := range parsed.Decls {
		collectDeclarationSymbols(symbols, declaration)
	}
	assert.Contains(t, symbols, "Service")
	assert.Contains(t, symbols, "ExportedField")
	assert.Contains(t, symbols, "ExportedConstant")
	assert.Contains(t, symbols, "ExportedMethod")
	assert.NotContains(t, symbols, "hiddenField")
}
