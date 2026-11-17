package recordedlaws_test

import (
	"strings"
	"testing"
)

type chain []hop

func (c chain) name() string {
	parts := make([]string, 0, len(c))
	for _, h := range c {
		parts = append(parts, h.name())
	}
	return strings.Join(parts, "+")
}

func chainsFrom(start wireFormat, hops []hop, depth int) []chain {
	var out []chain
	var walk func(at wireFormat, prefix chain)
	walk = func(at wireFormat, prefix chain) {
		if len(prefix) == depth {
			return
		}
		for _, h := range hops {
			if h.from != at || prefix.uses(h.name()) {
				continue
			}
			next := append(append(chain{}, prefix...), h)
			out = append(out, next)
			walk(h.to, next)
		}
	}
	walk(start, nil)
	return out
}

func (c chain) uses(name string) bool {
	for _, x := range c {
		if x.name() == name {
			return true
		}
	}
	return false
}

type stage struct {
	format wireFormat
	body   []byte
	facts  facts
}

type lawCheck struct {
	ledger    *lossLedger
	factsOf   func(wireFormat, []byte) facts
	validator func(wireFormat, []byte) []string
	family    func(wireFormat) string
}

func (lc lawCheck) runChain(t *testing.T, start stage, c chain) {
	t.Helper()
	cur := start
	touched := map[string]bool{}
	for _, h := range c {
		body := h.run(t, cur.body)
		for _, problem := range lc.ledger.unexplainedShapes(h.name(), lc.validator(h.to, body)) {
			t.Errorf("%s output is not valid %s: %s", h.name(), h.to, problem)
		}
		next := stage{format: h.to, body: body, facts: lc.factsOf(h.to, body)}
		diffs := diffFacts(cur.facts, next.facts)
		for _, d := range diffs {
			touched[d.key] = true
		}
		for _, d := range lc.ledger.unexplained(h.name(), diffs) {
			t.Errorf("%s loses a field outside the allowlist: %s", h.name(), d)
		}
		cur = next
	}
	if lc.family(cur.format) != lc.family(start.format) {
		return
	}
	for _, d := range diffFacts(start.facts, cur.facts) {
		if !touched[d.key] {
			t.Errorf("round trip %s changes %s although no hop reported it", c.name(), d)
		}
	}
}
