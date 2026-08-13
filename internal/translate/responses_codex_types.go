package translate

import (
	"encoding/json"
	"fmt"
)

// ResponsesToolMapping describes one Responses tool after it has been
// projected into a flat Chat Completions function. Alias is the synthetic Chat
// function name. Name and Namespace are restored on the way back to Codex;
// Custom means the function's {"input":"..."} arguments must be emitted as a
// raw Responses custom_tool_call rather than a function_call.
type ResponsesToolMapping struct {
	Alias     string
	Name      string
	Namespace string
	Custom    bool
}

func customToolInput(arguments string) (string, error) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &wrapper); err != nil {
		return "", fmt.Errorf("decode custom tool arguments: %w", err)
	}
	if len(wrapper) != 1 {
		return "", fmt.Errorf("decode custom tool arguments: expected exactly one input field")
	}
	raw, ok := wrapper["input"]
	if !ok {
		return "", fmt.Errorf("decode custom tool arguments: required string field input is missing")
	}
	var input string
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("decode custom tool arguments: input must be a string: %w", err)
	}
	return input, nil
}
