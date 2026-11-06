package translate

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var commandReferencePattern = regexp.MustCompile(`(?m)^\[weave-router-command-v[0-9]+\]: <weave-router:[A-Za-z0-9_-]+>\r?$`)

func stripCommandReference(text string) string {
	location := commandReferencePattern.FindStringIndex(text)
	if location == nil {
		return text
	}
	reference := strings.TrimSpace(text[location[0]:location[1]])
	encoded := strings.TrimSuffix(strings.SplitN(reference, "weave-router:", 2)[1], ">")
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	var outcome CommandOutcome
	if err == nil && json.Unmarshal(raw, &outcome) == nil && outcome.Version == 1 && outcome.ID != "" && outcome.Command != "" {
		// A control reply occupies the text before its reference definition. Any
		// later ordinary content stays in place, as do adjacent tool/reasoning items.
		return strings.TrimLeft(text[location[1]:], "\r\n")
	}
	return commandReferencePattern.ReplaceAllString(text, "")
}

// StripCommandOutcomes removes harness-only evidence from replayed assistant
// messages. It does not interpret that evidence as a new command application.
func (env *RequestEnvelope) StripCommandOutcomes() error {
	body, err := stripCommandOutcomes(env.body, "messages")
	if err != nil {
		return err
	}
	env.body = body
	return nil
}

func StripCommandOutcomesFromResponsesInput(body []byte) ([]byte, error) {
	return stripCommandOutcomes(body, "input")
}

func stripCommandOutcomes(body []byte, collection string) ([]byte, error) {
	items := gjson.GetBytes(body, collection)
	if !items.IsArray() {
		return body, nil
	}
	rebuilt := make([]string, 0, len(items.Array()))
	changed := false
	for _, item := range items.Array() {
		if item.Get("role").String() != "assistant" {
			rebuilt = append(rebuilt, item.Raw)
			continue
		}
		raw := []byte(item.Raw)
		control := item.Get("router_command").Exists() && item.Get("model").String() == "weave-router"
		var err error
		if item.Get("router_command").Exists() {
			raw, err = sjson.DeleteBytes(raw, "router_command")
			if err != nil {
				return nil, err
			}
		}
		content := item.Get("content")
		hasBody := content.Exists()
		if content.Type == gjson.String {
			stripped := stripCommandReference(content.String())
			if control {
				stripped = ""
			}
			hasBody = stripped != ""
			if stripped != content.String() {
				raw, err = sjson.SetBytes(raw, "content", stripped)
			}
		} else if content.IsArray() {
			blocks := make([]string, 0, len(content.Array()))
			blocksChanged := false
			for _, part := range content.Array() {
				switch part.Get("type").String() {
				case "text", "output_text", "input_text":
					text := part.Get("text").String()
					stripped := stripCommandReference(text)
					if control {
						stripped = ""
					}
					if stripped != text {
						blocksChanged = true
						if stripped == "" {
							continue
						}
						block, replaceErr := sjson.SetBytes([]byte(part.Raw), "text", stripped)
						if replaceErr != nil {
							return nil, replaceErr
						}
						blocks = append(blocks, string(block))
						continue
					}
				}
				blocks = append(blocks, part.Raw)
			}
			hasBody = len(blocks) > 0
			if blocksChanged {
				raw, err = sjson.SetRawBytes(raw, "content", []byte("["+strings.Join(blocks, ",")+"]"))
			}
		}
		if err != nil {
			return nil, err
		}
		if string(raw) != item.Raw {
			changed = true
			if !hasBody && len(item.Get("tool_calls").Array()) == 0 && !item.Get("reasoning_content").Exists() && !item.Get("reasoning").Exists() {
				continue
			}
		}
		rebuilt = append(rebuilt, string(raw))
	}
	if !changed {
		return body, nil
	}
	return sjson.SetRawBytes(body, collection, []byte("["+strings.Join(rebuilt, ",")+"]"))
}
