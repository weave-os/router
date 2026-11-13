package translate

import (
	"regexp"

	"github.com/tidwall/gjson"
)

// CodexSkillTurn names the router skill whose emitted directive a Codex
// tool-result turn returns.
type CodexSkillTurn int

const (
	CodexSkillTurnNone CodexSkillTurn = iota
	// CodexSkillTurnFeedback is a user-invoked $rf/$router-feedback skill.
	CodexSkillTurnFeedback
	// CodexSkillTurnForceModel is a force-model skill's emit.sh, whose
	// directive ends the turn.
	CodexSkillTurnForceModel
)

func codexSkillTurnFor(input gjson.Result) CodexSkillTurn {
	switch {
	case codexFeedbackSkillInvocation(input):
		return CodexSkillTurnFeedback
	case codexForceModelSkillToolOutput(input):
		return CodexSkillTurnForceModel
	default:
		return CodexSkillTurnNone
	}
}

var codexForceModelSkillScript = regexp.MustCompile(`/skills/(force-model|fm|force-parent|unforce-model|ufm)/scripts/emit\.sh`)

// codexForceModelSkillToolOutput reports whether the latest tool output since
// the last user message answers a call that ran a force-model skill script.
func codexForceModelSkillToolOutput(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	skillCalls := make(map[string]struct{})
	matched := false
	for _, item := range input.Array() {
		switch item.Get("type").Str {
		case "message":
			if item.Get("role").Str == "user" && !codexFeedbackSkillInstructions(item.Get("content")) {
				clear(skillCalls)
				matched = false
			}
		case "function_call", "custom_tool_call":
			if codexForceModelSkillScript.MatchString(item.Get("input").Str + item.Get("arguments").Str) {
				skillCalls[item.Get("call_id").Str] = struct{}{}
			}
		case "function_call_output", "custom_tool_call_output":
			_, matched = skillCalls[item.Get("call_id").Str]
		}
	}
	return matched
}
