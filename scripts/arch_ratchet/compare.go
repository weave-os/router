package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
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

// indexFuncs keys every function by funcKey. When one key occurs more than once (build
// tag variants of a function in different files), each occurrence is qualified with its
// file so none overwrites another.
func indexFuncs(files map[string]fileMetrics) map[string]funcMetrics {
	occurrences := map[string]int{}
	for _, file := range files {
		for _, fn := range file.funcs {
			occurrences[fn.key]++
		}
	}
	funcs := map[string]funcMetrics{}
	for _, filePath := range sortedKeys(files) {
		for _, fn := range files[filePath].funcs {
			if occurrences[fn.key] > 1 {
				fn.key += "@" + filePath
			}
			key := fn.key
			for n := 2; funcs[fn.key].values != nil; n++ {
				fn.key = fmt.Sprintf("%s#%d", key, n)
			}
			funcs[fn.key] = fn
		}
	}
	return funcs
}

// compareFuncs flags each function metric over its limit that grew. A function new to
// the diff passes as moved when it claims a distinct removed function at least as large
// on every metric it exceeds; each removed function covers at most one move.
func compareFuncs(base, head map[string]fileMetrics, allowed exceptionSet) []violation {
	baseFuncs, headFuncs := indexFuncs(base), indexFuncs(head)
	moves := newMovePool(baseFuncs, headFuncs)
	var violations []violation
	for _, key := range sortedKeys(headFuncs) {
		fn := headFuncs[key]
		previous, existed := baseFuncs[key]
		if !existed && moves.claim(fn) {
			continue
		}
		for _, m := range slices.Sorted(maps.Keys(funcLimits)) {
			after, before, limit := fn.values[m], previous.values[m], funcLimits[m]
			if after > limit && after > before && !allowed.permits(key, m, after) {
				violations = append(violations, violation{subject: key, metric: m, before: before, after: after, limit: limit})
			}
		}
	}
	return violations
}

type movePool map[string]funcMetrics

func newMovePool(baseFuncs, headFuncs map[string]funcMetrics) movePool {
	pool := movePool{}
	for key, fn := range baseFuncs {
		if _, kept := headFuncs[key]; !kept {
			pool[key] = fn
		}
	}
	return pool
}

// claim removes and reports the removed function that best explains fn as a move:
// one with the same name if possible, otherwise the smallest that covers fn.
func (p movePool) claim(fn funcMetrics) bool {
	var over []metric
	for m, limit := range funcLimits {
		if fn.values[m] > limit {
			over = append(over, m)
		}
	}
	if len(over) == 0 {
		return false
	}
	best := ""
	for _, key := range sortedKeys(p) {
		if covers(p[key], fn, over) && (best == "" || betterMove(fn, key, best, p)) {
			best = key
		}
	}
	if best == "" {
		return false
	}
	delete(p, best)
	return true
}

func covers(removed, fn funcMetrics, over []metric) bool {
	for _, m := range over {
		if removed.values[m] < fn.values[m] {
			return false
		}
	}
	return true
}

func betterMove(fn funcMetrics, candidate, current string, p movePool) bool {
	name := shortName(fn.key)
	candidateSame, currentSame := shortName(candidate) == name, shortName(current) == name
	if candidateSame != currentSame {
		return candidateSame
	}
	return size(p[candidate]) < size(p[current])
}

// shortName strips the package directory and any file qualifier from a function key.
func shortName(key string) string {
	key, _, _ = strings.Cut(key, "@")
	rest := key[strings.LastIndex(key, "/")+1:]
	_, name, _ := strings.Cut(rest, ".")
	return name
}

func size(fn funcMetrics) int {
	total := 0
	for _, value := range fn.values {
		total += value
	}
	return total
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
