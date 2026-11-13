package translate

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClampResponsesInputCallIDs rewrites every `input[*].call_id` on a native
// Responses request through clampOpenAIToolCallID. A history whose tool calls
// were served cross-format carries ids the router minted with a smuggled
// Gemini thought signature, well past OpenAI's 64-char limit; a native
// passthrough forwards the caller's bytes verbatim, so the clamp has to run
// here. Deterministic, so a function_call and its function_call_output keep
// matching.
func ClampResponsesInputCallIDs(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	out := body
	for i, item := range input.Array() {
		callID := item.Get("call_id")
		if callID.Type != gjson.String || callID.Str == "" {
			continue
		}
		clamped := clampOpenAIToolCallID(callID.Str)
		if clamped == callID.Str {
			continue
		}
		var err error
		out, err = sjson.SetBytes(out, fmt.Sprintf("input.%d.call_id", i), clamped)
		if err != nil {
			return nil, fmt.Errorf("clamp Responses call_id at input[%d]: %w", i, err)
		}
	}
	return out, nil
}
