package main

import (
	"cmp"
	"fmt"
	"slices"
)

type violation struct {
	subject string
	metric  metric
	before  int
	after   int
	limit   int
}

func (v violation) String() string {
	if v.limit < 0 {
		return fmt.Sprintf("%s: %s %d -> %d (may not grow)", v.subject, v.metric, v.before, v.after)
	}
	return fmt.Sprintf("%s: %s %d -> %d (limit %d; over-limit code may not grow, new code must meet the limit)",
		v.subject, v.metric, v.before, v.after, v.limit)
}

func indexFuncs(files map[string]fileMetrics) map[string]funcMetrics {
	funcs := map[string]funcMetrics{}
	for _, file := range files {
		for _, fn := range file.funcs {
			funcs[fn.key] = fn
		}
	}
	return funcs
}

// compareFuncs flags each function metric over its limit that grew. A function new to
// the diff is treated as moved when a function removed in the same diff was at least as
// large on that metric.
func compareFuncs(base, head map[string]fileMetrics, allowed exceptionSet) []violation {
	baseFuncs, headFuncs := indexFuncs(base), indexFuncs(head)
	removed := map[metric]int{}
	for key, fn := range baseFuncs {
		if _, kept := headFuncs[key]; kept {
			continue
		}
		for m, value := range fn.values {
			removed[m] = max(removed[m], value)
		}
	}
	var violations []violation
	for _, key := range sortedKeys(headFuncs) {
		previous, existed := baseFuncs[key]
		for m, limit := range funcLimits {
			after := headFuncs[key].values[m]
			before := previous.values[m]
			grew := after > limit && after > before
			moved := !existed && removed[m] >= after
			if grew && !moved && !allowed.permits(key, m, after) {
				violations = append(violations, violation{subject: key, metric: m, before: before, after: after, limit: limit})
			}
		}
	}
	return violations
}

func compareFiles(base, head map[string]fileMetrics, allowed exceptionSet) []violation {
	var violations []violation
	for _, filePath := range sortedKeys(head) {
		after, before := head[filePath].codeLines, base[filePath].codeLines
		if after > limitFileLines && after > before && !allowed.permits(filePath, metricFileLines, after) {
			violations = append(violations, violation{subject: filePath, metric: metricFileLines, before: before, after: after, limit: limitFileLines})
		}
	}
	return violations
}

// compareTotals sums counts over the changed files, so moving a line between files is
// neutral while adding one is not.
func compareTotals(base, head map[string]fileMetrics, allowed exceptionSet) []violation {
	totals := func(files map[string]fileMetrics) (nolint, matching int) {
		for _, file := range files {
			nolint += file.nolint
			matching += file.modelMatching
		}
		return nolint, matching
	}
	baseNolint, baseMatching := totals(base)
	headNolint, headMatching := totals(head)
	var violations []violation
	violations = appendGrowth(violations, "//nolint directives in changed files", metricNolint, baseNolint, headNolint, allowed)
	return appendGrowth(violations, "model/provider name matching outside "+catalogDir, metricModelMatching, baseMatching, headMatching, allowed)
}

func compareSurface(base, head proxySurface, allowed exceptionSet) []violation {
	var violations []violation
	violations = appendGrowth(violations, "proxy.Service", metricServiceFields, base.serviceFields, head.serviceFields, allowed)
	violations = appendGrowth(violations, "proxy.Service", metricServiceMethods, base.serviceMethods, head.serviceMethods, allowed)
	return appendGrowth(violations, proxyDir, metricContextKeys, base.contextKeys, head.contextKeys, allowed)
}

func appendGrowth(violations []violation, subject string, m metric, before, after int, allowed exceptionSet) []violation {
	if after <= before || allowed.permits(subject, m, after) {
		return violations
	}
	return append(violations, violation{subject: subject, metric: m, before: before, after: after, limit: -1})
}

func sortViolations(violations []violation) {
	slices.SortFunc(violations, func(a, b violation) int {
		return cmp.Or(cmp.Compare(a.subject, b.subject), cmp.Compare(a.metric, b.metric))
	})
}
