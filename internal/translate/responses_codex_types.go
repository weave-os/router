package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

var ErrCustomToolArguments = errors.New("decode custom tool arguments")

var ErrToolCallTruncated = errors.New("tool call cut off by the output token limit")

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
		return "", fmt.Errorf("%w: expected exactly one input field, got keys %v", ErrCustomToolArguments, argumentKeyNames(wrapper))
	}
	raw, ok := wrapper["input"]
	if !ok {
		return "", fmt.Errorf("%w: required string field input is missing, got keys %v", ErrCustomToolArguments, argumentKeyNames(wrapper))
	}
	var input string
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("%w: input must be a string: %w", ErrCustomToolArguments, err)
	}
	return input, nil
}

var argumentKeyName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)

func argumentKeyNames(wrapper map[string]json.RawMessage) []string {
	names := make([]string, 0, len(wrapper))
	for _, key := range slices.Sorted(maps.Keys(wrapper)) {
		if !argumentKeyName.MatchString(key) {
			key = "<redacted>"
		}
		names = append(names, key)
	}
	return names
}

func truncationAwareCustomToolInput(arguments, finishReason string) (string, error) {
	input, err := customToolInput(arguments)
	if err != nil && finishReason == "length" {
		return "", fmt.Errorf("%w: %w", ErrToolCallTruncated, err)
	}
	return input, err
}
