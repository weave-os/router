package translate

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AppendClientCompactionInstruction adds guidance only to the summarization call,
// whose output replaces history. It never rewrites an ordinary inference prefix.
func (e *RequestEnvelope) AppendClientCompactionInstruction(instruction string) error {
	messages := gjson.GetBytes(e.body, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return fmt.Errorf("client compaction requires conversation messages")
	}
	// The client may end with a system instruction rather than a user message.
	// Append instead of altering the retained history or its system instructions.
	updated, err := sjson.SetBytes(e.body, "messages.-1", map[string]string{"role": "user", "content": instruction})
	if err != nil {
		return fmt.Errorf("append client compaction instruction: %w", err)
	}
	e.body = updated
	return nil
}

// DisableParallelToolUse bounds the next retained batch on Anthropic/OpenAI
// targets. Gemini has no equivalent switch; continuation guidance remains advisory there.
func (e *RequestEnvelope) DisableParallelToolUse() (bool, error) {
	if !e.HasTools() {
		return false, nil
	}
	kind, _ := anthropicToolChoice(e.body)
	if kind == toolChoiceNone || kind == toolChoiceUnrecognized {
		return false, nil
	}
	if gjson.GetBytes(e.body, "tool_choice.disable_parallel_tool_use").Bool() {
		return false, nil
	}
	updated := e.body
	var err error
	if kind == toolChoiceAbsent {
		updated, err = sjson.SetBytes(updated, "tool_choice.type", "auto")
		if err != nil {
			return false, fmt.Errorf("set recovery tool choice: %w", err)
		}
	}
	updated, err = sjson.SetBytes(updated, "tool_choice.disable_parallel_tool_use", true)
	if err != nil {
		return false, fmt.Errorf("disable parallel tool use: %w", err)
	}
	e.body = updated
	return true, nil
}

func writeOpenAIParallelToolCallsFromAnthropic(jw *jsonWriter, body []byte) {
	if disabled := gjson.GetBytes(body, "tool_choice.disable_parallel_tool_use"); isJSONBool(disabled) && hasNonEmptyTools(body) {
		jw.Key("parallel_tool_calls")
		jw.Bool(!disabled.Bool())
	}
}
