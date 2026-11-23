package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const metricBaselineGrowth metric = "baseline_growth"

type baselineCounts map[string]map[string]int

type baselineSource struct {
	path  string
	parse func([]byte) (baselineCounts, error)
}

var baselineSources = []baselineSource{
	{path: "internal/architecture/atomization_ratchet_baseline.json", parse: parseAtomizationBaseline},
	{path: "internal/architecture/inference_boundary_baseline.json", parse: parseInferenceBaseline},
}

const goAllowlistDir = "internal/architecture"

var goAllowlistNames = []string{"envReadAllowlist", "proxyImportAllowlist", "strategyFromContextSites", "strategyRouterWrappers", "strategyRouteCallSites"}

func compareBaselines(ctx context.Context, r repo, baseRev string, allowed exceptionSet) ([]violation, error) {
	var violations []violation
	for _, source := range baselineSources {
		baseRaw, err := r.readAt(ctx, baseRev, []string{source.path})
		if err != nil {
			return nil, err
		}
		basePayload, tracked := baseRaw[source.path]
		if !tracked {
			continue
		}
		headPayload, err := os.ReadFile(filepath.Join(r.root, source.path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", source.path, err)
		}
		base, err := source.parse(basePayload)
		if err != nil {
			return nil, fmt.Errorf("parse %s at %s: %w", source.path, short(baseRev), err)
		}
		head, err := source.parse(headPayload)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", source.path, err)
		}
		violations = append(violations, baselineGrowth(source.path, base, head, allowed)...)
	}
	goViolations, err := compareGoAllowlists(ctx, r, baseRev, allowed)
	if err != nil {
		return nil, err
	}
	return append(violations, goViolations...), nil
}

func compareGoAllowlists(ctx context.Context, r repo, baseRev string, allowed exceptionSet) ([]violation, error) {
	listed, err := r.git(ctx, "ls-tree", "--name-only", "-z", baseRev, goAllowlistDir+"/")
	if err != nil {
		return nil, err
	}
	baseSources, err := r.readAt(ctx, baseRev, goFilesIn(splitNul(listed)))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(r.root, goAllowlistDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", goAllowlistDir, err)
	}
	var headPaths []string
	for _, entry := range entries {
		headPaths = append(headPaths, path.Join(goAllowlistDir, entry.Name()))
	}
	headSources, err := r.readWorktree(goFilesIn(headPaths))
	if err != nil {
		return nil, err
	}
	base, err := parseGoAllowlists(baseSources)
	if err != nil {
		return nil, fmt.Errorf("at %s: %w", short(baseRev), err)
	}
	head, err := parseGoAllowlists(headSources)
	if err != nil {
		return nil, err
	}
	return baselineGrowth(goAllowlistDir, base, head, allowed), nil
}

func goFilesIn(paths []string) []string {
	var files []string
	for _, candidate := range paths {
		if strings.HasSuffix(candidate, ".go") && path.Dir(candidate) == goAllowlistDir {
			files = append(files, candidate)
		}
	}
	return files
}

func parseGoAllowlists(sources map[string][]byte) (baselineCounts, error) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for name, payload := range sources {
		file, err := parser.ParseFile(fset, name, payload, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		files[name] = file
	}
	scope := packageConstants(fset, files)
	counts := baselineCounts{}
	for _, name := range sortedKeys(files) {
		if err := collectGoAllowlists(counts, scope, files[name]); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	return counts, nil
}

func packageConstants(fset *token.FileSet, files map[string]*ast.File) *constantScope {
	var decls []ast.Decl
	for _, name := range sortedKeys(files) {
		for _, decl := range files[name].Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				decls = append(decls, gen)
			}
		}
	}
	config := types.Config{Error: func(error) {}}
	pkg, _ := config.Check("constants", fset, []*ast.File{{Name: ast.NewIdent("constants"), Decls: decls}}, nil)
	return &constantScope{fset: fset, pkg: pkg}
}

type constantScope struct {
	fset *token.FileSet
	pkg  *types.Package
}

func baselineGrowth(path string, base, head baselineCounts, allowed exceptionSet) []violation {
	var violations []violation
	for _, group := range sortedKeys(head) {
		before, compared := base[group]
		if !compared {
			continue
		}
		for _, key := range sortedKeys(head[group]) {
			after := head[group][key]
			subject := path + "#" + group + "/" + key
			if after <= before[key] || allowed.permits(subject, metricBaselineGrowth, after) {
				continue
			}
			violations = append(violations, violation{subject: subject, metric: metricBaselineGrowth, before: before[key], after: after, limit: -1})
		}
	}
	return violations
}

func parseAtomizationBaseline(payload []byte) (baselineCounts, error) {
	var baseline struct {
		Ratchets baselineCounts `json:"ratchets"`
	}
	if err := json.Unmarshal(payload, &baseline); err != nil {
		return nil, err
	}
	return baseline.Ratchets, nil
}

func parseInferenceBaseline(payload []byte) (baselineCounts, error) {
	var baseline struct {
		Exceptions []struct {
			Kind string `json:"kind"`
		} `json:"exceptions"`
	}
	if err := json.Unmarshal(payload, &baseline); err != nil {
		return nil, err
	}
	kinds := map[string]int{}
	for _, entry := range baseline.Exceptions {
		kinds[entry.Kind]++
	}
	return baselineCounts{"kind": kinds}, nil
}

func collectGoAllowlists(counts baselineCounts, scope *constantScope, file *ast.File) error {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			if err := collectAllowlist(counts, scope, spec, goAllowlistNames); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectAllowlist(counts baselineCounts, scope *constantScope, spec ast.Spec, names []string) error {
	value, ok := spec.(*ast.ValueSpec)
	if !ok {
		return nil
	}
	for i, name := range value.Names {
		if !slices.Contains(names, name.Name) {
			continue
		}
		if i >= len(value.Values) {
			return fmt.Errorf("%s: want a composite literal initializer", name.Name)
		}
		literal, ok := ast.Unparen(value.Values[i]).(*ast.CompositeLit)
		if !ok {
			return fmt.Errorf("%s: want a composite literal initializer, got %s", name.Name, types.ExprString(value.Values[i]))
		}
		entries := counts[name.Name]
		if entries == nil {
			entries = map[string]int{}
		}
		for _, element := range literal.Elts {
			key, count, err := scope.allowlistEntry(element)
			if err != nil {
				return fmt.Errorf("%s: %w", name.Name, err)
			}
			entries[key] += count
		}
		counts[name.Name] = entries
	}
	return nil
}

func (scope *constantScope) allowlistEntry(element ast.Expr) (string, int, error) {
	pair, ok := element.(*ast.KeyValueExpr)
	if !ok {
		key, err := scope.constantString(element)
		return key, 1, err
	}
	key, err := scope.constantString(pair.Key)
	if err != nil {
		return "", 0, err
	}
	value, err := scope.constantOf(pair.Value)
	if err != nil {
		return "", 0, err
	}
	if value.Kind() != constant.Int {
		return key, 1, nil
	}
	count, exact := constant.Int64Val(value)
	if !exact {
		return "", 0, fmt.Errorf("count %s overflows int64", value)
	}
	return key, int(count), nil
}

func (scope *constantScope) constantString(expression ast.Expr) (string, error) {
	value, err := scope.constantOf(expression)
	if err != nil {
		return "", err
	}
	if value.Kind() != constant.String {
		return "", fmt.Errorf("key %s is not a string constant", types.ExprString(expression))
	}
	return constant.StringVal(value), nil
}

func (scope *constantScope) constantOf(expression ast.Expr) (constant.Value, error) {
	evaluated, err := types.Eval(scope.fset, scope.pkg, token.NoPos, types.ExprString(expression))
	if err != nil {
		return nil, fmt.Errorf("evaluate %s as a constant: %w", types.ExprString(expression), err)
	}
	if evaluated.Value == nil {
		return nil, fmt.Errorf("%s is not a constant", types.ExprString(expression))
	}
	return evaluated.Value, nil
}
