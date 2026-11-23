package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	{path: "internal/architecture/env_ownership_test.go", parse: goAllowlists("envReadAllowlist")},
	{path: "internal/architecture/proxy_import_ratchet_test.go", parse: goAllowlists("proxyImportAllowlist")},
	{path: "internal/architecture/strategy_from_context_sites_test.go", parse: goAllowlists("strategyFromContextSites")},
}

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
	return violations, nil
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

func goAllowlists(names ...string) func([]byte) (baselineCounts, error) {
	return func(payload []byte) (baselineCounts, error) {
		file, err := parser.ParseFile(token.NewFileSet(), "", payload, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		counts := baselineCounts{}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				collectAllowlist(counts, spec, names)
			}
		}
		return counts, nil
	}
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
		entries := map[string]int{}
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
	if number, ok := pair.Value.(*ast.BasicLit); ok && number.Kind == token.INT {
		if count, err := strconv.Atoi(number.Value); err == nil {
			return literalText(pair.Key), count
		}
	}
	return literalText(pair.Key), 1
}

func literalText(expression ast.Expr) string {
	if literal, ok := expression.(*ast.BasicLit); ok && literal.Kind == token.STRING {
		if text, err := strconv.Unquote(literal.Value); err == nil {
			return text
		}
	}
	return types.ExprString(expression)
}
