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

var goAllowlistNames = []string{"envReadAllowlist", "proxyImportAllowlist", "strategyFromContextSites"}

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
	counts := baselineCounts{}
	for _, name := range sortedKeys(sources) {
		if err := collectGoAllowlists(counts, sources[name]); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	return counts, nil
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

func collectGoAllowlists(counts baselineCounts, payload []byte) error {
	file, err := parser.ParseFile(token.NewFileSet(), "", payload, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			collectAllowlist(counts, spec, goAllowlistNames)
		}
	}
	return nil
}

func collectAllowlist(counts baselineCounts, spec ast.Spec, names []string) {
	value, ok := spec.(*ast.ValueSpec)
	if !ok {
		return
	}
	for i, name := range value.Names {
		if i >= len(value.Values) || !slices.Contains(names, name.Name) {
			continue
		}
		literal, ok := value.Values[i].(*ast.CompositeLit)
		if !ok {
			continue
		}
		entries := counts[name.Name]
		if entries == nil {
			entries = map[string]int{}
		}
		for _, element := range literal.Elts {
			key, count := allowlistEntry(element)
			entries[key] += count
		}
		counts[name.Name] = entries
	}
}

func allowlistEntry(element ast.Expr) (string, int) {
	pair, ok := element.(*ast.KeyValueExpr)
	if !ok {
		return literalText(element), 1
	}
	if value := constantOf(pair.Value); value != nil && value.Kind() == constant.Int {
		if count, exact := constant.Int64Val(value); exact {
			return literalText(pair.Key), int(count)
		}
	}
	return literalText(pair.Key), 1
}

func literalText(expression ast.Expr) string {
	if value := constantOf(expression); value != nil && value.Kind() == constant.String {
		return constant.StringVal(value)
	}
	return types.ExprString(expression)
}

func constantOf(expression ast.Expr) constant.Value {
	evaluated, err := types.Eval(token.NewFileSet(), nil, token.NoPos, types.ExprString(expression))
	if err != nil {
		return nil
	}
	return evaluated.Value
}
