package translate

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeCodeOnlyToolNames is the set of tools Claude Code (the client)
// implements internally — Task subagent dispatch, plan-mode toggles, Skill
// invocation, deferred tool loading, etc. Most have no useful behavior for a
// non-Anthropic model unless explicitly allowed by one of the subsets below.
// Emitting the rest is noise; worse, non-Anthropic models routinely
// hallucinate calls to them. On the v0.57 SWE-bench Verified eval, 224 phantom
// tool_use blocks for these names were observed across 150 router shards
// routed to non-Anthropic upstreams — 96% of them in the Task* family — with
// 27% clustering on the empty-patch failure subset.
//
// The filter applies only on Anthropic→non-Anthropic emit paths
// (buildOpenAIFromAnthropic and the Anthropic case of PrepareGemini). The
// Anthropic→Anthropic passthrough preserves them.
//
// Scheduling / wake-up tools (ScheduleWakeup, CronCreate/Delete/List, Monitor)
// and shell-session tools (BashOutput, KillShell) are client-executed like
// Read/Bash; stripping them broke /loop and background-shell control on
// non-Anthropic routes. NotebookEdit is a coding tool (same family as Edit),
// not a CC-internal control-plane tool.
var claudeCodeOnlyToolNames = map[string]struct{}{
	// Subagent dispatch. Task = pre-2.1 name; Agent = current CC name.
	"Task":        {},
	"Agent":       {},
	"TaskCreate":  {},
	"TaskUpdate":  {},
	"TaskGet":     {},
	"TaskList":    {},
	"TaskOutput":  {},
	"TaskStop":    {},
	"SendMessage": {}, // teammate/subagent messaging: CC-internal like Task*
	// Plan mode / skills / workflows.
	"EnterPlanMode":   {},
	"ExitPlanMode":    {},
	"UpdatePlan":      {},
	"Skill":           {},
	"Workflow":        {},
	"AskUserQuestion": {},
	// Client-side deferred MCP schema loader. Claude Code sends deferred tool
	// names in the prompt and exposes ToolSearch as the only way to load one.
	"ToolSearch": {},
	// Todo bookkeeping that non-Anthropic models invent.
	"TodoWrite": {},
	// Notifications / remote triggers / worktrees / LSP.
	"PushNotification": {},
	"RemoteTrigger":    {},
	"EnterWorktree":    {},
	"ExitWorktree":     {},
	"LSP":              {},
	// MCP resource listing — both historic *Tool suffix and current names.
	"ListMcpResourcesTool":     {},
	"ReadMcpResourceTool":      {},
	"ListMcpResources":         {},
	"ListMcpResourceTemplates": {},
	"ReadMcpResource":          {},
}

// isClaudeCodeOnlyTool reports whether name is one of the tools Claude Code
// dispatches internally and that requires an explicit cross-vendor policy.
// Names are compared case-sensitively because Claude Code emits them in
// PascalCase verbatim.
func isClaudeCodeOnlyTool(name string) bool {
	_, ok := claudeCodeOnlyToolNames[name]
	return ok
}

// claudeCodeOrchestrationToolNames is the subset of claudeCodeOnlyToolNames
// a capable non-Anthropic model can act on. Must stay a strict subset of
// claudeCodeOnlyToolNames — see TestOrchestrationToolsAreSubsetOfCCOnly.
//
// Task-list bookkeeping (claudeCodeTaskBookkeepingToolNames) is excluded:
// non-Anthropic models obey Claude Code's "task tools haven't been used"
// reminder literally and burn whole turns on TaskCreate/TaskUpdate.
// TaskOutput/TaskStop address background agents and stay.
var claudeCodeOrchestrationToolNames = map[string]struct{}{
	"Task":          {},
	"Agent":         {},
	"TaskOutput":    {},
	"TaskStop":      {},
	"Workflow":      {},
	"Skill":         {},
	"EnterPlanMode": {},
	"ExitPlanMode":  {},
	"UpdatePlan":    {},
}

// isCrossVendorOrchestrationTool reports whether name is a Claude Code
// orchestration tool that may be preserved on cross-vendor emit.
func isCrossVendorOrchestrationTool(name string) bool {
	_, ok := claudeCodeOrchestrationToolNames[name]
	return ok
}

// claudeCodeAlwaysKeptToolNames is the subset of claudeCodeOnlyToolNames that
// must survive every cross-vendor emit. These tools are executed by the client
// and are required for capabilities advertised in the request itself.
var claudeCodeAlwaysKeptToolNames = map[string]struct{}{
	"ToolSearch": {},
}

func isAlwaysKeptCrossVendorTool(name string) bool {
	_, ok := claudeCodeAlwaysKeptToolNames[name]
	return ok
}

// shouldStripCCTool reports whether a tool must be dropped from a cross-vendor
// emit. Non-CC-only tools and the always-kept client tools are retained.
// Other CC-only tools are dropped, except that orchestration tools are
// retained when keepOrchestration is set.
func shouldStripCCTool(name string, keepOrchestration bool) bool {
	if !isClaudeCodeOnlyTool(name) {
		return false
	}
	if isAlwaysKeptCrossVendorTool(name) {
		return false
	}
	if keepOrchestration && isCrossVendorOrchestrationTool(name) {
		return false
	}
	return true
}

// claudeCodeTaskBookkeepingToolNames are the task-list tools whose Claude Code
// reminder is dropped alongside the schemas — see stripTaskToolReminders.
var claudeCodeTaskBookkeepingToolNames = map[string]struct{}{
	"TaskCreate": {},
	"TaskUpdate": {},
	"TaskGet":    {},
	"TaskList":   {},
}

func isTaskBookkeepingTool(name string) bool {
	_, ok := claudeCodeTaskBookkeepingToolNames[name]
	return ok
}

// The apostrophe may arrive JSON-escaped, so the raw-body precheck stops
// short of it.
const (
	taskToolReminderMarker    = "task tools haven't been used recently"
	taskToolReminderRawPrefix = "task tools haven"
	systemReminderOpenTag     = "<system-reminder>"
)

// ccToolFilterResult reports what filterClaudeCodeOnlyToolsFromAnthropicBody
// removed from the body.
type ccToolFilterResult struct {
	// ToolsRemoved counts CC-only tool schemas dropped from "tools".
	ToolsRemoved int
	// TaskRemindersRemoved counts task-list reminder text blocks dropped from
	// user messages.
	TaskRemindersRemoved int
}

// filterClaudeCodeOnlyToolsFromAnthropicBody returns body with any
// Claude-Code-only tools removed from the top-level "tools" array. Returns
// body unchanged when none match, so callers can apply this unconditionally
// without paying a re-serialize cost on the common case.
//
// ToolSearch is always retained because it is Claude Code's client-side
// loader for deferred MCP schemas. When keepOrchestration is set, the
// orchestration subset (Task/Agent, TaskOutput/TaskStop, Workflow, Skill,
// plan-mode) is also retained; other CC-only tools are still dropped.
//
// When a task-list bookkeeping tool is dropped, Claude Code's matching
// "task tools haven't been used recently" <system-reminder> text blocks are
// dropped from user messages too, so the model is not nudged toward a tool
// it cannot see. The rewrite is deterministic, so the upstream-visible
// prefix stays cache-stable across turns.
//
// Otherwise tool_choice and message content are left alone. tool_choice is
// rare and Anthropic only honors "any"/"auto"/name=X anyway, so a stale
// tool_choice referencing a stripped CC-only name would be ignored upstream.
// Existing tool_use/tool_result blocks from past turns are not rewritten
// because those represent history the model has already acted on —
// rewriting them would invalidate prompt caches and could leave dangling
// tool_use_id references.
func filterClaudeCodeOnlyToolsFromAnthropicBody(body []byte, keepOrchestration bool) (out []byte, res ccToolFilterResult, err error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, res, nil
	}

	bookkeepingRemoved := false
	tools.ForEach(func(_, t gjson.Result) bool {
		name := t.Get("name").String()
		if shouldStripCCTool(name, keepOrchestration) {
			res.ToolsRemoved++
			bookkeepingRemoved = bookkeepingRemoved || isTaskBookkeepingTool(name)
		}
		return true
	})
	if res.ToolsRemoved == 0 {
		return body, res, nil
	}

	jw := newJSONWriter()
	jw.Arr()
	tools.ForEach(func(_, t gjson.Result) bool {
		if !shouldStripCCTool(t.Get("name").String(), keepOrchestration) {
			jw.Raw(t.Raw)
		}
		return true
	})
	jw.EndArr()
	out, err = sjson.SetRawBytes(body, "tools", jw.Bytes())
	if err != nil || !bookkeepingRemoved {
		return out, res, err
	}
	out, res.TaskRemindersRemoved, err = stripTaskToolReminders(out)
	return out, res, err
}

// isTaskToolReminderBlock reports whether block is a Claude Code
// <system-reminder> text block carrying the task-list nudge. Only whole
// reminder blocks qualify; user-authored text mentioning the phrase is kept.
func isTaskToolReminderBlock(block gjson.Result) bool {
	if block.Get("type").String() != "text" {
		return false
	}
	text := strings.TrimSpace(block.Get("text").String())
	return strings.HasPrefix(text, systemReminderOpenTag) && strings.Contains(text, taskToolReminderMarker)
}

// stripTaskToolReminders drops task-list reminder text blocks from user
// messages. A message whose every block is such a reminder is left intact so
// no message ends up with empty content.
func stripTaskToolReminders(body []byte) (out []byte, removed int, err error) {
	if !bytes.Contains(body, []byte(taskToolReminderRawPrefix)) {
		return body, 0, nil
	}
	type blockPath struct{ msg, block int }
	var drop []blockPath
	gjson.GetBytes(body, "messages").ForEach(func(mi, msg gjson.Result) bool {
		content := msg.Get("content")
		if msg.Get("role").String() != "user" || !content.IsArray() {
			return true
		}
		var hits []blockPath
		total := 0
		content.ForEach(func(bi, block gjson.Result) bool {
			total++
			if isTaskToolReminderBlock(block) {
				hits = append(hits, blockPath{int(mi.Int()), int(bi.Int())})
			}
			return true
		})
		if len(hits) < total {
			drop = append(drop, hits...)
		}
		return true
	})
	if len(drop) == 0 {
		return body, 0, nil
	}
	out = body
	// Delete from the end so earlier indices stay valid.
	for i := len(drop) - 1; i >= 0; i-- {
		p := drop[i]
		out, err = sjson.DeleteBytes(out, "messages."+strconv.Itoa(p.msg)+".content."+strconv.Itoa(p.block))
		if err != nil {
			return body, 0, err
		}
	}
	return out, len(drop), nil
}
