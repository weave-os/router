package translate

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ClampResponsesInputCallIDs(body []byte) ([]byte, error) {
	input := viewJSONPath(body, "input")
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
