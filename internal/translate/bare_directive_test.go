package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/translate"
)

// The duplication this replaced was a drift hazard, not just repetition: the
// same two defects had to be fixed twice, once per copy. This pins the shared
// rules against BOTH directives at once, so a rule that reaches one and not
// the other fails here rather than shipping as an inconsistency.
func TestBareDirectives_ShareTheSameRules(t *testing.T) {
	extractors := map[string]struct {
		token   string
		extract func(*translate.RequestEnvelope) bool
	}{
		"router-models":  {"$router-models", (*translate.RequestEnvelope).ExtractRouterModelsCommand},
		"router-session": {"$router-session", (*translate.RequestEnvelope).ExtractRouterSessionCommand},
	}

	for name, e := range extractors {
		t.Run(name, func(t *testing.T) {
			fires := func(text string) bool {
				return e.extract(codexEnvelope(t, codexUserItem(text)))
			}

			assert.True(t, fires(e.token), "the bare directive must fire")

			// Leading line only.
			assert.False(t, fires("pasted transcript\n"+e.token),
				"a directive below other text is not a directive")
			assert.False(t, fires("see `"+e.token+"` for details"),
				"a mention inside prose is not a directive")

			// Nothing but the directive may remain: these short-circuit the
			// turn, so a leftover reaches no model.
			assert.False(t, fires(e.token+" and something"),
				"a same-line tail must fall through")
			assert.False(t, fires(e.token+"\nand something"),
				"a next-line tail must fall through")
			assert.False(t, fires(e.token+"\n<foo>tagged text</foo>"),
				"arbitrary tagged text is the user's, not a client wrapper")

			// ...except the client's own wrappers, which it appends itself.
			assert.True(t, fires(e.token+"\n<system-reminder>be brief</system-reminder>"),
				"a known client wrapper must not stop the directive")

			// Codex appends the skill blob as a second user message.
			assert.True(t, e.extract(codexEnvelope(t,
				codexUserItem(e.token),
				codexUserItem("<skill>\nname: x\n</skill>"),
			)), "the Codex skill attachment must not hide the directive")
		})
	}
}
