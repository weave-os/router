package translate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeCodeReadToolName is Claude Code's file-read tool. Its results number
// every line as "N<TAB>content", and in tab-indented code that separator tab
// is indistinguishable from indentation.
const claudeCodeReadToolName = "Read"

// nonAnthropicReadLineSeparator replaces the separator tab for non-Anthropic
// targets. Models not trained on Claude Code's format copy that tab into
// Edit's old_string, adding one tab to every line, so the edit misses (dogfood
// transcripts: 43% of grok-4.6 and 18% of gpt-5.6-luna edits failed, versus
// 1.4% for Claude). Claude Code itself used this arrow before switching to a tab.
const nonAnthropicReadLineSeparator = "→"

var readLinePrefix = regexp.MustCompile(`(?m)^( *\d+)\t`)

// readPrefixDescriptionReplacer keeps Edit's own instructions consistent with
// the rewritten prefix. Best effort: wording varies across Claude Code releases.
var readPrefixDescriptionReplacer = strings.NewReplacer(
	"line number + tab", "line number + "+nonAnthropicReadLineSeparator,
	"after that tab", "after that "+nonAnthropicReadLineSeparator,
)

// withUnambiguousReadPrefixes returns e with Claude Code Read results'
// line-number separators rewritten for a non-Anthropic target. Anthropic
// targets keep the exact format their models are trained on. Deterministic, so
// the rewritten prefix is stable across turns for prompt caching.
func (e *RequestEnvelope) withUnambiguousReadPrefixes() (*RequestEnvelope, error) {
	if e.format != FormatAnthropic {
		return e, nil
	}
	readIDs := map[string]struct{}{}
	gjson.GetBytes(e.body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" && block.Get("name").String() == claudeCodeReadToolName {
				readIDs[block.Get("id").String()] = struct{}{}
			}
			return true
		})
		return true
	})
	if len(readIDs) == 0 {
		return e, nil
	}
	needsRewrite := func(block gjson.Result) bool {
		if block.Get("type").String() != "tool_result" {
			return false
		}
		if _, read := readIDs[block.Get("tool_use_id").String()]; !read {
			return false
		}
		content := block.Get("content")
		if content.Type == gjson.String {
			return readLinePrefix.MatchString(content.String())
		}
		matched := false
		content.ForEach(func(_, part gjson.Result) bool {
			matched = part.Get("type").String() == "text" && readLinePrefix.MatchString(part.Get("text").String())
			return !matched
		})
		return matched
	}
	body, err := rewriteMessageBlocks(e.body, needsRewrite, rewriteReadResultPrefixes)
	if err != nil {
		return nil, err
	}
	body, err = rewriteEditToolDescriptions(body)
	if err != nil {
		return nil, err
	}
	return &RequestEnvelope{body: body, format: e.format}, nil
}

func rewriteReadResultPrefixes(blockRaw string) (string, error) {
	separator := "${1}" + nonAnthropicReadLineSeparator
	content := gjson.Get(blockRaw, "content")
	if content.Type == gjson.String {
		return sjson.Set(blockRaw, "content", readLinePrefix.ReplaceAllString(content.String(), separator))
	}
	out := blockRaw
	var err error
	content.ForEach(func(index, part gjson.Result) bool {
		if part.Get("type").String() != "text" {
			return true
		}
		out, err = sjson.Set(out, fmt.Sprintf("content.%d.text", index.Int()), readLinePrefix.ReplaceAllString(part.Get("text").String(), separator))
		return err == nil
	})
	return out, err
}

func rewriteEditToolDescriptions(body []byte) ([]byte, error) {
	var err error
	gjson.GetBytes(body, "tools").ForEach(func(index, tool gjson.Result) bool {
		name := tool.Get("name").String()
		if name != "Edit" && name != "MultiEdit" {
			return true
		}
		description := tool.Get("description").String()
		if rewritten := readPrefixDescriptionReplacer.Replace(description); rewritten != description {
			body, err = sjson.SetBytes(body, fmt.Sprintf("tools.%d.description", index.Int()), rewritten)
		}
		return err == nil
	})
	return body, err
}
