package translate

import (
	"regexp"
	"strings"
)

// anthropicBillingHeaderPrefix matches the Claude Code billing-header line that
// breaks prompt-prefix caching on non-Anthropic upstreams.
var anthropicBillingHeaderPrefix = regexp.MustCompile(`\Ax-anthropic-billing-header:[^\n]*\n?`)

func stripAnthropicBillingHeader(s string) string {
	return anthropicBillingHeaderPrefix.ReplaceAllString(s, "")
}

// AnthropicBillingHeader returns the Claude Code billing-header line
// ("x-anthropic-billing-header: cc_version=...; cc_is_subagent=true; ...")
// that opens one of the request's system blocks, or "" when absent. Only a
// line at the very start of a block counts, so the same text quoted inside a
// prompt body never matches.
func (e *RequestEnvelope) AnthropicBillingHeader() string {
	for _, block := range e.SystemBlocks() {
		if line := anthropicBillingHeaderPrefix.FindString(block); line != "" {
			return strings.TrimSuffix(line, "\n")
		}
	}
	return ""
}
