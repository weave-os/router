package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const clusterPackagePath = modulePath + "/internal/router/cluster"

// The legacy cluster scorer is being removed. Only the offline report tool and
// the composition root may depend on it; every other importer is listed here
// and the list may only shrink.
var clusterImportRoots = []string{"cmd/router/", "cmd/routing-report/", "internal/router/cluster/"}

var clusterImportAllowlist = []string{
	"internal/api/admin/excluded_models.go",
	"internal/api/admin/routing_distribution.go",
	"internal/api/anthropic/messages_test.go",
	"internal/proxy/compaction_strategy_internal_test.go",
	"internal/proxy/compaction_routing_internal_test.go",
	"internal/proxy/dispatch_error.go",
	"internal/proxy/dispatch_error_test.go",
	"internal/proxy/service_session_pin_test.go",
	"internal/proxy/turnloop.go",
	"internal/server/middleware/cluster_version_override.go",
	"internal/server/middleware/cluster_version_override_test.go",
}

func TestClusterPackageImportsOnlyShrink(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	var importers []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || hasAnyPrefix(rel, clusterImportRoots) {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if importPath == clusterPackagePath {
				importers = append(importers, rel)
				break
			}
		}
		return nil
	})
	require.NoError(t, err)
	sort.Strings(importers)

	allowed := append([]string(nil), clusterImportAllowlist...)
	sort.Strings(allowed)
	require.Equal(t, allowed, importers,
		"only %v may import %s; import shared sentinels and roster types from internal/router instead, and drop allowlist entries that no longer import it",
		clusterImportRoots, clusterPackagePath)
}

func hasAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
