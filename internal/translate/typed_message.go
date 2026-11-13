package translate

import (
	"regexp"
	"strings"
)

const (
	codexPastedFilesHeader  = "# Files pasted by the user:"
	codexPastedFilesRequest = "\n## My request:"
)

var codexSkillMention = regexp.MustCompile(`^\[\$([a-z][a-z0-9-]*)\]\([^)]*/skills/([a-z][a-z0-9-]*)/SKILL\.md\)`)

// typedMessageInWrapper returns the message the user typed, split from the text
// a client wrapped around it: Cursor's <user_query>, or the header Codex
// prepends when the user pastes a file ("# Files pasted by the user: … ## My
// request: <typed>"). A Codex-encoded directive is decoded to its plain form.
func typedMessageInWrapper(text string) (before, query, after string, ok bool) {
	before, query, after, ok = clientWrappedMessage(text)
	if !ok {
		before, query, after = "", text, ""
	}
	if directive, decoded := codexTypedDirective(query); decoded {
		return before, directive, after, true
	}
	return before, query, after, ok
}

func clientWrappedMessage(text string) (before, query, after string, ok bool) {
	if before, query, after, ok := cursorUserQuery(text); ok {
		return before, query, after, true
	}
	if !strings.HasPrefix(strings.TrimSpace(text), codexPastedFilesHeader) {
		return "", "", "", false
	}
	idx := strings.Index(text, codexPastedFilesRequest)
	if idx < 0 {
		return "", "", "", false
	}
	start := idx + len(codexPastedFilesRequest)
	return text[:start], text[start:], "", true
}

// codexTypedDirective decodes how the Codex app writes a typed directive: a
// picked skill becomes a link to its SKILL.md ("[$fm](…/skills/fm/SKILL.md)
// astra"), and a directive the user quoted keeps its code span.
func codexTypedDirective(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if inner, ok := strings.CutPrefix(trimmed, "`"); ok {
		inner, ok = strings.CutSuffix(inner, "`")
		if ok && strings.HasPrefix(inner, "$") && !strings.Contains(inner, "`") {
			return inner, true
		}
	}
	m := codexSkillMention.FindStringSubmatchIndex(trimmed)
	if m == nil || trimmed[m[2]:m[3]] != trimmed[m[4]:m[5]] {
		return text, false
	}
	return "$" + trimmed[m[2]:m[3]] + trimmed[m[1]:], true
}
