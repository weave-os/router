package architecture_test

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const (
	configPackagePath      = modulePath + "/internal/config"
	envReadsFixturePackage = "./internal/architecture/testdata/env_reads"
	envOwnershipFix        = "declare a typed Config in the owning package, populate it from the environment in cmd/<binary> or internal/config, and pass it in by constructor or Configure"
)

// envReadAllowlist lists library files that still read the environment.
// Counts only go down; delete an entry once its package takes typed config.
var envReadAllowlist = map[string]int{}

var envReadObjects = map[string][]string{
	"os":       {"Getenv", "LookupEnv", "Environ", "ExpandEnv"},
	"syscall":  {"Getenv", "Environ"},
	"net/http": {"ProxyFromEnvironment"},
}

var envReadImports = []string{
	"flag",
	"github.com/kelseyhightower/envconfig",
	"github.com/caarlos0/env",
	"github.com/caarlos0/env/v11",
	"github.com/joho/godotenv",
	"github.com/spf13/viper",
}

func TestEnvironmentIsReadOnlyByEntrypointsAndConfig(t *testing.T) {
	root := repositoryRoot(t)
	measured := measureEnvReads(t, root, loadTypedPackages(t, root, "./..."))
	violations := compareEnvReads(measured, envReadAllowlist)
	assert.Empty(t, violations, "environment read outside cmd/, package main and internal/config: %s", envOwnershipFix)
}

func TestEnvReadDetectionCoversIndirectReadersAndAliases(t *testing.T) {
	root := repositoryRoot(t)
	measured := measureEnvReads(t, root, loadTypedPackages(t, root, envReadsFixturePackage))
	assert.Equal(t, map[string]int{"internal/architecture/testdata/env_reads/fixture.go": 5}, measured,
		"flag import, aliased os.Getenv and os.LookupEnv, internal/config.GetOr and http.ProxyFromEnvironment count; a method named Get on a local value does not")
}

func TestEnvReadAllowlistRejectsNewFilesGrowthAndStaleEntries(t *testing.T) {
	allowlist := map[string]int{"internal/a/a.go": 2}
	assert.Empty(t, compareEnvReads(map[string]int{"internal/a/a.go": 2}, allowlist))
	assert.Equal(t, []string{"internal/a/a.go: 1 environment reads: lower its envReadAllowlist entry from 2 to 1, the allowlist only goes down"}, compareEnvReads(map[string]int{"internal/a/a.go": 1}, allowlist))
	assert.Equal(t, []string{"internal/a/a.go: no environment reads left: delete its envReadAllowlist entry"}, compareEnvReads(map[string]int{}, allowlist))
	assert.Equal(t, []string{"internal/a/a.go: 3 environment reads (allowlist 2)"}, compareEnvReads(map[string]int{"internal/a/a.go": 3}, allowlist))
	assert.Equal(t, []string{"internal/b/b.go: 1 environment reads (allowlist 0)"}, compareEnvReads(map[string]int{"internal/a/a.go": 2, "internal/b/b.go": 1}, allowlist))
}

func TestEnvOwnershipScopeExemptsEntrypointsAndConfig(t *testing.T) {
	for path, exempt := range map[string]bool{
		modulePath + "/cmd/router":                  true,
		modulePath + "/cmd/router/internal/wiring":  true,
		configPackagePath:                           true,
		configPackagePath + "/sub":                  true,
		modulePath + "/internal/configuration":      false,
		modulePath + "/internal/providers/httputil": false,
		modulePath + "/scripts/fixtures":            false,
	} {
		assert.Equal(t, exempt, envOwnershipExempt(&packages.Package{PkgPath: path, Name: "lib"}), path)
	}
	assert.True(t, envOwnershipExempt(&packages.Package{PkgPath: modulePath + "/scripts/check", Name: "main"}))
}

func envOwnershipExempt(pkg *packages.Package) bool {
	return pkg.Name == "main" || hasPathPrefix(pkg.PkgPath, modulePath+"/cmd") || hasPathPrefix(pkg.PkgPath, configPackagePath)
}

func measureEnvReads(t *testing.T, root string, loaded []*packages.Package) map[string]int {
	t.Helper()
	measured := map[string]int{}
	for _, pkg := range loaded {
		if envOwnershipExempt(pkg) {
			continue
		}
		for _, file := range pkg.Syntax {
			relPath, err := filepath.Rel(root, pkg.Fset.Position(file.Pos()).Filename)
			require.NoError(t, err)
			if count := countEnvReads(pkg.TypesInfo, file); count > 0 {
				measured[filepath.ToSlash(relPath)] += count
			}
		}
	}
	return measured
}

func countEnvReads(info *types.Info, file *ast.File) int {
	count := 0
	for _, spec := range file.Imports {
		if slices.Contains(envReadImports, strings.Trim(spec.Path.Value, `"`)) {
			count++
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && isEnvReadObject(info.Uses[ident]) {
			count++
		}
		return true
	})
	return count
}

func isEnvReadObject(object types.Object) bool {
	if object == nil || object.Pkg() == nil {
		return false
	}
	if _, isPackageName := object.(*types.PkgName); isPackageName {
		return false
	}
	path := object.Pkg().Path()
	if hasPathPrefix(path, configPackagePath) {
		_, isFunc := object.(*types.Func)
		return isFunc
	}
	return slices.Contains(envReadObjects[path], object.Name())
}

func compareEnvReads(measured, allowlist map[string]int) []string {
	var violations []string
	for _, file := range sortedStrings(append(slices.Collect(mapKeys(measured)), slices.Collect(mapKeys(allowlist))...)) {
		measuredCount, budget := measured[file], allowlist[file]
		switch {
		case measuredCount > budget:
			violations = append(violations, fmt.Sprintf("%s: %d environment reads (allowlist %d)", file, measuredCount, budget))
		case measuredCount == 0:
			violations = append(violations, fmt.Sprintf("%s: no environment reads left: delete its envReadAllowlist entry", file))
		case measuredCount < budget:
			violations = append(violations, fmt.Sprintf("%s: %d environment reads: lower its envReadAllowlist entry from %d to %d, the allowlist only goes down", file, measuredCount, budget, measuredCount))
		}
	}
	return violations
}
