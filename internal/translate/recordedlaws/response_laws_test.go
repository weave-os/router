package recordedlaws_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func responseFamily(f wireFormat) string {
	family, _, _ := strings.Cut(string(f), "_")
	return family
}

func TestRecordedResponsesSurviveEveryTranslationChain(t *testing.T) {
	ledger := newLossLedger(responseLossAllowlist())
	check := lawCheck{ledger: ledger, factsOf: responseFacts, validator: validate, family: responseFamily}
	hops := responseHops()
	ran := 0
	for _, rec := range responseCorpus(t) {
		start := stage{format: rec.format, body: rec.body, facts: responseFacts(rec.format, rec.body)}
		require.NotEmpty(t, start.facts, "%s yields no semantic facts", rec.name)
		for _, c := range chainsFrom(rec.format, hops, 1) {
			ran++
			t.Run(string(rec.origin)+"/"+rec.name+"/"+c.name(), func(t *testing.T) {
				check.runChain(t, start, c)
			})
		}
	}
	require.Positive(t, ran)
	for _, r := range ledger.stale() {
		t.Errorf("allowlisted loss no longer happens; delete it: %s", r)
	}
}
