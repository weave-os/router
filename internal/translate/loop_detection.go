package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

type ToolCallSig struct {
	Name      string
	InputHash string
}

func (e *RequestEnvelope) AssistantToolCallArgsPreview(offset, maxLen int) []string {
	switch e.format {
	case FormatAnthropic:
		return assistantToolCallArgsPreview(anthropicAssistantToolCallEntries(e.body), offset, maxLen)
	case FormatOpenAI:
		return assistantToolCallArgsPreview(openAIAssistantToolCallEntries(e.body), offset, maxLen)
	default:
		return nil
	}
}

func assistantToolCallArgsPreview(entries []assistantToolCallEntry, offset, maxLen int) []string {
	var out []string
	for i, entry := range entries {
		if i < offset {
			continue
		}
		preview := entry.InputRaw
		if len(preview) > maxLen {
			preview = preview[:maxLen] + "…"
		}
		out = append(out, entry.Name+":"+preview)
	}
	return out
}

func (e *RequestEnvelope) AssistantToolCallSignatures() []ToolCallSig {
	switch e.format {
	case FormatAnthropic:
		return anthropicAssistantToolCallSigs(e.body)
	case FormatOpenAI:
		return openAIAssistantToolCallSigs(e.body)
	default:
		return nil
	}
}

type assistantToolCallEntry struct {
	Name     string
	InputRaw string
}

func anthropicAssistantToolCallEntries(body []byte) []assistantToolCallEntry {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	var entries []assistantToolCallEntry
	msgs.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role == "user" && userPromptTextGJSON(msg.Get("content")) != "" {
			entries = nil
			return true
		}
		if role != "assistant" {
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
			input := block.Get("input")
			if !isMeaningfulInput(input) {
				return true
			}
			entries = append(entries, assistantToolCallEntry{Name: name, InputRaw: input.Raw})
			return true
		})
		return true
	})
	return entries
}

func openAIAssistantToolCallEntriesFromView(body []byte) []assistantToolCallEntry {
	msgs := viewJSONPath(body, "messages")
	if !msgs.IsArray() {
		return nil
	}
	var entries []assistantToolCallEntry
	msgs.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role == "user" && userPromptTextGJSON(msg.Get("content")) != "" {
			entries = nil
			return true
		}
		if role != "assistant" {
			return true
		}
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.IsArray() {
			return true
		}
		toolCalls.ForEach(func(_, tc gjson.Result) bool {
			if tc.Get("type").String() != "function" {
				return true
			}
			name := tc.Get("function.name").String()
			if name == "" {
				return true
			}
			argsRaw := tc.Get("function.arguments").String()
			if !isMeaningfulInputRaw(argsRaw) {
				return true
			}
			entries = append(entries, assistantToolCallEntry{Name: name, InputRaw: argsRaw})
			return true
		})
		return true
	})
	return entries
}

func anthropicAssistantToolCallSigs(body []byte) []ToolCallSig {
	return sigsFromEntries(anthropicAssistantToolCallEntries(body))
}

func openAIAssistantToolCallSigs(body []byte) []ToolCallSig {
	return sigsFromEntries(openAIAssistantToolCallEntries(body))
}

func sigsFromEntries(entries []assistantToolCallEntry) []ToolCallSig {
	if entries == nil {
		return nil
	}
	sigs := make([]ToolCallSig, len(entries))
	for i, entry := range entries {
		sigs[i] = ToolCallSig{Name: entry.Name, InputHash: hashCanonicalJSON(entry.InputRaw)}
	}
	return sigs
}

func isMeaningfulInput(r gjson.Result) bool {
	if !r.Exists() {
		return false
	}
	if r.IsObject() {
		empty := true
		r.ForEach(func(_, _ gjson.Result) bool {
			empty = false
			return false
		})
		return !empty
	}
	return isMeaningfulInputRaw(r.Raw)
}

func isMeaningfulInputRaw(raw string) bool {
	switch raw {
	case "", "{}", "null":
		return false
	}
	parsed := gjson.Parse(raw)
	if !parsed.IsObject() {
		return raw != ""
	}
	empty := true
	parsed.ForEach(func(_, _ gjson.Result) bool {
		empty = false
		return false
	})
	return !empty
}

func hashCanonicalJSON(raw string) string {
	if raw == "" {
		h := sha256.Sum256(nil)
		return hex.EncodeToString(h[:])
	}
	parsed := gjson.Parse(raw)
	if !parsed.Exists() {
		h := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(h[:])
	}
	canonical := canonicalize(parsed)
	h := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(h[:])
}

func canonicalize(r gjson.Result) string {
	var b strings.Builder
	canonicalizeInto(&b, r)
	return b.String()
}

func canonicalizeInto(b *strings.Builder, r gjson.Result) {
	switch {
	case r.IsObject():
		keys := make([]string, 0, 8)
		fields := make(map[string]gjson.Result, 8)
		r.ForEach(func(k, v gjson.Result) bool {
			ks := k.String()
			keys = append(keys, ks)
			fields[ks] = v
			return true
		})
		sortStrings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(jsonQuote(k))
			b.WriteByte(':')
			canonicalizeInto(b, fields[k])
		}
		b.WriteByte('}')
	case r.IsArray():
		b.WriteByte('[')
		first := true
		r.ForEach(func(_, v gjson.Result) bool {
			if !first {
				b.WriteByte(',')
			}
			first = false
			canonicalizeInto(b, v)
			return true
		})
		b.WriteByte(']')
	default:
		b.WriteString(r.Raw)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func jsonQuote(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 0x20 {
				out = append(out, '\\', 'u', '0', '0',
					hexDigit(byte(r>>4)), hexDigit(byte(r&0xf)))
			} else {
				out = append(out, []byte(string(r))...)
			}
		}
	}
	out = append(out, '"')
	return string(out)
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + n - 10
}
