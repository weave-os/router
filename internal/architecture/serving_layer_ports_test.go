package architecture_test

import (
	"go/types"
	"slices"
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
		"internal/architecture/testdata/serving_layer_proxy: holds *proxy.Service (aliasDeps.svc)",
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
			variable, ok := obj.(*types.Var)
			if !ok || !mentionsProxyService(variable.Type()) {
				continue
			}
			holder := ident.Name
			if owner := fieldOwner(pkg.TypesInfo, variable); owner != "" {
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

func mentionsProxyService(typ types.Type) bool {
	switch typ := types.Unalias(typ).(type) {
	case *types.Pointer:
		if named, ok := types.Unalias(typ.Elem()).(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == proxyPackagePath && named.Obj().Name() == "Service" {
			return true
		}
		return mentionsProxyService(typ.Elem())
	case *types.Slice:
		return mentionsProxyService(typ.Elem())
	case *types.Array:
		return mentionsProxyService(typ.Elem())
	case *types.Chan:
		return mentionsProxyService(typ.Elem())
	case *types.Map:
		return mentionsProxyService(typ.Key()) || mentionsProxyService(typ.Elem())
	case *types.Struct:
		for i := range typ.NumFields() {
			if mentionsProxyService(typ.Field(i).Type()) {
				return true
			}
		}
	case *types.Tuple:
		for i := range typ.Len() {
			if mentionsProxyService(typ.At(i).Type()) {
				return true
			}
		}
	case *types.Signature:
		return mentionsProxyService(typ.Params()) || mentionsProxyService(typ.Results())
	case *types.Named:
		for i := range typ.TypeArgs().Len() {
			if mentionsProxyService(typ.TypeArgs().At(i)) {
				return true
			}
		}
	}
	return false
}
