package main

import (
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
	note    string
}

func (v violation) String() string {
	if v.note != "" {
		return fmt.Sprintf("%s: %s; %s", v.subject, v.metric, v.note)
	}
	if v.limit < 0 {
		return fmt.Sprintf("%s: %s %d -> %d (may not grow)", v.subject, v.metric, v.before, v.after)
	}
	return fmt.Sprintf("%s: %s %d -> %d (limit %d; over-limit code may not grow, new code must meet the limit)",
		v.subject, v.metric, v.before, v.after, v.limit)
}

func indexFuncs(files map[string]fileMetrics, occurrences map[string]int) map[string]funcMetrics {
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

func compareFuncs(base, head map[string]fileMetrics, allowed exceptionSet) []violation {
	occurrences := map[string]int{}
	for _, files := range []map[string]fileMetrics{base, head} {
		side := map[string]int{}
		for _, file := range files {
			for _, fn := range file.funcs {
				side[fn.key]++
			}
		}
		for key, n := range side {
			occurrences[key] = max(occurrences[key], n)
		}
	}
	baseFuncs, headFuncs := indexFuncs(base, occurrences), indexFuncs(head, occurrences)
	moves := newMovePool(baseFuncs, headFuncs)
	exactMoves := moves.claimExact(headFuncs, baseFuncs)
	var violations []violation
	for _, key := range sortedKeys(headFuncs) {
		fn := headFuncs[key]
		previous, existed := baseFuncs[key]
		if !existed && (exactMoves[key] || moves.claim(fn)) {
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

func (p movePool) claimExact(headFuncs, baseFuncs map[string]funcMetrics) map[string]bool {
	claimed := map[string]bool{}
	for _, key := range sortedKeys(headFuncs) {
		if _, existed := baseFuncs[key]; existed {
			continue
		}
		fn := headFuncs[key]
		for _, candidate := range sortedKeys(p) {
			if shortName(candidate) == shortName(key) && p[candidate].body == fn.body && maps.Equal(p[candidate].values, fn.values) {
				claimed[key] = true
				delete(p, candidate)
				break
			}
		}
	}
	return claimed
}

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
		tooSmall := slices.ContainsFunc(over, func(m metric) bool { return p[key].values[m] < fn.values[m] })
		if !tooSmall && (best == "" || betterMove(fn, key, best, p)) {
			best = key
		}
	}
	if best == "" {
		return false
	}
	delete(p, best)
	return true
}

func betterMove(fn funcMetrics, candidate, current string, p movePool) bool {
	name := shortName(fn.key)
	candidateSame, currentSame := shortName(candidate) == name, shortName(current) == name
	if candidateSame != currentSame {
		return candidateSame
	}
	candidateSize, currentSize := 0, 0
	for _, value := range p[candidate].values {
		candidateSize += value
	}
	for _, value := range p[current].values {
		currentSize += value
	}
	return candidateSize < currentSize
}

func shortName(key string) string {
	key, _, _ = strings.Cut(key, "@")
	_, name, _ := strings.Cut(key[strings.LastIndex(key, "/")+1:], ".")
	return name
}

func compareFiles(base, head map[string]fileMetrics, allowed exceptionSet) []violation {
	var violations []violation
	for _, filePath := range sortedKeys(head) {
		if strings.HasSuffix(filePath, "_test.go") {
			continue
		}
		after, before := head[filePath].codeLines, base[filePath].codeLines
		if after > limitFileLines && after > before && !allowed.permits(filePath, metricFileLines, after) {
			violations = append(violations, violation{subject: filePath, metric: metricFileLines, before: before, after: after, limit: limitFileLines})
		}
	}
	return violations
}

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

func appendGrowth(violations []violation, subject string, m metric, before, after int, allowed exceptionSet) []violation {
	if after <= before || allowed.permits(subject, m, after) {
		return violations
	}
	return append(violations, violation{subject: subject, metric: m, before: before, after: after, limit: -1})
}
