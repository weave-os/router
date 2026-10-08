package translate

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	anthropicRequestToolNamePaths = []string{"tools.#.name", "tool_choice.name", "messages.#.content.#.name"}
	openAIRequestToolNamePaths    = []string{"tools.#.function.name", "tool_choice.function.name", "messages.#.tool_calls.#.function.name"}
)

func openAIToolNameAliases(body []byte, paths []string) map[string]string {
	aliases := make(map[string]string)
	var add func(gjson.Result)
	add = func(value gjson.Result) {
		if value.IsArray() {
			for _, item := range value.Array() {
				add(item)
			}
			return
		}
		if value.Type != gjson.String {
			return
		}
		if alias := sanitizeResponsesToolAlias(value.Str); alias != value.Str {
			aliases[alias] = value.Str
		}
	}
	for _, path := range paths {
		add(gjson.GetBytes(body, path))
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func restoreOpenAIResponseToolNames(body []byte, names map[string]string) ([]byte, error) {
	var paths []string
	collect := func(path string) {
		if _, ok := names[gjson.GetBytes(body, path).String()]; ok {
			paths = append(paths, path)
		}
	}
	gjson.GetBytes(body, "choices").ForEach(func(choiceIndex, choice gjson.Result) bool {
		for _, field := range []string{"delta", "message"} {
			choice.Get(field + ".tool_calls").ForEach(func(callIndex, _ gjson.Result) bool {
				collect(fmt.Sprintf("choices.%d.%s.tool_calls.%d.function.name", choiceIndex.Int(), field, callIndex.Int()))
				return true
			})
		}
		return true
	})
	collect("item.name")
	for _, output := range []string{"output", "response.output"} {
		gjson.GetBytes(body, output).ForEach(func(outputIndex, _ gjson.Result) bool {
			collect(fmt.Sprintf("%s.%d.name", output, outputIndex.Int()))
			return true
		})
	}
	var err error
	for _, path := range paths {
		body, err = sjson.SetBytes(body, path, names[gjson.GetBytes(body, path).String()])
		if err != nil {
			return nil, fmt.Errorf("restore OpenAI tool name: %w", err)
		}
	}
	return body, nil
}
