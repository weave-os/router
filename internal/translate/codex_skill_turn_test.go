package translate_test

// Codex splits one user turn into the typed directive plus a message carrying
// the invoked skill's SKILL.md. These pin the trailing-turn scan against that
// real wire shape, captured from codex-cli 0.153.4 against a logging proxy.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

const codexSkillBlob = "<skill>\n<name>fm</name>\n<path>/x/skills/fm/SKILL.md</path>\n---\nname: fm\ndescription: \"Alias for force-model.\"\n---\nRun this skill's scripts/emit.sh.\n</skill>"

func codexUserItem(text string) any {
	return map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}
}

// codexEnvelope mirrors the Responses ingress: convert to Chat Completions the
// way the proxy does, then parse, so the test exercises the real path rather
// than a hand-built message array.
func codexEnvelope(t *testing.T, items ...any) *translate.RequestEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "gpt-5.6-sol", "input": items})
	require.NoError(t, err)
	conv, err := translate.ConvertResponsesToChatCompletionsWithOptions(
		body, translate.ResponsesConversionOptions{})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(conv.Body)
	require.NoError(t, err)
	return env
}

func codexAssistantItem(text string) any {
	return map[string]any{
		"type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

func TestExtractForceModelCommand_CodexSkillBlobFollowsDirective(t *testing.T) {
	env := codexEnvelope(t,
		codexUserItem("<environment_context><cwd>/x</cwd></environment_context>"),
		codexUserItem("$fm astra"),
		codexUserItem(codexSkillBlob),
	)
	res, found := env.ExtractForceModelCommand()
	require.True(t, found, "the skill blob must not hide the directive the user typed")
	assert.Equal(t, "astra", res.Model)
	assert.False(t, res.FromToolResult, "a typed directive is not an agent-issued one")
}

func TestExtractForceModelCommand_SkillBlobDoesNotRevivePriorTurn(t *testing.T) {
	// The directive belongs to an older turn that the assistant already answered.
	// Skipping the blob must not reach back past a real conversational turn.
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "$fm astra"},
			map[string]any{"role": "assistant", "content": "pinned"},
			map[string]any{"role": "user", "content": codexSkillBlob},
		},
	})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "a directive from a completed turn must not fire again")
}

func TestExtractForceModelCommand_MessageMentioningSkillTagIsARealTurn(t *testing.T) {
	// Prose that merely mentions the tag is the user's actual message. Skipping
	// it would let the earlier directive fire on this turn.
	env := codexEnvelope(t,
		codexUserItem("$fm astra"),
		codexUserItem("what does <skill> mean in the payload?"),
	)
	_, found := env.ExtractForceModelCommand()
	assert.False(t, found, "a message that only mentions <skill> is a newer turn")
}

func TestExtractForceModelCommand_SkillBlobAloneIsNotACommand(t *testing.T) {
	env := codexEnvelope(t, codexUserItem(codexSkillBlob))
	_, found := env.ExtractForceModelCommand()
	assert.False(t, found)
}

func TestExtractRouterFeedbackCommand_CodexSkillBlobFollowsDirective(t *testing.T) {
	// The same shape has to work for feedback, including the leading verdict
	// token that the skill path used to lose.
	env := codexEnvelope(t,
		codexUserItem("$rf - too slow on this one"),
		codexUserItem(codexSkillBlob),
	)
	res, found := env.ExtractRouterFeedbackCommand()
	require.True(t, found)
	assert.Equal(t, "down", res.Rating, "the leading - is a verdict, not prose")
	assert.Equal(t, "too slow on this one", res.Feedback)
}

// The proxy strips feedback artifacts from history *before* extracting the
// command, so a test that only calls Extract passes while the real path fails.
// That is exactly what happened here: the strip treated the skill blob as the
// trailing user turn, decided the directive belonged to a prior turn, and
// removed it before extraction ran.
func TestStripThenExtract_SkillBlobDoesNotEatTheDirective(t *testing.T) {
	env := codexEnvelope(t,
		codexUserItem("<environment_context><cwd>/x</cwd></environment_context>"),
		codexUserItem("$rf - too slow on this one"),
		codexUserItem(codexSkillBlob),
	)
	env.StripRouterFeedbackArtifacts()
	res, found := env.ExtractRouterFeedbackCommand()
	require.True(t, found, "the strip must not remove the directive it is meant to preserve")
	assert.Equal(t, "down", res.Rating)
	assert.Equal(t, "too slow on this one", res.Feedback)
}

// A genuinely prior feedback turn should still be stripped from history.
func TestStripThenExtract_PriorFeedbackTurnStillStripped(t *testing.T) {
	env := codexEnvelope(t,
		codexUserItem("$rf - an older complaint"),
		codexAssistantItem("Weave Router: Feedback recorded 👎. Thank you."),
		codexUserItem("now do something else"),
	)
	removed := env.StripRouterFeedbackArtifacts()
	assert.Positive(t, removed, "a completed feedback exchange is history, not the current turn")
	_, found := env.ExtractRouterFeedbackCommand()
	assert.False(t, found)
}

// A skill block is an attachment only when a user message precedes it. One that
// follows an assistant turn is a real trailing turn, and skipping it made a
// completed feedback exchange look current: the strip kept the old command and
// removed the ack that ended it, so the extractor recorded the same complaint a
// second time.
func TestStripThenExtract_SkillBlobAfterAnAckDoesNotRevivePriorFeedback(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6-sol",
		"messages": []any{
			map[string]any{"role": "user", "content": "$rf - an older complaint"},
			map[string]any{"role": "assistant", "content": "Weave Router: Feedback recorded 👎. Thank you."},
			map[string]any{"role": "user", "content": codexSkillBlob},
		},
	})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(body)
	require.NoError(t, err)

	removed := env.StripRouterFeedbackArtifacts()
	assert.Equal(t, 2, removed, "the completed exchange is history: both the command and its ack go")
	_, found := env.ExtractRouterFeedbackCommand()
	assert.False(t, found, "a completed feedback turn must not be recorded twice")
}
