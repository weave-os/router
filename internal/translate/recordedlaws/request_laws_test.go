package recordedlaws_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func requestChainDepth(format wireFormat) int {
	if format == responsesReq {
		return 2
	}
	return 1
}

func TestRecordedConversationRendersAgree(t *testing.T) {
	s := recordedScript(t)
	bodies := s.bodies()
	want := requestFacts(anthropicReq, bodies[anthropicReq])
	for format, body := range bodies {
		require.Empty(t, validateRequest(format, body), "%s rendering of the recorded conversation is not valid", format)
		require.Equal(t, want, requestFacts(format, body), "%s rendering disagrees with the Anthropic rendering", format)
	}
}

func TestRecordedConversationSurvivesEveryRequestPath(t *testing.T) {
	ledger := newLossLedger(requestLossAllowlist())
	check := lawCheck{ledger: ledger, factsOf: requestFacts, validator: validateRequest, family: responseFamily}
	hops := plainHops(recordedTargets(t).requestHops())
	for format, body := range recordedScript(t).bodies() {
		start := stage{format: format, body: body, facts: requestFacts(format, body)}
		for _, c := range chainsFrom(format, hops, requestChainDepth(format)) {
			if c.uses(string(chatReq)+">"+string(geminiReq)+"@"+geminiSignedTarget) || c.uses(string(anthropicReq)+">"+string(geminiReq)+"@"+geminiSignedTarget) {
				continue
			}
			t.Run(string(format)+"/"+c.name(), func(t *testing.T) {
				check.runChain(t, start, c)
			})
		}
	}
	for _, r := range ledger.stale() {
		t.Errorf("allowlisted request loss no longer happens; delete it: %s", r)
	}
}
