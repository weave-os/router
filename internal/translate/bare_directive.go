package translate

import "strings"

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
