package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

const toolResultErrorScanLimit = 2048

var toolResultErrorMarkers = []string{
	"Traceback (most recent call last)",
	"FAILED",
	"Error:",
	"error:",
}

type ToolResultErrorStats struct {
	Total             int
	Errored           int
	TrailingErrStreak int
}

func (e *RequestEnvelope) ToolResultErrors() ToolResultErrorStats {
	if e.format != FormatAnthropic {
		return ToolResultErrorStats{}
	}
	var stats ToolResultErrorStats
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return stats
	}
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "tool_result" {
				return true
			}
			stats.Total++
			if toolResultIsErrored(block) {
				stats.Errored++
				stats.TrailingErrStreak++
			} else {
				stats.TrailingErrStreak = 0
			}
			return true
		})
		return true
	})
	return stats
}

func toolResultIsErrored(block gjson.Result) bool {
	if block.Get("is_error").Bool() {
		return true
	}
	content := block.Get("content")
	var text string
	switch {
	case content.Type == gjson.String:
		text = content.String()
	case content.IsArray():
		var b strings.Builder
		content.ForEach(func(_, inner gjson.Result) bool {
			if inner.Get("type").String() == "text" {
				b.WriteString(inner.Get("text").String())
			}
			return b.Len() < toolResultErrorScanLimit
		})
		text = b.String()
	default:
		return false
	}
	if len(text) > toolResultErrorScanLimit {
		text = text[:toolResultErrorScanLimit]
	}
	for _, marker := range toolResultErrorMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type ToolCallOutcome struct {
	Name     string
	Resolved bool
	Errored  bool
}

func (e *RequestEnvelope) AssistantToolCallOutcomes() []ToolCallOutcome {
	if e.format != FormatAnthropic {
		return nil
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	errored := toolResultOutcomesByID(msgs)
	var out []ToolCallOutcome
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "tool_use" {
				return true
			}
			name := block.Get("name").String()
			if name == "" {
				return true
			}
			isErr, resolved := errored[block.Get("id").String()]
			out = append(out, ToolCallOutcome{Name: name, Resolved: resolved, Errored: isErr})
			return true
		})
		return true
	})
	return out
}

func toolResultOutcomesByID(msgs gjson.Result) map[string]bool {
	out := make(map[string]bool)
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "tool_result" {
				return true
			}
			id := block.Get("tool_use_id").String()
			if id == "" {
				return true
			}
			out[id] = toolResultIsErrored(block)
			return true
		})
		return true
	})
	return out
}

type ToolCallFilePath struct {
	Name string
	Path string
}

func (e *RequestEnvelope) AssistantToolCallFilePaths() []ToolCallFilePath {
	if e.format != FormatAnthropic {
		return nil
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	var out []ToolCallFilePath
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "tool_use" {
				return true
			}
			name := block.Get("name").String()
			if name == "" {
				return true
			}
			path := block.Get("input.file_path").String()
			if path == "" {
				path = block.Get("input.notebook_path").String()
			}
			if path == "" {
				return true
			}
			out = append(out, ToolCallFilePath{Name: name, Path: path})
			return true
		})
		return true
	})
	return out
}

func (e *RequestEnvelope) TrailingAssistantMonologue() int {
	if e.format != FormatAnthropic {
		return 0
	}
	msgs := gjson.GetBytes(e.body, "messages")
	if !msgs.IsArray() {
		return 0
	}
	all := msgs.Array()
	streak := 0
	for i := len(all) - 1; i >= 0; i-- {
		msg := all[i]
		switch msg.Get("role").String() {
		case "assistant":
			if assistantHasRealToolUse(msg) {
				return streak
			}
			streak++
		case "user":
			if userHasNonToolResultContent(msg) {
				return streak
			}
		}
	}
	return streak
}

func assistantHasRealToolUse(msg gjson.Result) bool {
	content := msg.Get("content")
	if !content.IsArray() {
		return false
	}
	has := false
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "tool_use" {
			return true
		}
		has = true
		return false
	})
	return has
}

func userHasNonToolResultContent(msg gjson.Result) bool {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return content.String() != ""
	}
	if !content.IsArray() {
		return false
	}
	has := false
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "tool_result" {
			return true
		}
		has = true
		return false
	})
	return has
}
