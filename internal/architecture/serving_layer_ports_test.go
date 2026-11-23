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
		"internal/architecture/testdata/serving_layer_proxy: holds *proxy.Service (pointerAliasDeps.svc)",
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
	typ = types.Unalias(typ)
	return isProxyServicePointer(typ) || slices.ContainsFunc(componentTypes(typ), mentionsProxyService)
}

func isProxyServicePointer(typ types.Type) bool {
	pointer, ok := typ.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := types.Unalias(pointer.Elem()).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == proxyPackagePath && named.Obj().Name() == "Service"
}

func componentTypes(typ types.Type) []types.Type {
	var components []types.Type
	switch typ := typ.(type) {
	case *types.Pointer:
		components = append(components, typ.Elem())
	case *types.Slice:
		components = append(components, typ.Elem())
	case *types.Array:
		components = append(components, typ.Elem())
	case *types.Chan:
		components = append(components, typ.Elem())
	case *types.Map:
		components = append(components, typ.Key(), typ.Elem())
	case *types.Signature:
		components = append(components, typ.Params(), typ.Results())
	case *types.Struct:
		for i := range typ.NumFields() {
			components = append(components, typ.Field(i).Type())
		}
	case *types.Tuple:
		for i := range typ.Len() {
			components = append(components, typ.At(i).Type())
		}
	case *types.Named:
		for i := range typ.TypeArgs().Len() {
			components = append(components, typ.TypeArgs().At(i))
		}
	}
	return components
}
