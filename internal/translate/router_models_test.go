package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractRouterModelsCommand_BareFormsInBothSigils(t *testing.T) {
	for _, directive := range []string{
		"/router-models", "$router-models", "/models", "$models",
	} {
		t.Run(directive, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(directive))
			require.True(t, env.ExtractRouterModelsCommand(), "expected %q to be recognized", directive)
		})
	}
}

// Mutation needs admin auth the chat key deliberately lacks, so an argument
// form must fall through to the skill rather than be answered here.
func TestExtractRouterModelsCommand_ArgumentFormsFallThroughToTheSkill(t *testing.T) {
	for _, text := range []string{
		"$router-models enable gpt-5.5",
		"$router-models disable gpt-5.5",
		"$router-models prefer claude-opus-5 gpt-5.5",
		"$router-models providers",
		"$models enable gpt-5.5",
	} {
		t.Run(text, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(text))
			assert.False(t, env.ExtractRouterModelsCommand(),
				"%q mutates admin state and must not be short-circuited", text)
		})
	}
}

func TestExtractRouterModelsCommand_CodexSkillBlobFollowsDirective(t *testing.T) {
	env := codexEnvelope(t,
		codexUserItem("$router-models"),
		codexUserItem("<skill>\nname: router-models\nList the models.\n</skill>"),
	)
	require.True(t, env.ExtractRouterModelsCommand())
}

func TestExtractRouterModelsCommand_IgnoresNonLeadingUses(t *testing.T) {
	for name, text := range map[string]string{
		"not on the leading line": "notes below\n$router-models",
		"mentioned inline":        "run `$router-models` to see the list",
	} {
		t.Run(name, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(text))
			assert.False(t, env.ExtractRouterModelsCommand(), "should not match %q", text)
		})
	}
}
