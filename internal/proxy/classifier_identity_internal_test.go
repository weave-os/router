package proxy

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

func TestClassifierInstructionIdentityAcrossProtocols(t *testing.T) {
	fixtures := []struct {
		name  string
		parse func([]byte) (*translate.RequestEnvelope, error)
		body  string
	}{
		{"anthropic", translate.ParseAnthropic, `{"system":%q,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`},
		{"chat system", translate.ParseOpenAI, `{"messages":[{"role":"system","content":%q},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`},
		{"chat developer", translate.ParseOpenAI, `{"messages":[{"role":"developer","content":%q},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`},
		{"responses instructions", translate.ParseOpenAI, `{"instructions":%q,"input":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`},
		{"responses developer", translate.ParseOpenAI, `{"input":[{"role":"developer","content":%q},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`},
		{"gemini", translate.ParseGemini, `{"systemInstruction":{"parts":[{"text":%q}]},"contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"next"}]}]}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var previous router.ClassifierContext
			for _, instructions := range []string{"instruction A", "instruction B"} {
				envelope, err := fixture.parse([]byte(fmt.Sprintf(fixture.body, instructions)))
				require.NoError(t, err)
				observation, err := envelope.EscalationObservation()
				require.NoError(t, err)
				input, err := classifierContextForCall(observation)
				require.NoError(t, err)
				retry, err := classifierContextForCall(observation)
				require.NoError(t, err)
				require.Equal(t, input, retry)
				require.Equal(t, "next", input.CurrentUserMessage)
				require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2}, input.Features)
				require.Len(t, input.PrecedingResponses, 1)
				require.Equal(t, "answer", input.PrecedingResponses[0].Content)
				request, err := input.WithHistoricalPredictions(map[string]router.ClassifierComplexity{input.PrecedingResponses[0].PrefixDigest: router.ClassifierLow})
				require.NoError(t, err)
				encodedRequest, err := json.Marshal(request)
				require.NoError(t, err)
				require.NotContains(t, string(encodedRequest), instructions)
				if previous.TurnDigest != "" {
					require.NotEqual(t, previous.TurnDigest, input.TurnDigest)
					require.NotEqual(t, previous.PrecedingResponses[0].PrefixDigest, input.PrecedingResponses[0].PrefixDigest)
					_, err = input.WithHistoricalPredictions(map[string]router.ClassifierComplexity{previous.PrecedingResponses[0].PrefixDigest: router.ClassifierLow})
					require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
				}
				previous = input
			}
		})
	}
}

func TestClassifierInstructionsBeforeNextUserBoundary(t *testing.T) {
	for _, role := range []translate.EscalationRole{translate.EscalationRoleSystem, translate.EscalationRoleDeveloper} {
		t.Run(string(role), func(t *testing.T) {
			observation := classifierTestObservation(
				classifierTestText(translate.EscalationRoleUser, "first"),
				classifierTestText(translate.EscalationRoleAssistant, "answer"),
				classifierTestText(role, "instruction A"),
				classifierTestText(translate.EscalationRoleUser, "next"),
			)
			initial, err := classifierContextForCall(observation)
			require.NoError(t, err)
			observation.Messages[2].Blocks[0].Text = "instruction B"
			changed, err := classifierContextForCall(observation)
			require.NoError(t, err)
			require.NotEqual(t, initial.TurnDigest, changed.TurnDigest)
			require.Equal(t, initial.PrecedingResponses, changed.PrecedingResponses)
		})
	}
}

func TestClassifierRejectsOmittedMediaAcrossProtocols(t *testing.T) {
	fixtures := []struct {
		name  string
		parse func([]byte) (*translate.RequestEnvelope, error)
		body  string
	}{
		{"anthropic image", translate.ParseAnthropic, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]}]}`},
		{"anthropic document", translate.ParseAnthropic, `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":%q}}]}]}`},
		{"chat image", translate.ParseOpenAI, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`},
		{"chat audio", translate.ParseOpenAI, `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":%q,"format":"wav"}}]}]}`},
		{"chat file", translate.ParseOpenAI, `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":%q}}]}]}`},
		{"responses image", translate.ParseOpenAI, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":%q}]}]}`},
		{"responses file", translate.ParseOpenAI, `{"input":[{"role":"user","content":[{"type":"input_file","file_id":%q}]}]}`},
		{"gemini inline", translate.ParseGemini, `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":%q}}]}]}`},
		{"gemini file", translate.ParseGemini, `{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"image/png","fileUri":%q}}]}]}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			for _, media := range []string{"media-A", "media-B"} {
				envelope, err := fixture.parse([]byte(fmt.Sprintf(fixture.body, media)))
				require.NoError(t, err)
				observation, err := envelope.EscalationObservation()
				require.NoError(t, err)
				require.Len(t, observation.Messages, 1)
				require.True(t, observation.Messages[0].HasOmittedMedia)
				_, err = classifierContextForCall(observation)
				require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
				// Adding text must not conceal the same omitted media identity.
				observation.Messages[0].Blocks = append(observation.Messages[0].Blocks, translate.EscalationBlock{Type: translate.EscalationBlockText, Text: "describe it"})
				_, err = classifierContextForCall(observation)
				require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
				observation.Messages = append(observation.Messages, classifierTestText(translate.EscalationRoleUser, "next"))
				_, err = classifierContextForCall(observation)
				require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
			}
		})
	}
}

func TestClassifierExplicitEmptyUserTextIsNotOmittedMedia(t *testing.T) {
	input, err := classifierContextForCall(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "")))
	require.NoError(t, err)
	require.Equal(t, "", input.CurrentUserMessage)
	require.Equal(t, 1, input.Features.UserMessageCount)
}
