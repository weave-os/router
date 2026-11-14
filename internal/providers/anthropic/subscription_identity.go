package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeCodeIdentity is the leading system block Anthropic requires on
// subscription-authenticated /v1/messages calls. A subscription token whose
// request does not identify as Claude Code is answered with 429
// rate_limit_error regardless of remaining quota, so a cross-format or
// non-Claude-Code turn leased onto a healthy account would otherwise fail and
// cool that account down.
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// claudeCodeBillingHeaderPrefix opens the per-request billing line Claude Code
// sends as its first system block. Its cch and cc_prompt_id values change on
// every request; a block inserted ahead of it kept cache reads at zero.
const claudeCodeBillingHeaderPrefix = "x-anthropic-billing-header:"

// ensureClaudeCodeIdentity returns body with claudeCodeIdentity as its leading
// system block, after a leading billing line, preserving any caller-supplied
// system prompt. Bodies that already identify as Claude Code there are
// returned unchanged.
func ensureClaudeCodeIdentity(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	system := gjson.GetBytes(body, "system")
	switch {
	case system.IsArray() && len(system.Array()) > 0:
		blocks := system.Array()
		at := 0
		if strings.HasPrefix(blocks[0].Get("text").String(), claudeCodeBillingHeaderPrefix) {
			at = 1
		}
		if at < len(blocks) && identifiesAsClaudeCode(blocks[at].Get("text").String()) {
			return body
		}
		raws := make([]string, 0, len(blocks)+1)
		for _, block := range blocks[:at] {
			raws = append(raws, block.Raw)
		}
		raws = append(raws, textBlock(claudeCodeIdentity))
		for _, block := range blocks[at:] {
			raws = append(raws, block.Raw)
		}
		return setSystem(body, "["+strings.Join(raws, ",")+"]")
	case system.Type == gjson.String && system.String() != "":
		if identifiesAsClaudeCode(system.String()) {
			return body
		}
		return setSystem(body, "["+textBlock(claudeCodeIdentity)+","+textBlock(system.String())+"]")
	default:
		return setSystem(body, "["+textBlock(claudeCodeIdentity)+"]")
	}
}

// identifiesAsClaudeCode also accepts the Agent SDK variant ("…CLI for Claude,
// running within the Claude Agent SDK."), which Claude Code itself sends.
func identifiesAsClaudeCode(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), strings.TrimSuffix(claudeCodeIdentity, "."))
}

func textBlock(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		return `{"type":"text","text":""}`
	}
	return `{"type":"text","text":` + string(encoded) + `}`
}

func setSystem(body []byte, rawSystem string) []byte {
	out, err := sjson.SetRawBytes(body, "system", []byte(rawSystem))
	if err != nil {
		return body
	}
	return out
}
