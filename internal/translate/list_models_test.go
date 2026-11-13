package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractListModelsCommand_BareFormsInBothSigils(t *testing.T) {
	for _, directive := range []string{"/list-models", "$list-models"} {
		t.Run(directive, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(directive))
			require.True(t, env.ExtractListModelsCommand(), "expected %q to be recognized", directive)
		})
	}
}

func TestExtractListModelsCommand_CodexSkillBlobFollowsDirective(t *testing.T) {
	env := codexEnvelope(t,
		codexUserItem("$list-models"),
		codexUserItem("<skill>\nname: list-models\nList the forceable models.\n</skill>"),
	)
	require.True(t, env.ExtractListModelsCommand())
}

// The directive short-circuits the turn, so anything typed with it would reach
// no model; only the bare form is answered.
func TestExtractListModelsCommand_TextAfterTheDirectiveIsNotAnswered(t *testing.T) {
	for _, text := range []string{
		"$list-models anthropic",
		"$list-models\nthen force the cheapest one",
		"please run /list-models",
	} {
		t.Run(text, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(text))
			assert.False(t, env.ExtractListModelsCommand(), "%q must reach the model", text)
		})
	}
}

func TestExtractListModelsCommand_DoesNotClaimOtherDirectives(t *testing.T) {
	env := codexEnvelope(t, codexUserItem("$router-models"))
	assert.False(t, env.ExtractListModelsCommand())
}
