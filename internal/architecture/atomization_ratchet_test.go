package architecture_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const (
	atomizationBaselinePath    = "atomization_ratchet_baseline.json"
	atomizationSchemaVersion   = "atomization_ratchet_v1"
	atomizationFixturePackage  = "./internal/architecture/testdata/atomization/"
	sessionpinPackagePath      = routerPackagePath + "/sessionpin"
	hmmPackagePath             = routerPackagePath + "/hmm"
	escalationPackagePath      = routerPackagePath + "/escalation"
	escalationJudgePackagePath = modulePath + "/internal/escalationjudge"
)

const (
	ratchetProxyHMMMetadata       = "proxy_hmm_metadata_references"
	ratchetStrategyFromContext    = "strategy_from_context_callers"
	ratchetProxyReasonComparisons = "proxy_reason_string_comparisons"
	ratchetStrategyAdapterImports = "strategy_serving_adapter_imports"
	ratchetServingStrategyImports = "serving_strategy_internal_imports"
	ratchetHMMPinFields           = "hmm_pin_field_references"
	ratchetProxyEscalation        = "proxy_escalation_internal_references"
)

var atomizationRatchetFixes = map[string]string{
	ratchetProxyHMMMetadata:       "internal/proxy must not read HMM-only decision metadata (RoutingMetadata.PolicyGroup, Classifier*, ArmScores, SelectionTrace) or call router.IsHMMStrategy; move the logic into internal/router/hmm and return a strategy-neutral value",
	ratchetStrategyFromContext:    "read the strategy once at strategy selection or the serving-layer entry and pass the typed router.Strategy down instead of calling router.StrategyFromContext again",
	ratchetProxyReasonComparisons: "internal/proxy must not branch on decision reason text (== / != on a Reason field, or an hmm_policy prefix check); carry the fact as a typed field on the decision",
	ratchetStrategyAdapterImports: "strategy packages (internal/router/hmm, rl, decisionmodel, policy) must not import serving-layer adapters; declare a port in the strategy package and implement it in the serving layer",
	ratchetServingStrategyImports: "serving-layer packages may reach a strategy only through router.Router, router.Request and router.Decision; wire the concrete strategy in cmd/router instead of importing its package",
	ratchetProxyEscalation:        "internal/proxy must not drive escalation scoring, judging or state (escalation.Store/Observer, llmescalation.Judge, internal/escalationjudge); call the helper in internal/router/escalation/floor and pass it turn facts",
	ratchetHMMPinFields:           "HMM-only session pin fields (sessionpin.Pin.PolicyGroup, ConsecutiveDowngradeVotes, ConsecutiveUpgradeVotes) are read and written only by the accessor in internal/router/hmm",
}

var (
	strategyPackageRoots    = []string{hmmPackagePath, routerPackagePath + "/rl", routerPackagePath + "/decisionmodel", policyPackagePath}
	strategyInternalRoots   = []string{hmmPackagePath, routerPackagePath + "/rl", routerPackagePath + "/decisionmodel"}
	servingAdapterRoots     = []string{"auth", "billing", "subscriptions", "credentials", "dispatch", "postgres"}
	routerLayerRoots        = []string{routerPackagePath, modulePath + "/internal/routingpolicy", modulePath + "/internal/architecture"}
	hmmOnlyPinFields        = []string{"PolicyGroup", "ConsecutiveDowngradeVotes", "ConsecutiveUpgradeVotes"}
	hmmOnlyMetadataFields   = []string{"PolicyGroup", "ArmScores", "SelectionTrace"}
	hmmOnlyMetadataPrefixes = []string{"Classifier"}
	escalationInternalPorts = map[string][]string{escalationPackagePath: {"Store", "Observer"}, routerPackagePath + "/llmescalation": {"Judge"}}
)

type atomizationBaseline struct {
	SchemaVersion string                    `json:"schema_version"`
	Ratchets      map[string]map[string]int `json:"ratchets"`
}

type atomizationCounts map[string]map[string]int

func (c atomizationCounts) add(ratchet, key string) {
	if c[ratchet] == nil {
		c[ratchet] = map[string]int{}
	}
	c[ratchet][key]++
}

func TestAtomizationRatchetsOnlyGoDown(t *testing.T) {
	root := repositoryRoot(t)
	measured := measureAtomization(t, root, "./internal/...", "./cmd/...")
	baseline := readAtomizationBaseline(t)
	require.Equal(t, atomizationSchemaVersion, baseline.SchemaVersion)
	require.ElementsMatch(t, slices.Collect(mapKeys(atomizationRatchetFixes)), slices.Collect(mapKeys(baseline.Ratchets)), "every ratchet needs a baseline entry, even an empty one")
	if failures := compareAtomization(measured, baseline.Ratchets); len(failures) > 0 {
		t.Fatalf("atomization ratchets:\n%s", strings.Join(failures, "\n"))
	}
}

func TestAtomizationRatchetsDetectSyntheticViolations(t *testing.T) {
	root := repositoryRoot(t)
	tests := []struct {
		fixture string
		want    atomizationCounts
	}{
		{fixture: "proxy_hmm", want: atomizationCounts{
			ratchetProxyHMMMetadata:       {"internal/architecture/testdata/atomization/proxy_hmm": 6},
			ratchetStrategyFromContext:    {"internal/architecture/testdata/atomization/proxy_hmm": 1},
			ratchetProxyReasonComparisons: {"internal/architecture/testdata/atomization/proxy_hmm": 3},
		}},
		{fixture: "strategy_adapter", want: atomizationCounts{
			ratchetStrategyAdapterImports: {"internal/architecture/testdata/atomization/strategy_adapter -> internal/billing": 1},
		}},
		{fixture: "serving_strategy", want: atomizationCounts{
			ratchetServingStrategyImports: {"internal/architecture/testdata/atomization/serving_strategy -> internal/router/rl": 1},
		}},
		{fixture: "proxy_escalation", want: atomizationCounts{
			ratchetProxyEscalation: {"internal/architecture/testdata/atomization/proxy_escalation": 3},
		}},
		{fixture: "pin_fields", want: atomizationCounts{
			ratchetHMMPinFields: {"internal/architecture/testdata/atomization/pin_fields": 4},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got := measureAtomizationFixture(t, root, atomizationFixturePackage+tt.fixture)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAtomizationRatchetsRejectGrowthAndStaleBaselines(t *testing.T) {
	baseline := map[string]map[string]int{ratchetProxyHMMMetadata: {"internal/proxy": 3}, ratchetHMMPinFields: {}}
	fix := atomizationRatchetFixes[ratchetProxyHMMMetadata]

	assert.Empty(t, compareAtomization(atomizationCounts{ratchetProxyHMMMetadata: {"internal/proxy": 3}}, baseline))
	assert.Equal(t, []string{ratchetProxyHMMMetadata + ": internal/proxy has 4, baseline 3 in internal/architecture/" + atomizationBaselinePath + "; " + fix},
		compareAtomization(atomizationCounts{ratchetProxyHMMMetadata: {"internal/proxy": 4}}, baseline))
	assert.Equal(t, []string{ratchetProxyHMMMetadata + ": internal/proxy has 2: lower its baseline in internal/architecture/" + atomizationBaselinePath + " from 3 to 2, the ratchet only goes down"},
		compareAtomization(atomizationCounts{ratchetProxyHMMMetadata: {"internal/proxy": 2}}, baseline))
	assert.Equal(t, []string{ratchetProxyHMMMetadata + ": internal/proxy has none left: remove it from internal/architecture/" + atomizationBaselinePath},
		compareAtomization(atomizationCounts{}, baseline))
	assert.Equal(t, []string{
		ratchetHMMPinFields + ": internal/proxy has 1, baseline 0 in internal/architecture/" + atomizationBaselinePath + "; " + atomizationRatchetFixes[ratchetHMMPinFields],
		ratchetProxyHMMMetadata + ": internal/proxy has none left: remove it from internal/architecture/" + atomizationBaselinePath,
	}, compareAtomization(atomizationCounts{ratchetHMMPinFields: {"internal/proxy": 1}}, baseline))
}

func TestAtomizationLayerClassification(t *testing.T) {
	assert.True(t, isStrategyPackage(hmmPackagePath+"/selection"))
	assert.True(t, isStrategyPackage(policyPackagePath))
	assert.False(t, isStrategyPackage(routerPackagePath+"/sessionpin"))
	assert.True(t, isServingAdapterImport(modulePath+"/internal/providers/anthropic"))
	assert.False(t, isServingAdapterImport(providersPackagePath))
	assert.True(t, isServingAdapterImport(modulePath+"/internal/billing"))
	assert.True(t, isServingLayerPackage(proxyPackagePath))
	assert.True(t, isServingLayerPackage(modulePath+"/internal/api/admin"))
	assert.False(t, isServingLayerPackage(modulePath+"/cmd/router"))
	assert.False(t, isServingLayerPackage(modulePath+"/internal/routingpolicy"))
	assert.False(t, isServingLayerPackage(routerPackagePath+"/planner"))
}

func measureAtomization(t *testing.T, root string, patterns ...string) atomizationCounts {
	t.Helper()
	counts := atomizationCounts{}
	for _, pkg := range loadTypedPackages(t, root, patterns...) {
		path := pkg.PkgPath
		measurePackage(counts, pkg, atomizationScope{
			proxy:      hasPathPrefix(path, proxyPackagePath),
			strategy:   isStrategyPackage(path),
			serving:    isServingLayerPackage(path),
			pinAllowed: hasPathPrefix(path, hmmPackagePath),
		})
	}
	return counts
}

func measureAtomizationFixture(t *testing.T, root, pattern string) atomizationCounts {
	t.Helper()
	counts := atomizationCounts{}
	for _, pkg := range loadTypedPackages(t, root, pattern) {
		measurePackage(counts, pkg, atomizationScope{proxy: true, strategy: true, serving: true})
	}
	return counts
}

type atomizationScope struct {
	proxy, strategy, serving, pinAllowed bool
}

func loadTypedPackages(t *testing.T, root string, patterns ...string) []*packages.Package {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{
		Dir:  root,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
	}, patterns...)
	require.NoError(t, err)
	if count := packages.PrintErrors(loaded); count > 0 {
		t.Fatalf("load packages for atomization ratchets: %d errors", count)
	}
	return loaded
}

func measurePackage(counts atomizationCounts, pkg *packages.Package, scope atomizationScope) {
	key := relativePackage(pkg.PkgPath)
	for importPath := range pkg.Imports {
		if scope.strategy && isServingAdapterImport(importPath) {
			counts.add(ratchetStrategyAdapterImports, key+" -> "+relativePackage(importPath))
		}
		if scope.proxy && hasPathPrefix(importPath, escalationJudgePackagePath) {
			counts.add(ratchetProxyEscalation, key)
		}
		if scope.serving && isStrategyInternal(importPath) {
			counts.add(ratchetServingStrategyImports, key+" -> "+relativePackage(importPath))
		}
	}
	for _, file := range pkg.Syntax {
		if strings.HasSuffix(pkg.Fset.Position(file.Package).Filename, "_test.go") {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			for _, ratchet := range nodeViolations(pkg.TypesInfo, node, scope) {
				counts.add(ratchet, key)
			}
			return true
		})
	}
}

func nodeViolations(info *types.Info, node ast.Node, scope atomizationScope) []string {
	var found []string
	switch n := node.(type) {
	case *ast.SelectorExpr:
		if scope.proxy && isHMMMetadataField(info, n) {
			found = append(found, ratchetProxyHMMMetadata)
		}
		if !scope.pinAllowed && isHMMPinField(info, n) {
			found = append(found, ratchetHMMPinFields)
		}
	case *ast.CompositeLit:
		if !scope.pinAllowed {
			for range hmmPinLiteralKeys(info, n) {
				found = append(found, ratchetHMMPinFields)
			}
		}
	case *ast.CallExpr:
		object := calledObject(info, n.Fun)
		if isRouterFunc(object, "StrategyFromContext") {
			found = append(found, ratchetStrategyFromContext)
		}
		if scope.proxy {
			found = append(found, proxyCallViolations(info, n, object)...)
		}
	case *ast.BinaryExpr:
		if scope.proxy && (n.Op == token.EQL || n.Op == token.NEQ) && (isReasonField(n.X) || isReasonField(n.Y)) {
			found = append(found, ratchetProxyReasonComparisons)
		}
	}
	return found
}

func isHMMMetadataField(info *types.Info, selector *ast.SelectorExpr) bool {
	selection, ok := info.Selections[selector]
	if !ok || selection.Kind() != types.FieldVal || !isNamedType(selection.Recv(), routerPackagePath, "RoutingMetadata") {
		return false
	}
	name := selector.Sel.Name
	return slices.Contains(hmmOnlyMetadataFields, name) || slices.ContainsFunc(hmmOnlyMetadataPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

func isHMMPinField(info *types.Info, selector *ast.SelectorExpr) bool {
	selection, ok := info.Selections[selector]
	return ok && selection.Kind() == types.FieldVal && isNamedType(selection.Recv(), sessionpinPackagePath, "Pin") && slices.Contains(hmmOnlyPinFields, selector.Sel.Name)
}

func hmmPinLiteralKeys(info *types.Info, literal *ast.CompositeLit) []string {
	if !isNamedType(info.TypeOf(literal), sessionpinPackagePath, "Pin") {
		return nil
	}
	var keys []string
	for _, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			if ident, ok := pair.Key.(*ast.Ident); ok && slices.Contains(hmmOnlyPinFields, ident.Name) {
				keys = append(keys, ident.Name)
			}
		}
	}
	return keys
}

func isRouterFunc(object types.Object, name string) bool {
	function, ok := object.(*types.Func)
	return ok && function.Pkg() != nil && function.Pkg().Path() == routerPackagePath && function.Name() == name
}

func proxyCallViolations(info *types.Info, call *ast.CallExpr, object types.Object) []string {
	var found []string
	if isRouterFunc(object, "IsHMMStrategy") {
		found = append(found, ratchetProxyHMMMetadata)
	}
	if isEscalationInternalCall(object) {
		found = append(found, ratchetProxyEscalation)
	}
	if isHMMPolicyPrefixCheck(info, call, object) {
		found = append(found, ratchetProxyReasonComparisons)
	}
	return found
}

func isEscalationInternalCall(object types.Object) bool {
	function, ok := object.(*types.Func)
	if !ok {
		return false
	}
	recv := function.Signature().Recv()
	if recv == nil {
		return false
	}
	named, ok := types.Unalias(recv.Type()).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return slices.Contains(escalationInternalPorts[named.Obj().Pkg().Path()], named.Obj().Name())
}

func isHMMPolicyPrefixCheck(info *types.Info, call *ast.CallExpr, object types.Object) bool {
	function, ok := object.(*types.Func)
	if !ok || function.Pkg() == nil || function.Pkg().Path() != "strings" || len(call.Args) != 2 {
		return false
	}
	if function.Name() != "HasPrefix" && function.Name() != "Contains" && function.Name() != "CutPrefix" && function.Name() != "TrimPrefix" {
		return false
	}
	value := info.Types[call.Args[1]].Value
	return value != nil && value.Kind() == constant.String && strings.HasPrefix(constant.StringVal(value), "hmm_policy")
}

func isReasonField(expression ast.Expr) bool {
	selector, ok := ast.Unparen(expression).(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Reason"
}

func isStrategyPackage(path string) bool {
	return slices.ContainsFunc(strategyPackageRoots, func(root string) bool { return hasPathPrefix(path, root) })
}

func isStrategyInternal(path string) bool {
	return slices.ContainsFunc(strategyInternalRoots, func(root string) bool { return hasPathPrefix(path, root) })
}

func isServingAdapterImport(path string) bool {
	if hasPathPrefix(path, providersPackagePath) && path != providersPackagePath {
		return true
	}
	return slices.ContainsFunc(servingAdapterRoots, func(name string) bool { return hasPathPrefix(path, modulePath+"/internal/"+name) })
}

func isServingLayerPackage(path string) bool {
	if !hasPathPrefix(path, modulePath+"/internal") {
		return false
	}
	return !slices.ContainsFunc(routerLayerRoots, func(root string) bool { return hasPathPrefix(path, root) })
}

func hasPathPrefix(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func relativePackage(path string) string {
	return strings.TrimPrefix(path, modulePath+"/")
}

func compareAtomization(measured atomizationCounts, baseline map[string]map[string]int) []string {
	location := "internal/architecture/" + atomizationBaselinePath
	var failures []string
	for _, ratchet := range sortedStrings(append(slices.Collect(mapKeys(measured)), slices.Collect(mapKeys(baseline))...)) {
		got, want := measured[ratchet], baseline[ratchet]
		for _, key := range sortedStrings(append(slices.Collect(mapKeys(got)), slices.Collect(mapKeys(want))...)) {
			have, budget := got[key], want[key]
			switch {
			case have > budget:
				failures = append(failures, fmt.Sprintf("%s: %s has %d, baseline %d in %s; %s", ratchet, key, have, budget, location, atomizationRatchetFixes[ratchet]))
			case have == 0:
				failures = append(failures, fmt.Sprintf("%s: %s has none left: remove it from %s", ratchet, key, location))
			case have < budget:
				failures = append(failures, fmt.Sprintf("%s: %s has %d: lower its baseline in %s from %d to %d, the ratchet only goes down", ratchet, key, have, location, budget, have))
			}
		}
	}
	return failures
}

func sortedStrings(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}

func readAtomizationBaseline(t *testing.T) atomizationBaseline {
	t.Helper()
	payload, err := os.ReadFile(filepath.Clean(atomizationBaselinePath))
	require.NoError(t, err)
	var baseline atomizationBaseline
	require.NoError(t, json.Unmarshal(payload, &baseline))
	return baseline
}
