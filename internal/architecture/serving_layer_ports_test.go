package architecture_test

import (
	"go/types"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const (
	servingLayerFixturePackage = "./internal/architecture/testdata/serving_layer_proxy"
	servingLayerRemediation    = "internal/api and internal/server take declared ports (server.Services and the handler port interfaces) that cmd/router wires; they must not import internal/proxy or hold *proxy.Service (.agents/skills/add-context-port)"
)

var servingLayerPackages = []string{"./internal/api/...", "./internal/server/..."}

// Non-test presentation code depends on ports only; tests may still build a
// real proxy to exercise wiring end to end.
func TestServingLayerDependsOnPortsNotProxy(t *testing.T) {
	assert.Empty(t, servingLayerProxyCouplings(t, servingLayerPackages...), servingLayerRemediation)
}

func TestServingLayerPortsRejectsNegativeFixtures(t *testing.T) {
	assert.Equal(t, []string{
		"internal/architecture/testdata/serving_layer_proxy: holds *proxy.Service (handlerDeps.svc)",
		"internal/architecture/testdata/serving_layer_proxy: imports internal/proxy",
	}, servingLayerProxyCouplings(t, servingLayerFixturePackage))
}

func servingLayerProxyCouplings(t *testing.T, patterns ...string) []string {
	t.Helper()
	mode := packages.NeedName | packages.NeedImports | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax
	loaded, err := packages.Load(&packages.Config{Dir: repositoryRoot(t), Mode: mode}, patterns...)
	require.NoError(t, err)
	require.NotEmpty(t, loaded)
	serviceType := "*" + proxyPackagePath + ".Service"
	found := map[string]bool{}
	for _, pkg := range loaded {
		require.Empty(t, pkg.Errors, pkg.PkgPath)
		pkgPath := relativePackage(pkg.PkgPath)
		for importPath := range pkg.Imports {
			if hasPathPrefix(importPath, proxyPackagePath) {
				found[pkgPath+": imports "+relativePackage(importPath)] = true
			}
		}
		for ident, obj := range pkg.TypesInfo.Defs {
			field, ok := obj.(*types.Var)
			if !ok || !strings.Contains(types.TypeString(field.Type(), nil), serviceType) {
				continue
			}
			holder := ident.Name
			if owner := fieldOwner(pkg.TypesInfo, field); owner != "" {
				holder = owner + "." + holder
			}
			found[pkgPath+": holds *proxy.Service ("+holder+")"] = true
		}
	}
	return sortedStrings(slices.Collect(mapKeys(found)))
}

func fieldOwner(info *types.Info, field *types.Var) string {
	if !field.IsField() {
		return ""
	}
	for _, obj := range info.Defs {
		named, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		if structType, isStruct := named.Type().Underlying().(*types.Struct); isStruct {
			for i := range structType.NumFields() {
				if structType.Field(i) == field {
					return named.Name()
				}
			}
		}
	}
	return ""
}
