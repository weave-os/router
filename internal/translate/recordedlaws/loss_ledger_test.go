package recordedlaws_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLossLedgerFailsOnUnlistedAndStaleLosses(t *testing.T) {
	ledger := newLossLedger([]lossRule{
		{hop: "a>b", kind: dropped, key: "tool.*.id", why: "listed"},
		{hop: "a>b", kind: changed, key: "tool.*.id", holds: nonceSuffixed, why: "nonce"},
		{hop: "a>b", kind: dropped, key: "usage.output", why: "never happens"},
	})
	diffs := diffFacts(
		facts{"tool.0.id": "x", "tool.1.id": "call_b", "text": "hi", "stop": "end_turn"},
		facts{"tool.1.id": "call_b_0123456789ab", "text": "hi!", "usage.output": "3"},
	)
	unexplained := ledger.unexplained("a>b", diffs)
	assert.ElementsMatch(t, []factDiff{
		{key: "stop", kind: dropped, before: "end_turn"},
		{key: "text", kind: changed, before: "hi", after: "hi!"},
		{key: "usage.output", kind: added, after: "3"},
	}, unexplained)
	assert.Equal(t, []lossRule{ledger.rules[2]}, ledger.stale())
	assert.Len(t, ledger.unexplained("other>hop", diffs), len(diffs), "a rule scoped to one hop explains nothing on another")
}
