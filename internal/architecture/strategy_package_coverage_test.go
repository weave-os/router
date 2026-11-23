package architecture_test

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// strategyRouterWrappers are router.Router implementations outside
// strategyPackageRoots that delegate to a registered strategy instead of
// choosing a model. The list only shrinks.
var strategyRouterWrappers = []string{
	"internal/policyregistry.DynamicRouter",
	"internal/proxy.Service",
	"internal/proxy.strategyScorer",
}

func TestStrategyPackageRootsCoverEveryStrategy(t *testing.T) {
	root := repositoryRoot(t)
	found := strategyRouterTypes(loadTypedPackages(t, root, "./internal/...", "./cmd/..."))
	assert.Empty(t, uncoveredStrategyRouters(found), "a router.Router implementation or policy.StrategySpec router lives outside strategyPackageRoots; add its package to strategyPackageRoots (so the strategy ratchets apply) or, for a delegating wrapper, to strategyRouterWrappers")
	for _, wrapper := range strategyRouterWrappers {
		assert.Contains(t, found, wrapper, "strategyRouterWrappers only shrinks; delete the entry for a wrapper that no longer implements router.Router")
	}
}

func TestStrategyPackageCoverageRejectsUnlistedRouter(t *testing.T) {
	root := repositoryRoot(t)
	fixture := strategyRouterTypes(loadTypedPackages(t, root, "./internal/architecture/testdata/strategy_coverage"))
	require.Equal(t, []string{"internal/architecture/testdata/strategy_coverage.registeredStrategy", "internal/architecture/testdata/strategy_coverage.shadowStrategy"}, fixture)
	assert.Equal(t, fixture, uncoveredStrategyRouters(fixture))
	assert.Empty(t, uncoveredStrategyRouters([]string{"internal/router/hmm.Router", "internal/proxy.Service"}))
}

func uncoveredStrategyRouters(found []string) []string {
	var uncovered []string
	for _, name := range found {
		if !isStrategyPackage(modulePath+"/"+name[:strings.LastIndex(name, ".")]) && !slices.Contains(strategyRouterWrappers, name) {
			uncovered = append(uncovered, name)
		}
	}
	return uncovered
}

// strategyRouterTypes names every package-level type that implements
// router.Router plus every concrete type passed as policy.StrategySpec.Router.
func strategyRouterTypes(loaded []*packages.Package) []string {
	routerIface := lookupRouterInterface(loaded)
	var found []string
	for _, pkg := range loaded {
		if pkg.PkgPath == routerPackagePath {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			if named, ok := scope.Lookup(name).(*types.TypeName); ok && implementsRouter(named.Type(), routerIface) {
				found = append(found, relativePackage(pkg.PkgPath)+"."+name)
			}
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				if named := strategySpecRouterType(pkg.TypesInfo, node); named != nil && named.Obj().Pkg() != nil {
					found = append(found, relativePackage(named.Obj().Pkg().Path())+"."+named.Obj().Name())
				}
				return true
			})
		}
	}
	slices.Sort(found)
	return slices.Compact(found)
}

func implementsRouter(typ types.Type, routerIface *types.Interface) bool {
	if types.IsInterface(typ) {
		return false
	}
	return types.Implements(typ, routerIface) || types.Implements(types.NewPointer(typ), routerIface)
}

func strategySpecRouterType(info *types.Info, node ast.Node) *types.Named {
	literal, ok := node.(*ast.CompositeLit)
	if !ok || !isNamedType(info.TypeOf(literal), policyPackagePath, "StrategySpec") {
		return nil
	}
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if key, isIdent := pair.Key.(*ast.Ident); !ok || !isIdent || key.Name != "Router" {
			continue
		}
		typ := info.TypeOf(pair.Value)
		if pointer, isPointer := typ.(*types.Pointer); isPointer {
			typ = pointer.Elem()
		}
		if named, isNamed := typ.(*types.Named); isNamed && !types.IsInterface(named) {
			return named
		}
	}
	return nil
}

func lookupRouterInterface(loaded []*packages.Package) *types.Interface {
	var routerIface *types.Interface
	packages.Visit(loaded, nil, func(pkg *packages.Package) {
		if pkg.PkgPath != routerPackagePath || pkg.Types == nil {
			return
		}
		if object, ok := pkg.Types.Scope().Lookup("Router").(*types.TypeName); ok {
			routerIface, _ = object.Type().Underlying().(*types.Interface)
		}
	})
	return routerIface
}
