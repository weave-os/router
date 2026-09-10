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

// An argument on its own line is still an argument. Matching the bare token
// and dropping the rest would answer with a listing while silently discarding
// the mutation the user asked for.
func TestExtractRouterModelsCommand_ArgumentOnTheNextLineStillFallsThrough(t *testing.T) {
	for _, text := range []string{
		"$router-models\nenable gpt-5.5",
		"$router-models\n\ndisable gpt-5.5",
		"/router-models\nprefer claude-opus-5",
	} {
		t.Run(text, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(text))
			assert.False(t, env.ExtractRouterModelsCommand(),
				"%q carries a mutating argument and must reach the skill", text)
		})
	}
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

// A tag is not a licence to discard. leadingInjectedPrefixEnd accepts any
// well-formed <name>...</name>, so the earlier guard treated arbitrary tagged
// text as synthetic and dropped it -- here, a mutating argument.
func TestExtractRouterModelsCommand_TaggedArgumentIsNotSynthetic(t *testing.T) {
	for _, text := range []string{
		"$router-models\n<foo>enable gpt-5.5</foo>",
		"$router-models\n<note>disable gpt-5.5</note>",
	} {
		t.Run(text, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(text))
			assert.False(t, env.ExtractRouterModelsCommand(),
				"%q is the user's text, not a client wrapper", text)
		})
	}
}

// The client's OWN wrappers still have to pass, or the directive breaks for
// every Claude Code user: it appends these to user messages.
func TestExtractRouterModelsCommand_ClientWrappersStillCount(t *testing.T) {
	for _, text := range []string{
		"/router-models\n<system-reminder>be concise</system-reminder>",
		"<command-name>/router-models</command-name>\n/router-models",
	} {
		t.Run(text, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(text))
			assert.True(t, env.ExtractRouterModelsCommand(),
				"%q carries only client-injected wrappers", text)
		})
	}
}
