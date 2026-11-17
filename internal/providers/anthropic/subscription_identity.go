package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

const claudeCodeBillingHeaderPrefix = "x-anthropic-billing-header:"

func ensureClaudeCodeIdentity(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	system := gjson.GetBytes(body, "system")
	switch {
	case system.IsArray() && len(system.Array()) > 0:
		blocks := system.Array()
		insertAt := 0
		if strings.HasPrefix(blocks[0].Get("text").String(), claudeCodeBillingHeaderPrefix) {
			insertAt = 1
		}
		if insertAt < len(blocks) && identifiesAsClaudeCode(blocks[insertAt].Get("text").String()) {
			return body
		}
		raws := make([]string, 0, len(blocks)+1)
		for _, block := range blocks[:insertAt] {
			raws = append(raws, block.Raw)
		}
		raws = append(raws, textBlock(claudeCodeIdentity))
		for _, block := range blocks[insertAt:] {
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
