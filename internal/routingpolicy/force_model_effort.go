package routingpolicy

import (
	"regexp"
	"strings"
)

func CutSpelledEffort(name string) (head, level string, ok bool) {
	folded := FoldModelSpelling(name)
	for _, spelled := range []string{"-extra-high", "-x-high"} {
		if head, ok := strings.CutSuffix(strings.ToLower(folded), spelled); ok && head != "" {
			return head, "xhigh", true
		}
	}
	idx := strings.LastIndexByte(folded, '-')
	if idx <= 0 || !LooksLikeEffortAlias(folded[idx+1:]) {
		return "", "", false
	}
	return folded[:idx], folded[idx+1:], true
}

var modelSpellingSeparators = regexp.MustCompile(`[._ -]+`)

func FoldModelSpelling(name string) string {
	return modelSpellingSeparators.ReplaceAllString(name, "-")
}
