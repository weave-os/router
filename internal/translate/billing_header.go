package translate

import (
	"regexp"
	"strings"
)

var anthropicBillingHeaderPrefix = regexp.MustCompile(`\Ax-anthropic-billing-header:[^\n]*\n?`)

func stripAnthropicBillingHeader(s string) string {
	return anthropicBillingHeaderPrefix.ReplaceAllString(s, "")
}

func (e *RequestEnvelope) AnthropicBillingHeader() string {
	for _, block := range e.SystemBlocks() {
		if line := anthropicBillingHeaderPrefix.FindString(block); line != "" {
			return strings.TrimSuffix(line, "\n")
		}
	}
	return ""
}
