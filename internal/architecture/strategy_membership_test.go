package architecture_test

import (
	"go/ast"
	"go/types"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const candidatesPackagePath = routerPackagePath + "/candidates"

// strategyRouteCallSites are the serving-layer functions that call
// router.Router.Route. The list only shrinks: the serving layer reaches a
// strategy through (*Service).routeWithStrategy, which checks the decision
// against the offered candidates, or through the registered DynamicRouter
// that routeWithStrategy itself calls.
var strategyRouteCallSites = []string{
	"internal/policyregistry.(*DynamicRouter).Route",
	"internal/proxy.(*Service).routeWithStrategyUnchecked",
}

func TestServingLayerChecksStrategyDecisionsAgainstCandidates(t *testing.T) {
	loaded := loadTypedPackages(t, repositoryRoot(t), "./internal/...")
	assert.Empty(t, strategyMembershipViolations(loaded, proxyPackagePath, isServingLayerPackage))
}

func TestStrategyMembershipRejectsBypass(t *testing.T) {
	fixturePath := modulePath + "/internal/architecture/testdata/strategy_membership"
	loaded := loadTypedPackages(t, repositoryRoot(t), "./internal/architecture/testdata/strategy_membership")
	require.Equal(t, []string{
		"router.Router.Route callers: + internal/architecture/testdata/strategy_membership.(*Service).routeWithStrategyUnchecked",
		"router.Router.Route callers: + internal/architecture/testdata/strategy_membership.bypass",
		"router.Router.Route callers: - internal/policyregistry.(*DynamicRouter).Route",
		"router.Router.Route callers: - internal/proxy.(*Service).routeWithStrategyUnchecked",
		"routeWithStrategyUnchecked callers: + internal/architecture/testdata/strategy_membership.(*Service).shortcut",
		"routeWithStrategy does not call eligibleStrategyDecision",
		"eligibleStrategyDecision does not call candidates.EnforceMembership",
	}, strategyMembershipViolations(loaded, fixturePath, func(path string) bool { return path == fixturePath }))
}

func strategyMembershipViolations(loaded []*packages.Package, servingPath string, inServingLayer func(string) bool) []string {
	serving := relativePackage(servingPath)
	var violations []string
	routeCallers := referenceSites(loaded, inServingLayer, isRouterRouteMethod)
	violations = append(violations, siteDiff("router.Router.Route callers", strategyRouteCallSites, routeCallers)...)
	uncheckedCallers := referenceSites(loaded, inServingLayer, isFuncNamed(servingPath, "routeWithStrategyUnchecked"))
	violations = append(violations, siteDiff("routeWithStrategyUnchecked callers", []string{serving + ".(*Service).routeWithStrategy"}, uncheckedCallers)...)
	if !slices.Contains(referenceSites(loaded, inServingLayer, isFuncNamed(servingPath, "eligibleStrategyDecision")), serving+".(*Service).routeWithStrategy") {
		violations = append(violations, "routeWithStrategy does not call eligibleStrategyDecision")
	}
	if !slices.Contains(referenceSites(loaded, inServingLayer, isFuncNamed(candidatesPackagePath, "EnforceMembership")), serving+".eligibleStrategyDecision") {
		violations = append(violations, "eligibleStrategyDecision does not call candidates.EnforceMembership")
	}
	return violations
}

func siteDiff(label string, want, got []string) []string {
	var diff []string
	for _, site := range got {
		if !slices.Contains(want, site) {
			diff = append(diff, label+": + "+site)
		}
	}
	for _, site := range want {
		if !slices.Contains(got, site) {
			diff = append(diff, label+": - "+site)
		}
	}
	return diff
}

// referenceSites names each serving-layer function that references an object
// matching target, by call or by function value.
func referenceSites(loaded []*packages.Package, inServingLayer func(string) bool, target func(types.Object) bool) []string {
	var sites []string
	for _, pkg := range loaded {
		if !inServingLayer(pkg.PkgPath) {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(node ast.Node) bool {
					if ident, ok := node.(*ast.Ident); ok && target(pkg.TypesInfo.Uses[ident]) {
						sites = append(sites, relativePackage(pkg.PkgPath)+"."+funcDeclName(pkg.TypesInfo, fn))
					}
					return true
				})
			}
		}
	}
	slices.Sort(sites)
	return slices.Compact(sites)
}

func isRouterRouteMethod(object types.Object) bool {
	fn, ok := object.(*types.Func)
	if !ok || fn.Name() != "Route" {
		return false
	}
	recv := fn.Signature().Recv()
	return recv != nil && isNamedType(recv.Type(), routerPackagePath, "Router")
}

func isFuncNamed(packagePath, name string) func(types.Object) bool {
	return func(object types.Object) bool {
		fn, ok := object.(*types.Func)
		return ok && fn.Pkg() != nil && fn.Pkg().Path() == packagePath && fn.Name() == name
	}
}
