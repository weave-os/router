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

// strategyFromContextSites is the P5 target: the serving-layer entries that
// read the selected strategy once and pass the typed router.Strategy down.
var strategyFromContextSites = []string{
	"internal/api/anthropic.PreviewRouteHandler",
	"internal/proxy.(*Service).Route",
	"internal/proxy.(*Service).anthropicRoutingRequest",
	"internal/proxy.withTurnClassifierInput",
}

func TestStrategyFromContextOnlyAtServingEntries(t *testing.T) {
	sites := strategyFromContextCallSites(loadTypedPackages(t, repositoryRoot(t), "./internal/...", "./cmd/..."))
	if unexpected := unexpectedStrategyFromContextSites(sites); len(unexpected) > 0 {
		t.Fatalf("router.StrategyFromContext called outside the serving-layer entries; %s:\n%s", atomizationRatchetFixes[ratchetStrategyFromContext], strings.Join(unexpected, "\n"))
	}
}

func TestStrategyFromContextSitesRejectNewCaller(t *testing.T) {
	root := repositoryRoot(t)
	sites := strategyFromContextCallSites(loadTypedPackages(t, root, "./internal/...", "./cmd/..."))
	fixture := strategyFromContextCallSites(loadTypedPackages(t, root, atomizationFixturePackage+"proxy_hmm"))
	require.Equal(t, []string{"internal/architecture/testdata/atomization/proxy_hmm.selectedGroup"}, fixture)

	assert.Equal(t, []string{"+ internal/architecture/testdata/atomization/proxy_hmm.selectedGroup"}, unexpectedStrategyFromContextSites(append(slices.Clone(sites), fixture...)))
	assert.Equal(t, []string{"+ internal/proxy.withTurnClassifierInput"}, unexpectedStrategyFromContextSites(append(slices.Clone(sites), "internal/proxy.withTurnClassifierInput")))
	assert.Equal(t, []string{"- internal/proxy.(*Service).Route"}, unexpectedStrategyFromContextSites(slices.DeleteFunc(slices.Clone(sites), func(site string) bool { return site == "internal/proxy.(*Service).Route" })))
}

func unexpectedStrategyFromContextSites(sites []string) []string {
	remaining := slices.Clone(strategyFromContextSites)
	var unexpected []string
	for _, site := range sites {
		if i := slices.Index(remaining, site); i >= 0 {
			remaining = slices.Delete(remaining, i, i+1)
			continue
		}
		unexpected = append(unexpected, "+ "+site)
	}
	for _, site := range remaining {
		unexpected = append(unexpected, "- "+site)
	}
	return unexpected
}

func strategyFromContextCallSites(loaded []*packages.Package) []string {
	var sites []string
	for _, pkg := range loaded {
		if pkg.PkgPath == routerPackagePath {
			continue
		}
		for _, file := range pkg.Syntax {
			if strings.HasSuffix(pkg.Fset.Position(file.Package).Filename, "_test.go") {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(node ast.Node) bool {
					if call, ok := node.(*ast.CallExpr); ok && isRouterFunc(calledObject(pkg.TypesInfo, call.Fun), "StrategyFromContext") {
						sites = append(sites, relativePackage(pkg.PkgPath)+"."+funcDeclName(pkg.TypesInfo, fn))
					}
					return true
				})
			}
		}
	}
	slices.Sort(sites)
	return sites
}

func funcDeclName(info *types.Info, fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := types.TypeString(info.TypeOf(fn.Recv.List[0].Type), func(*types.Package) string { return "" })
	if strings.HasPrefix(receiver, "*") {
		receiver = "(" + receiver + ")"
	}
	return receiver + "." + fn.Name.Name
}
