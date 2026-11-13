package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CoalesceDuplicateToolResults merges every tool result answering the same
// tool call into the first one, appending later contents in order and
// removing the later results. Codex code-mode records each notify() from an
// exec script as another output under the running call's id; OpenAI accepts
// that, but Anthropic 400s on a tool_use with more than one tool_result, and
// Codex resends the history every turn, so one such call bricks the session.
// A later assistant call that reuses an id starts a new group. Returns the
// number of duplicate results removed; the body is untouched when zero.
// Gemini-format envelopes are left alone.
func (e *RequestEnvelope) CoalesceDuplicateToolResults() int {
	if e == nil {
		return 0
	}
	switch e.format {
	case FormatOpenAI:
		return e.coalesceOpenAIToolResults()
	case FormatAnthropic:
		return e.coalesceAnthropicToolResults()
	default:
		return 0
	}
}

// toolResultKey identifies the call a result answers: its id plus how many
// assistant calls with that id precede it, so a reused id is a distinct call.
type toolResultKey struct {
	id   string
	call int
}

func (e *RequestEnvelope) coalesceOpenAIToolResults() int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	keys := make([]toolResultKey, len(all))
	groups := make(map[toolResultKey][]gjson.Result)
	calls := make(map[string]int)
	duplicated := false
	for i, m := range all {
		switch m.Get("role").String() {
		case "assistant":
			m.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
				if id := tc.Get("id").String(); id != "" {
					calls[id]++
				}
				return true
			})
		case "tool":
			id := m.Get("tool_call_id").String()
			if id == "" {
				continue
			}
			k := toolResultKey{id: id, call: calls[id]}
			keys[i] = k
			groups[k] = append(groups[k], m.Get("content"))
			duplicated = duplicated || len(groups[k]) > 1
		}
	}
	if !duplicated {
		return 0
	}

	out := make([]string, 0, len(all))
	seen := make(map[toolResultKey]struct{}, len(groups))
	removed := 0
	for i, m := range all {
		k := keys[i]
		group := groups[k]
		if k.id == "" || len(group) < 2 {
			out = append(out, m.Raw)
			continue
		}
		if _, ok := seen[k]; ok {
			removed++
			continue
		}
		seen[k] = struct{}{}
		merged, err := sjson.SetRawBytes([]byte(m.Raw), "content", []byte(mergeToolResultContents(group)))
		if err != nil {
			return 0
		}
		out = append(out, string(merged))
	}
	body, err := sjson.SetRawBytes(e.body, "messages", []byte("["+strings.Join(out, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = body
	return removed
}

// anthropicToolResult is one tool_result block; pos is its ordinal among all
// message content blocks.
type anthropicToolResult struct {
	content      gjson.Result
	isError      bool
	cacheControl string
	pos          int
}

// anthropicCacheMarker is a message block carrying a top-level cache_control.
// key is set when that block is a tool_result.
type anthropicCacheMarker struct {
	pos int
	key toolResultKey
}

func (e *RequestEnvelope) coalesceAnthropicToolResults() int {
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	blockKeys := make([][]toolResultKey, len(all))
	groups := make(map[toolResultKey][]anthropicToolResult)
	var markers []anthropicCacheMarker
	calls := make(map[string]int)
	duplicated := false
	pos := 0
	for i, m := range all {
		role := m.Get("role").String()
		content := m.Get("content")
		if !content.IsArray() {
			continue
		}
		content.ForEach(func(_, block gjson.Result) bool {
			blockPos := pos
			pos++
			var k toolResultKey
			switch {
			case role == "assistant" && block.Get("type").String() == "tool_use":
				if id := block.Get("id").String(); id != "" {
					calls[id]++
				}
			case role == "user" && block.Get("type").String() == "tool_result":
				if id := block.Get("tool_use_id").String(); id != "" {
					k = toolResultKey{id: id, call: calls[id]}
					groups[k] = append(groups[k], anthropicToolResult{
						content:      block.Get("content"),
						isError:      block.Get("is_error").Bool(),
						cacheControl: block.Get("cache_control").Raw,
						pos:          blockPos,
					})
					duplicated = duplicated || len(groups[k]) > 1
				}
			}
			blockKeys[i] = append(blockKeys[i], k)
			if block.Get("cache_control").Exists() {
				markers = append(markers, anthropicCacheMarker{pos: blockPos, key: k})
			}
			return true
		})
	}
	if !duplicated {
		return 0
	}

	out := make([]string, 0, len(all))
	seen := make(map[toolResultKey]struct{}, len(groups))
	removed := 0
	for i, m := range all {
		content := m.Get("content")
		if m.Get("role").String() != "user" || !content.IsArray() {
			out = append(out, m.Raw)
			continue
		}
		changed := false
		var kept []string
		var mergeErr error
		blockIdx := 0
		content.ForEach(func(_, block gjson.Result) bool {
			k := blockKeys[i][blockIdx]
			blockIdx++
			group := groups[k]
			if k.id == "" || len(group) < 2 {
				kept = append(kept, block.Raw)
				return true
			}
			changed = true
			if _, ok := seen[k]; ok {
				removed++
				return true
			}
			seen[k] = struct{}{}
			merged, err := mergeAnthropicToolResultBlock(block, k, group, markers)
			if err != nil {
				mergeErr = err
				return false
			}
			kept = append(kept, merged)
			return true
		})
		if mergeErr != nil {
			return 0
		}
		if !changed {
			out = append(out, m.Raw)
			continue
		}
		if len(kept) == 0 {
			continue
		}
		rebuilt, err := sjson.SetRawBytes([]byte(m.Raw), "content", []byte("["+strings.Join(kept, ",")+"]"))
		if err != nil {
			return 0
		}
		out = append(out, string(rebuilt))
	}
	body, err := sjson.SetRawBytes(e.body, "messages", []byte("["+strings.Join(out, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = body
	return removed
}

func mergeAnthropicToolResultBlock(first gjson.Result, k toolResultKey, group []anthropicToolResult, markers []anthropicCacheMarker) (string, error) {
	contents := make([]gjson.Result, 0, len(group))
	isError := false
	for _, r := range group {
		contents = append(contents, r.content)
		isError = isError || r.isError
	}
	out, err := sjson.SetRawBytes([]byte(first.Raw), "content", []byte(mergeToolResultContents(contents)))
	if err != nil {
		return "", err
	}
	if isError {
		if out, err = sjson.SetBytes(out, "is_error", true); err != nil {
			return "", err
		}
	}
	if group[0].cacheControl != "" {
		return string(out), nil
	}
	for _, r := range group[1:] {
		if r.cacheControl == "" {
			continue
		}
		// Anthropic requires 1h breakpoints before 5m ones, so a marker may only
		// move up to the survivor when no other breakpoint sits in between.
		if cacheMarkerBetween(markers, k, group[0].pos, r.pos) {
			break
		}
		if out, err = sjson.SetRawBytes(out, "cache_control", []byte(r.cacheControl)); err != nil {
			return "", err
		}
		break
	}
	return string(out), nil
}

func cacheMarkerBetween(markers []anthropicCacheMarker, k toolResultKey, from, to int) bool {
	for _, m := range markers {
		if m.pos > from && m.pos < to && m.key != k {
			return true
		}
	}
	return false
}

func textPart(text string) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("text")
	jw.Key("text")
	jw.Str(text)
	jw.EndObj()
	return string(jw.Bytes())
}

// mergeToolResultContents returns the raw JSON for the concatenation of
// contents: a "\n\n"-joined string when every content is a string, otherwise
// a part array with strings promoted to text parts. Empty and missing
// contents are skipped.
func mergeToolResultContents(contents []gjson.Result) string {
	allStrings := true
	for _, c := range contents {
		if c.IsArray() {
			allStrings = false
			break
		}
	}
	if allStrings {
		texts := make([]string, 0, len(contents))
		for _, c := range contents {
			if c.Type == gjson.String && c.Str != "" {
				texts = append(texts, c.Str)
			}
		}
		jw := newJSONWriter()
		jw.Str(strings.Join(texts, "\n\n"))
		return string(jw.Bytes())
	}
	var parts []string
	for _, c := range contents {
		switch {
		case c.Type == gjson.String && c.Str != "":
			parts = append(parts, textPart(c.Str))
		case c.IsArray():
			c.ForEach(func(_, p gjson.Result) bool {
				parts = append(parts, p.Raw)
				return true
			})
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}
