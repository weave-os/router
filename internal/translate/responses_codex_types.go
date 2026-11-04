package translate

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrCustomToolArguments marks model tool-call arguments that cannot be
// unwrapped into a Codex custom tool input.
var ErrCustomToolArguments = errors.New("decode custom tool arguments")

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
		return "", fmt.Errorf("%w: %w", ErrCustomToolArguments, err)
	}
	if len(wrapper) != 1 {
		return "", fmt.Errorf("%w: expected exactly one input field", ErrCustomToolArguments)
	}
	raw, ok := wrapper["input"]
	if !ok {
		return "", fmt.Errorf("%w: required string field input is missing", ErrCustomToolArguments)
	}
	var input string
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("%w: input must be a string: %w", ErrCustomToolArguments, err)
	}
	return input, nil
}
