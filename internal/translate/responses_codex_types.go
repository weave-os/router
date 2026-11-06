package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrCustomToolArguments marks model tool-call arguments that cannot be
// unwrapped into a Codex custom tool input.
var ErrCustomToolArguments = errors.New("decode custom tool arguments")

// ResponsesToolMapping describes one Responses tool after it has been
// projected into a flat Chat Completions function. Alias is the synthetic Chat
// function name. Name and Namespace are restored on the way back to Codex;
// Custom marks a function projected from a Responses custom tool.
type ResponsesToolMapping struct {
	Alias     string
	Name      string
	Namespace string
	Custom    bool
}

func customToolInput(arguments string) (string, error) {
	if !json.Valid([]byte(arguments)) {
		return arguments, nil
	}
	trimmed := strings.TrimSpace(arguments)
	if !strings.HasPrefix(trimmed, "{") {
		if strings.HasPrefix(trimmed, `"`) {
			var input string
			if err := json.Unmarshal([]byte(arguments), &input); err == nil {
				return input, nil
			}
		}
		return arguments, nil
	}

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
