package recordedlaws_test

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
)

type diffKind string

const (
	dropped diffKind = "dropped"
	added   diffKind = "added"
	changed diffKind = "changed"
	shape   diffKind = "shape"
)

type factDiff struct {
	key           string
	kind          diffKind
	before, after string
}

func (d factDiff) String() string {
	return fmt.Sprintf("%s %s: %q -> %q", d.kind, d.key, clip(d.before), clip(d.after))
}

func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

func diffFacts(before, after facts) []factDiff {
	var out []factDiff
	for k, b := range before {
		a, ok := after[k]
		switch {
		case !ok:
			out = append(out, factDiff{key: k, kind: dropped, before: b})
		case a != b:
			out = append(out, factDiff{key: k, kind: changed, before: b, after: a})
		}
	}
	for k, a := range after {
		if _, ok := before[k]; !ok {
			out = append(out, factDiff{key: k, kind: added, after: a})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

type lossRule struct {
	hop       string
	key       string
	kind      diffKind
	holds     func(before, after string) bool
	why       string
	candidate bool
}

func (r lossRule) String() string {
	label := "intentional"
	if r.candidate {
		label = "candidate bug"
	}
	return fmt.Sprintf("%s %s %s [%s] (%s)", r.hop, r.kind, r.key, label, r.why)
}

func (r lossRule) hopMatches(hop string) bool {
	ok, err := path.Match(r.hop, hop)
	return err == nil && ok
}

func dottedKeyMatches(pattern, value string) bool {
	ok, err := path.Match(strings.ReplaceAll(pattern, ".", "/"), strings.ReplaceAll(value, ".", "/"))
	return err == nil && ok
}

func (r lossRule) explains(hop string, d factDiff) bool {
	if !r.hopMatches(hop) || r.kind != d.kind || !dottedKeyMatches(r.key, d.key) {
		return false
	}
	return r.holds == nil || r.holds(d.before, d.after)
}

type lossLedger struct {
	mu    sync.Mutex
	rules []lossRule
	used  map[int]bool
}

func newLossLedger(rules []lossRule) *lossLedger {
	return &lossLedger{rules: rules, used: map[int]bool{}}
}

func (l *lossLedger) unexplained(hop string, diffs []factDiff) []factDiff {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []factDiff
	for _, d := range diffs {
		explained := false
		for i, r := range l.rules {
			if r.explains(hop, d) {
				l.used[i] = true
				explained = true
			}
		}
		if !explained {
			out = append(out, d)
		}
	}
	return out
}

func (l *lossLedger) unexplainedShapes(hop string, found []string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, problem := range found {
		explained := false
		for i, r := range l.rules {
			if r.kind == shape && r.hopMatches(hop) && strings.Contains(problem, r.key) {
				l.used[i] = true
				explained = true
			}
		}
		if !explained {
			out = append(out, problem)
		}
	}
	return out
}

func (l *lossLedger) stale() []lossRule {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []lossRule
	for i, r := range l.rules {
		if !l.used[i] {
			out = append(out, r)
		}
	}
	return out
}

func hasPrefixOf(before, after string) bool { return strings.HasPrefix(after, before) }
