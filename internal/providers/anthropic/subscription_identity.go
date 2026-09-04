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

// ensureClaudeCodeIdentity returns body with claudeCodeIdentity as its leading
// system block, preserving any caller-supplied system prompt. Bodies that
// already identify as Claude Code are returned unchanged.
func ensureClaudeCodeIdentity(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	system := gjson.GetBytes(body, "system")
	switch {
	case system.IsArray() && len(system.Array()) > 0:
		if identifiesAsClaudeCode(system.Array()[0].Get("text").String()) {
			return body
		}
		return setSystem(body, "["+textBlock(claudeCodeIdentity)+","+strings.TrimPrefix(system.Raw, "["))
	case system.Type == gjson.String && system.String() != "":
		if identifiesAsClaudeCode(system.String()) {
			return body
		}
		return setSystem(body, "["+textBlock(claudeCodeIdentity)+","+textBlock(system.String())+"]")
	default:
		return setSystem(body, "["+textBlock(claudeCodeIdentity)+"]")
	}
}

func identifiesAsClaudeCode(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), claudeCodeIdentity)
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
