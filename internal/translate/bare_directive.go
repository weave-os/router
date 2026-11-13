package translate

import "strings"

// parseBareDirective matches one of tokens, alone on the first non-empty line
// of text, and returns the text with that line removed.
//
// Shared by every directive whose only form is the bare token (router-models,
// router-session). They had a copy each, and the copies were the hazard
// rather than the duplication itself: the same two defects had to be fixed
// twice, once per file, and a third fix reaching only one of them would have
// left the directives silently inconsistent.
//
// Two rules, both load-bearing:
//
// Leading line only, so a pasted transcript that happens to contain a
// directive cannot fire one.
//
// Nothing but the directive may remain. These callers short-circuit the turn,
// so anything left over reaches no model at all -- "$router-models\nenable
// gpt-5.5" would answer with a bare listing and drop the mutation, and
// "$router-session\nand what has this cost?" would drop the question. Only a
// client's own wrapper blocks may remain, via isOnlyKnownInjectedText;
// arbitrary tagged text is the user's, not synthetic.
func parseBareDirective(text string, tokens []string) (found bool, stripped string) {
	prefixEnd := leadingInjectedPrefixEnd(text)
	prefix := text[:prefixEnd]
	body := text[prefixEnd:]

	lines := strings.Split(body, "\n")
	cmdIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, tok := range tokens {
			if trimmed == tok {
				cmdIdx = i
				break
			}
		}
		break
	}
	if cmdIdx < 0 {
		return false, text
	}

	remaining := make([]string, 0, len(lines)-1)
	remaining = append(remaining, lines[:cmdIdx]...)
	remaining = append(remaining, lines[cmdIdx+1:]...)
	stripped = strings.TrimSpace(prefix + strings.Join(remaining, "\n"))
	if !isOnlyKnownInjectedText(stripped) {
		return false, text
	}
	return true, stripped
}
