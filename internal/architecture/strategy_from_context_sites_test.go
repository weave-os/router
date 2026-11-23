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
	values := strategyFromContextCallSites(loadTypedPackages(t, root, atomizationFixturePackage+"strategy_from_context_value"))
	require.Equal(t, []string{"internal/architecture/testdata/atomization/strategy_from_context_value.readStrategy", "internal/architecture/testdata/atomization/strategy_from_context_value.selectedVia"}, values)
	assert.Equal(t, []string{"+ internal/architecture/testdata/atomization/strategy_from_context_value.readStrategy", "+ internal/architecture/testdata/atomization/strategy_from_context_value.selectedVia"}, unexpectedStrategyFromContextSites(append(slices.Clone(sites), values...)))
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
				site := relativePackage(pkg.PkgPath) + "." + declSiteName(pkg.TypesInfo, decl)
				ast.Inspect(decl, func(node ast.Node) bool {
					if ident, ok := node.(*ast.Ident); ok && isRouterFunc(pkg.TypesInfo.Uses[ident], "StrategyFromContext") {
						sites = append(sites, site)
					}
					return true
				})
			}
		}
	}
	slices.Sort(sites)
	return sites
}

// declSiteName names a top-level declaration: a function or method, or the
// names a var/const/type block declares, so a package-level function value
// such as `var read = router.StrategyFromContext` is a site too.
func declSiteName(info *types.Info, decl ast.Decl) string {
	var names []string
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		return funcDeclName(info, decl)
	case *ast.GenDecl:
		names = genDeclNames(decl)
	}
	return strings.Join(names, ",")
}

func genDeclNames(decl *ast.GenDecl) []string {
	var names []string
	for _, spec := range decl.Specs {
		switch spec := spec.(type) {
		case *ast.ValueSpec:
			for _, name := range spec.Names {
				names = append(names, name.Name)
			}
		case *ast.TypeSpec:
			names = append(names, spec.Name.Name)
		}
	}
	return names
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
