package architecture_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const (
	proxyImportFixturePackage = "./internal/architecture/testdata/proxy_import"
	proxyImportRemediation    = "only internal/proxy, internal/api, internal/server and cmd/ may import internal/proxy/...; move the shared code into the inner-ring context that owns it (.agents/skills/where-does-this-go) or declare a port there (.agents/skills/add-context-port)"
)

var proxyImporterRoots = []string{proxyPackagePath, modulePath + "/internal/api", modulePath + "/internal/server", modulePath + "/cmd"}

// proxyImportAllowlist lists importers that predate the rule. Entries only go away.
var proxyImportAllowlist = map[string]bool{
	"internal/requestcontext -> internal/proxy":                 true,
	"scripts/classifier_session_check -> internal/proxy":        true,
	"scripts/feedback_integration_check -> internal/proxy":      true,
	"scripts/subscription_verification_check -> internal/proxy": true,
}

func TestOnlyServingLayerImportsProxy(t *testing.T) {
	measured := measureProxyImports(t, "./...")
	assert.Empty(t, compareProxyImports(measured, proxyImportAllowlist), proxyImportRemediation)
}

func TestProxyImportRatchetDetectsSyntheticViolation(t *testing.T) {
	assert.Equal(t, map[string]bool{"internal/architecture/testdata/proxy_import -> internal/proxy": true},
		measureProxyImports(t, proxyImportFixturePackage))
}

func TestProxyImportScopeCoversSubpackagesAndExemptsServingLayer(t *testing.T) {
	for path, want := range map[string]bool{
		proxyPackagePath:                     true,
		proxyPackagePath + "/usage":          true,
		modulePath + "/internal/proxyfacts":  false,
		modulePath + "/internal/credentials": false,
	} {
		assert.Equal(t, want, hasPathPrefix(path, proxyPackagePath), path)
	}
	for path, exempt := range map[string]bool{
		proxyPackagePath + "_test":                      true,
		proxyPackagePath + "/usage":                     true,
		modulePath + "/internal/api/admin_test":         true,
		modulePath + "/internal/server/middleware":      true,
		modulePath + "/cmd/router":                      true,
		modulePath + "/internal/postgres":               false,
		modulePath + "/internal/credentials/usage_test": false,
		modulePath + "/internal/subscriptions":          false,
		modulePath + "/scripts/seat_usage_check":        false,
	} {
		assert.Equal(t, exempt, proxyImporterExempt(path), path)
	}
}

func TestProxyImportAllowlistRejectsNewEdgesAndStaleEntries(t *testing.T) {
	allowlist := map[string]bool{"internal/a -> internal/proxy": true}
	assert.Empty(t, compareProxyImports(map[string]bool{"internal/a -> internal/proxy": true}, allowlist))
	assert.Equal(t, []string{"internal/a -> internal/proxy: no longer imported: delete its proxyImportAllowlist entry"},
		compareProxyImports(map[string]bool{}, allowlist))
	assert.Equal(t, []string{"internal/b -> internal/proxy/usage: new import of the serving orchestrator"},
		compareProxyImports(map[string]bool{"internal/a -> internal/proxy": true, "internal/b -> internal/proxy/usage": true}, allowlist))
}

func proxyImporterExempt(pkgPath string) bool {
	path := strings.TrimSuffix(pkgPath, "_test")
	return slices.ContainsFunc(proxyImporterRoots, func(root string) bool { return hasPathPrefix(path, root) })
}

func measureProxyImports(t *testing.T, patterns ...string) map[string]bool {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{Dir: repositoryRoot(t), Mode: packages.NeedName | packages.NeedImports, Tests: true}, patterns...)
	require.NoError(t, err)
	require.NotEmpty(t, loaded)
	edges := map[string]bool{}
	for _, pkg := range loaded {
		if strings.HasSuffix(pkg.PkgPath, ".test") || proxyImporterExempt(pkg.PkgPath) {
			continue
		}
		for importPath := range pkg.Imports {
			if hasPathPrefix(importPath, proxyPackagePath) {
				edges[relativePackage(strings.TrimSuffix(pkg.PkgPath, "_test"))+" -> "+relativePackage(importPath)] = true
			}
		}
	}
	return edges
}

func compareProxyImports(measured, allowlist map[string]bool) []string {
	var violations []string
	for _, edge := range sortedStrings(append(slices.Collect(mapKeys(measured)), slices.Collect(mapKeys(allowlist))...)) {
		switch {
		case measured[edge] && !allowlist[edge]:
			violations = append(violations, fmt.Sprintf("%s: new import of the serving orchestrator", edge))
		case !measured[edge] && allowlist[edge]:
			violations = append(violations, fmt.Sprintf("%s: no longer imported: delete its proxyImportAllowlist entry", edge))
		}
	}
	return violations
}
