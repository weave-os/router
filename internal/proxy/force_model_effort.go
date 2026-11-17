package proxy

import "strings"

func cutSpelledEffort(name string) (head, level string, ok bool) {
	folded := foldModelSpelling(name)
	for _, spelled := range []string{"-extra-high", "-x-high"} {
		if head, ok := strings.CutSuffix(strings.ToLower(folded), spelled); ok && head != "" {
			return head, "xhigh", true
		}
	}
	idx := strings.LastIndexByte(folded, '-')
	if idx <= 0 || !looksLikeEffortAlias(folded[idx+1:]) {
		return "", "", false
	}
	return folded[:idx], folded[idx+1:], true
}

func looksLikeEffortAlias(tail string) bool {
	switch strings.ToLower(strings.TrimSpace(tail)) {
	case "fast", "low", "medium", "med", "high", "max", "xhigh",
		"ultra", "minimal", "min":
		return true
	default:
		return false
	}
}
