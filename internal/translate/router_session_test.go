package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractRouterSessionCommand_BothSigils(t *testing.T) {
	for _, directive := range []string{"/router-session", "$router-session"} {
		t.Run(directive, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(directive))
			require.True(t, env.ExtractRouterSessionCommand(), "expected %q to be recognized", directive)
		})
	}
}

// The whole point of the directive is that it never reaches a model, so the
// Codex skill blob that trails the typed text must not hide it -- the same
// regression #1257 fixed for $fm and $rf.
func TestExtractRouterSessionCommand_CodexSkillBlobFollowsDirective(t *testing.T) {
	env := codexEnvelope(t,
		codexUserItem("$router-session"),
		codexUserItem("<skill>\nname: router-session\nPrint the session id.\n</skill>"),
	)
	require.True(t, env.ExtractRouterSessionCommand())
}

// A skill block that follows an assistant turn is a real turn, not an
// attachment; treating it as one would re-fire a directive from an older turn.
func TestExtractRouterSessionCommand_SkillBlobAfterAssistantIsNotADirective(t *testing.T) {
	env := codexEnvelope(t,
		codexUserItem("$router-session"),
		codexAssistantItem("Weave Router: session id: abc-123."),
		codexUserItem("<skill>\nname: router-session\n</skill>"),
	)
	assert.False(t, env.ExtractRouterSessionCommand())
}

func TestExtractRouterSessionCommand_IgnoresNonLeadingAndArgumentForms(t *testing.T) {
	for name, text := range map[string]string{
		"not on the leading line": "here is a transcript\n$router-session",
		"pasted with a prefix":    "see `$router-session` for the id",
		"carries an argument":     "$router-session please",
		"different directive":     "$router-status",
	} {
		t.Run(name, func(t *testing.T) {
			env := codexEnvelope(t, codexUserItem(text))
			assert.False(t, env.ExtractRouterSessionCommand(), "should not match %q", text)
		})
	}
}
