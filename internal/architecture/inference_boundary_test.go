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
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

const (
	modulePath            = "weave-os/router"
	providersPackagePath  = modulePath + "/internal/providers"
	routerPackagePath     = modulePath + "/internal/router"
	policyPackagePath     = modulePath + "/internal/router/policy"
	baselineSchemaVersion = "inference_boundary_baseline_v1"
	baselineOwner         = "@steventohme"
	baselinePath          = "inference_boundary_baseline.json"
)

var purposePackagePaths = []string{policyPackagePath}

type findingKind string

const (
	findingConcreteProviderImport findingKind = "concrete_provider_import"
	findingDirectProviderCall     findingKind = "direct_provider_call"
	findingProviderMap            findingKind = "provider_map"
	findingTargetConstruction     findingKind = "target_construction"
	findingFeatureTargetConstant  findingKind = "feature_target_constant"
	findingRawHTTPClient          findingKind = "raw_http_client"
	findingUnregisteredPurpose    findingKind = "unregistered_purpose"
)

type finding struct {
	ID     string      `json:"id"`
	Kind   findingKind `json:"kind"`
	File   string      `json:"file"`
	Line   int         `json:"line"`
	Symbol string      `json:"symbol"`
	Detail string      `json:"detail"`
}

type exception struct {
	finding
	Owner          string               `json:"owner"`
	Rationale      string               `json:"rationale"`
	OperationClass policy.DispatchClass `json:"operation_class"`
	RemovalPhase   string               `json:"removal_phase"`
}

type baseline struct {
	SchemaVersion  string      `json:"schema_version"`
	SourceRevision string      `json:"source_revision"`
	Exceptions     []exception `json:"exceptions"`
}

type pendingFinding struct {
	finding
	column  int
	subject string
}

func TestInferenceBoundaryMatchesReviewedBaseline(t *testing.T) {
	root := repositoryRoot(t)
	findings := scanPackages(t, root, "./internal/...", "./cmd/...")
	reviewed := readBaseline(t)

	require.Equal(t, baselineSchemaVersion, reviewed.SchemaVersion)
	require.NotEmpty(t, reviewed.SourceRevision)
	for _, item := range reviewed.Exceptions {
		assert.Equal(t, baselineOwner, item.Owner, "exception %s must retain policy-owner review", item.ID)
		assert.NotEmpty(t, item.Rationale, "exception %s needs a rationale", item.ID)
		assert.NotEmpty(t, item.OperationClass, "exception %s needs an operation class", item.ID)
		assert.NotEmpty(t, item.RemovalPhase, "exception %s needs a removal phase", item.ID)
	}

	missing, stale := compareBaseline(findings, reviewed.Exceptions)
	if len(missing) > 0 || len(stale) > 0 {
		missingJSON, err := json.MarshalIndent(missing, "", "  ")
		require.NoError(t, err)
		t.Fatalf("inference boundary changed without a reviewed manifest update\nnew or changed findings:\n%s\nstale exception ids:\n%s", missingJSON, strings.Join(stale, "\n"))
	}
}

func TestInferenceBoundaryRejectsNegativeFixtures(t *testing.T) {
	root := repositoryRoot(t)
	tests := []struct {
		name         string
		fixture      string
		expectedKind findingKind
	}{
		{name: "direct fixed model", fixture: "direct_fixed_model", expectedKind: findingTargetConstruction},
		{name: "named constant fixed model", fixture: "named_constant_fixed_model", expectedKind: findingFeatureTargetConstant},
		{name: "aliased provider client", fixture: "aliased_provider_client", expectedKind: findingDirectProviderCall},
		{name: "second raw HTTP client", fixture: "raw_inference_http", expectedKind: findingRawHTTPClient},
		{name: "unregistered purpose", fixture: "unregistered_purpose", expectedKind: findingUnregisteredPurpose},
		{name: "concrete provider import", fixture: "concrete_provider_import", expectedKind: findingConcreteProviderImport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := "./internal/architecture/testdata/" + tt.fixture
			findings := scanPackages(t, root, pattern)
			assert.Contains(t, findingKinds(findings), tt.expectedKind)
		})
	}
}

func TestInferenceBoundaryDetectsEveryPurposeShape(t *testing.T) {
	findings := scanPackages(t, repositoryRoot(t), "./internal/architecture/testdata/unregistered_purpose")
	details := make(map[string]struct{}, len(findings))
	for _, item := range findings {
		require.Equal(t, findingUnregisteredPurpose, item.Kind)
		details[item.Detail] = struct{}{}
	}
	for _, expected := range []string{
		"fixture_unregistered_purpose",
		"fixture_unregistered_purpose_paren",
		"fixture_unregistered_purpose_typed",
		"fixture_unregistered_purpose_const",
		"fixture_unregistered_purpose_arg",
	} {
		assert.Contains(t, details, expected)
	}
}

func TestInferenceBoundaryRejectsBaselineExpansion(t *testing.T) {
	newFinding := finding{ID: "direct_provider_call|new.go|feature|providers.Client.Proxy|1"}
	missing, stale := compareBaseline([]finding{newFinding}, nil)

	assert.Equal(t, []finding{newFinding}, missing)
	assert.Empty(t, stale)
}

func TestInferenceBoundaryToleratesLineShiftsButNotDetailChanges(t *testing.T) {
	reviewed := exception{finding: finding{ID: "kind|file.go|symbol|subject|1", Kind: "kind", File: "file.go", Line: 10, Symbol: "symbol", Detail: "detail"}}

	shifted := reviewed.finding
	shifted.Line = 42
	missing, stale := compareBaseline([]finding{shifted}, []exception{reviewed})
	assert.Empty(t, missing)
	assert.Empty(t, stale)

	changed := reviewed.finding
	changed.Detail = "other"
	missing, stale = compareBaseline([]finding{changed}, []exception{reviewed})
	assert.Equal(t, []finding{changed}, missing)
	assert.Empty(t, stale)
}

func scanPackages(t *testing.T, root string, patterns ...string) []finding {
	t.Helper()
	configuration := &packages.Config{
		Dir:   root,
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Tests: false,
	}
	loaded, err := packages.Load(configuration, patterns...)
	require.NoError(t, err)
	if count := packages.PrintErrors(loaded); count > 0 {
		t.Fatalf("load packages for inference-boundary scan: %d errors", count)
	}

	knownTargets := make(map[string]struct{}, len(catalog.Models)+len(providers.AllProviders()))
	for _, model := range catalog.Models {
		knownTargets[model.ID] = struct{}{}
	}
	for _, provider := range providers.AllProviders() {
		knownTargets[provider] = struct{}{}
	}
	knownPurposes := make(map[string]struct{}, len(policy.KnownPurposes()))
	for _, purpose := range policy.KnownPurposes() {
		knownPurposes[string(purpose)] = struct{}{}
	}

	var pending []pendingFinding
	for _, loadedPackage := range loaded {
		if !strings.HasPrefix(loadedPackage.PkgPath, modulePath+"/") || loadedPackage.TypesInfo == nil {
			continue
		}
		for _, file := range loadedPackage.Syntax {
			filename := loadedPackage.Fset.Position(file.Package).Filename
			relative, err := filepath.Rel(root, filename)
			require.NoError(t, err)
			relative = filepath.ToSlash(relative)
			functions := declaredFunctions(file)
			ast.Inspect(file, func(node ast.Node) bool {
				if node == nil {
					return true
				}
				position := loadedPackage.Fset.Position(node.Pos())
				symbol := enclosingSymbol(functions, node.Pos(), loadedPackage.Name)
				appendFinding := func(kind findingKind, subject, detail string) {
					pending = append(pending, pendingFinding{
						finding: finding{Kind: kind, File: relative, Line: position.Line, Symbol: symbol, Detail: detail},
						column:  position.Column,
						subject: subject,
					})
				}

				if expression, ok := node.(ast.Expr); ok && isPurposeType(loadedPackage.TypesInfo.TypeOf(expression)) {
					purposeValue, known := compileTimeString(loadedPackage.TypesInfo, expression)
					if known {
						if _, registered := knownPurposes[purposeValue]; !registered {
							appendFinding(findingUnregisteredPurpose, purposeValue, purposeValue)
						}
						return false
					}
				}

				switch typedNode := node.(type) {
				case *ast.ImportSpec:
					importPath, err := strconv.Unquote(typedNode.Path.Value)
					if err == nil && strings.HasPrefix(importPath, providersPackagePath+"/") && !concreteProviderImportAllowed(loadedPackage.PkgPath) {
						appendFinding(findingConcreteProviderImport, importPath, importPath)
					}
				case *ast.MapType:
					mapType, ok := loadedPackage.TypesInfo.TypeOf(typedNode).(*types.Map)
					if ok && isNamedType(mapType.Elem(), providersPackagePath, "Client") && !providerOwnershipAllowed(loadedPackage.PkgPath) {
						appendFinding(findingProviderMap, "providers.Client", "map value type is providers.Client")
					}
				case *ast.CompositeLit:
					if isNamedType(loadedPackage.TypesInfo.TypeOf(typedNode), routerPackagePath, "Decision") && !targetConstructionAllowed(loadedPackage.PkgPath) {
						providerValue, modelValue, complete := decisionTarget(loadedPackage.TypesInfo, typedNode)
						if complete {
							appendFinding(findingTargetConstruction, "router.Decision", "provider="+providerValue+" model="+modelValue)
						}
					}
				case *ast.GenDecl:
					if typedNode.Tok == token.CONST && !targetConstantAllowed(loadedPackage.PkgPath) {
						for _, specNode := range typedNode.Specs {
							valueSpec, ok := specNode.(*ast.ValueSpec)
							if !ok {
								continue
							}
							for index, valueExpression := range valueSpec.Values {
								value, known := compileTimeString(loadedPackage.TypesInfo, valueExpression)
								if !known {
									continue
								}
								if _, target := knownTargets[value]; !target {
									continue
								}
								name := valueSpec.Names[index].Name
								appendFinding(findingFeatureTargetConstant, name, name+"="+value)
							}
						}
					}
				case *ast.CallExpr:
					object := calledObject(loadedPackage.TypesInfo, typedNode.Fun)
					if object != nil && object.Pkg() != nil {
						callee := object.Pkg().Path() + "." + object.Name()
						if object.Pkg().Path() == providersPackagePath && (object.Name() == "Proxy" || object.Name() == "Passthrough") && !providerCallAllowed(loadedPackage.PkgPath) {
							appendFinding(findingDirectProviderCall, callee, callee)
						}
						if isRawInferenceHTTPCall(loadedPackage.TypesInfo, typedNode, object) && !rawHTTPAllowed(loadedPackage.PkgPath) {
							appendFinding(findingRawHTTPClient, callee, callee)
						}
					}
				}
				return true
			})
		}
	}
	return finalizeFindings(pending)
}

func decisionTarget(info *types.Info, literal *ast.CompositeLit) (string, string, bool) {
	providerValue := "<dynamic>"
	modelValue := "<dynamic>"
	hasProvider := false
	hasModel := false
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := keyValue.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Provider":
			hasProvider = true
			if value, known := compileTimeString(info, keyValue.Value); known {
				providerValue = value
			}
		case "Model":
			hasModel = true
			if value, known := compileTimeString(info, keyValue.Value); known {
				modelValue = value
			}
		}
	}
	return providerValue, modelValue, hasProvider && hasModel
}

func compileTimeString(info *types.Info, expression ast.Expr) (string, bool) {
	value := info.Types[expression].Value
	if value == nil || value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(value), true
}

func calledObject(info *types.Info, expression ast.Expr) types.Object {
	switch callable := expression.(type) {
	case *ast.Ident:
		return info.Uses[callable]
	case *ast.SelectorExpr:
		if selection := info.Selections[callable]; selection != nil {
			return selection.Obj()
		}
		return info.Uses[callable.Sel]
	default:
		return nil
	}
}

// isPurposeType matches every typed purpose expression: explicit conversions,
// parenthesized conversions, typed declarations, and untyped constants passed
// where a Purpose is expected.
func isPurposeType(value types.Type) bool {
	if value == nil {
		return false
	}
	for _, packagePath := range purposePackagePaths {
		if isNamedType(value, packagePath, "Purpose") {
			return true
		}
	}
	return false
}

func isRawInferenceHTTPCall(info *types.Info, call *ast.CallExpr, object types.Object) bool {
	if object.Pkg() == nil || object.Pkg().Path() != "net/http" {
		return false
	}
	urlArgument := -1
	switch object.Name() {
	case "NewRequest":
		urlArgument = 1
	case "NewRequestWithContext":
		urlArgument = 2
	case "Get", "Post", "PostForm":
		function, ok := object.(*types.Func)
		if !ok {
			return false
		}
		signature, ok := function.Type().(*types.Signature)
		if !ok || (signature.Recv() != nil && !isNamedType(signature.Recv().Type(), "net/http", "Client")) {
			return false
		}
		urlArgument = 0
	default:
		return false
	}
	if urlArgument >= len(call.Args) {
		return false
	}
	url, known := compileTimeString(info, call.Args[urlArgument])
	if !known {
		return false
	}
	url = strings.ToLower(url)
	for _, inferencePath := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", ":generatecontent", ":streamgeneratecontent"} {
		if strings.Contains(url, inferencePath) {
			return true
		}
	}
	return false
}

func isNamedType(value types.Type, packagePath, name string) bool {
	value = types.Unalias(value)
	if pointer, ok := value.(*types.Pointer); ok {
		value = pointer.Elem()
	}
	named, ok := value.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == packagePath && named.Obj().Name() == name
}

func concreteProviderImportAllowed(packagePath string) bool {
	return packagePath == modulePath+"/cmd/router" || strings.HasPrefix(packagePath, providersPackagePath+"/")
}

func providerOwnershipAllowed(packagePath string) bool {
	return packagePath == modulePath+"/cmd/router" || strings.HasPrefix(packagePath, providersPackagePath) || strings.HasPrefix(packagePath, modulePath+"/internal/dispatch")
}

func providerCallAllowed(packagePath string) bool {
	return strings.HasPrefix(packagePath, providersPackagePath) || strings.HasPrefix(packagePath, modulePath+"/internal/dispatch")
}

func targetConstructionAllowed(packagePath string) bool {
	return packagePath == routerPackagePath || strings.HasPrefix(packagePath, routerPackagePath+"/") || strings.HasPrefix(packagePath, modulePath+"/internal/dispatch")
}

func targetConstantAllowed(packagePath string) bool {
	return strings.HasPrefix(packagePath, providersPackagePath) || packagePath == routerPackagePath || strings.HasPrefix(packagePath, routerPackagePath+"/")
}

func rawHTTPAllowed(packagePath string) bool {
	return strings.HasPrefix(packagePath, providersPackagePath+"/")
}

type functionRange struct {
	start  token.Pos
	end    token.Pos
	symbol string
}

func declaredFunctions(file *ast.File) []functionRange {
	var functions []functionRange
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		symbol := function.Name.Name
		if function.Recv != nil && len(function.Recv.List) > 0 {
			symbol = receiverName(function.Recv.List[0].Type) + "." + symbol
		}
		functions = append(functions, functionRange{start: function.Pos(), end: function.End(), symbol: symbol})
	}
	return functions
}

func receiverName(expression ast.Expr) string {
	switch receiver := expression.(type) {
	case *ast.Ident:
		return receiver.Name
	case *ast.StarExpr:
		return receiverName(receiver.X)
	case *ast.IndexExpr:
		return receiverName(receiver.X)
	case *ast.IndexListExpr:
		return receiverName(receiver.X)
	default:
		return "receiver"
	}
}

func enclosingSymbol(functions []functionRange, position token.Pos, packageName string) string {
	for _, function := range functions {
		if function.start <= position && position <= function.end {
			return function.symbol
		}
	}
	return packageName + ".package"
}

func finalizeFindings(pending []pendingFinding) []finding {
	sort.Slice(pending, func(i, j int) bool {
		left, right := pending[i], pending[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		if left.column != right.column {
			return left.column < right.column
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Detail < right.Detail
	})
	occurrences := make(map[string]int)
	findings := make([]finding, len(pending))
	for index, item := range pending {
		baseID := strings.Join([]string{string(item.Kind), item.File, item.Symbol, item.subject}, "|")
		occurrences[baseID]++
		item.ID = fmt.Sprintf("%s|%d", baseID, occurrences[baseID])
		findings[index] = item.finding
	}
	return findings
}

func compareBaseline(findings []finding, exceptions []exception) (missing []finding, stale []string) {
	currentByID := make(map[string]finding, len(findings))
	for _, item := range findings {
		currentByID[item.ID] = item
	}
	exceptionByID := make(map[string]exception, len(exceptions))
	for _, item := range exceptions {
		exceptionByID[item.ID] = item
	}
	for _, current := range findings {
		reviewed, found := exceptionByID[current.ID]
		if !found || withoutLine(reviewed.finding) != withoutLine(current) {
			missing = append(missing, current)
		}
	}
	for _, reviewed := range exceptions {
		if _, found := currentByID[reviewed.ID]; !found {
			stale = append(stale, reviewed.ID)
		}
	}
	sort.Strings(stale)
	return missing, stale
}

// withoutLine drops the advisory line number so unrelated edits above a
// reviewed exception do not count as a boundary change; IDs never include it.
func withoutLine(item finding) finding {
	item.Line = 0
	return item
}

func findingKinds(findings []finding) []findingKind {
	kinds := make([]findingKind, len(findings))
	for index, item := range findings {
		kinds[index] = item.Kind
	}
	return kinds
}

func readBaseline(t *testing.T) baseline {
	t.Helper()
	payload, err := os.ReadFile(baselinePath)
	require.NoError(t, err)
	var reviewed baseline
	require.NoError(t, json.Unmarshal(payload, &reviewed))
	return reviewed
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}
