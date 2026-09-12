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
	systemReminderCloseTag    = "</system-reminder>"
)

// ccToolFilterResult reports what filterClaudeCodeOnlyToolsFromAnthropicBody
// removed from the body.
type ccToolFilterResult struct {
	// ToolsRemoved counts CC-only tool schemas dropped from "tools".
	ToolsRemoved int
	// TaskRemindersRemoved counts task-list reminder segments cut from user
	// messages (standalone text blocks or appended to tool_result content).
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
// "task tools haven't been used recently" <system-reminder> segments are cut
// from user messages too (see stripTaskToolReminders), so the model is not
// nudged toward a tool it cannot see. The rewrite is deterministic, so the
// upstream-visible prefix stays cache-stable across turns.
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

// removeTaskToolReminders cuts every <system-reminder>…</system-reminder>
// segment carrying the task-list nudge out of s, with the whitespace Claude
// Code pads it with. Other reminders and prose mentioning the phrase stay.
func removeTaskToolReminders(s string) (string, int) {
	removed := 0
	for from := 0; ; {
		open := strings.Index(s[from:], systemReminderOpenTag)
		if open < 0 {
			return s, removed
		}
		open += from
		closeRel := strings.Index(s[open:], systemReminderCloseTag)
		if closeRel < 0 {
			return s, removed
		}
		end := open + closeRel + len(systemReminderCloseTag)
		if !strings.Contains(s[open:end], taskToolReminderMarker) {
			from = end
			continue
		}
		start := len(strings.TrimRight(s[:open], " \t\r\n"))
		s = s[:start] + s[end:]
		from = start
		removed++
	}
}

// systemMessageIsTaskReminder reports how many task-list reminders a system
// message's content consists of, or 0 when anything else would remain after
// cutting them.
func systemMessageIsTaskReminder(content gjson.Result) int {
	var texts []gjson.Result
	switch {
	case content.Type == gjson.String:
		texts = []gjson.Result{content}
	case content.IsArray():
		for _, block := range content.Array() {
			if block.Get("type").String() != "text" {
				return 0
			}
			texts = append(texts, block.Get("text"))
		}
	default:
		return 0
	}
	removed := 0
	for _, v := range texts {
		s, n := removeTaskToolReminders(v.String())
		if strings.TrimSpace(s) != "" {
			return 0
		}
		removed += n
	}
	return removed
}

// reminderEdit is one pending rewrite: set the string at path to text, or
// delete the element at path.
type reminderEdit struct {
	path string
	text string
	drop bool
}

// stripTaskToolReminders removes the task-list reminder from user and
// mid-conversation system messages. Claude Code appends it either as its own
// text block, inside the trailing tool_result's content (string or text
// parts), or as a standalone role:"system" message. A text element left empty
// by the cut is dropped, unless it is the sole element of its array. User
// prose left empty is kept so no message ends up without content; a
// tool_result left empty becomes empty tool output, which every emit target
// accepts; a system message left empty is dropped outright.
func stripTaskToolReminders(body []byte) (out []byte, removed int, err error) {
	if !bytes.Contains(body, []byte(taskToolReminderRawPrefix)) {
		return body, 0, nil
	}
	var edits []reminderEdit
	// editString rewrites a scalar string in place; an empty remainder is
	// written only when allowEmpty is set.
	editString := func(path string, v gjson.Result, allowEmpty bool) {
		s, n := removeTaskToolReminders(v.String())
		if n == 0 {
			return
		}
		if strings.TrimSpace(s) == "" {
			if !allowEmpty {
				return
			}
			s = ""
		}
		edits = append(edits, reminderEdit{path: path, text: s})
		removed += n
	}
	// editTextElem rewrites a text element of an array; an empty remainder
	// drops the element unless it is the array's only one, in which case it is
	// emptied when allowEmpty is set and otherwise left intact.
	editTextElem := func(path string, v gjson.Result, sole, allowEmpty bool) {
		s, n := removeTaskToolReminders(v.String())
		if n == 0 {
			return
		}
		empty := strings.TrimSpace(s) == ""
		if empty && sole {
			if !allowEmpty {
				return
			}
			edits = append(edits, reminderEdit{path: path, text: ""})
			removed += n
			return
		}
		edits = append(edits, reminderEdit{path: path, text: s, drop: empty})
		removed += n
	}
	gjson.GetBytes(body, "messages").ForEach(func(mi, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role != "user" && role != "system" {
			return true
		}
		msgPath := "messages." + mi.String() + ".content"
		content := msg.Get("content")
		if role == "system" {
			if n := systemMessageIsTaskReminder(content); n > 0 {
				edits = append(edits, reminderEdit{path: "messages." + mi.String(), drop: true})
				removed += n
				return true
			}
		}
		if content.Type == gjson.String {
			editString(msgPath, content, false)
			return true
		}
		if !content.IsArray() {
			return true
		}
		blocks := content.Array()
		for bi, block := range blocks {
			blockPath := msgPath + "." + strconv.Itoa(bi)
			switch block.Get("type").String() {
			case "text":
				editTextElem(blockPath+".text", block.Get("text"), len(blocks) == 1, false)
			case "tool_result":
				rc := block.Get("content")
				if rc.Type == gjson.String {
					editString(blockPath+".content", rc, true)
					continue
				}
				parts := rc.Array()
				for pi, part := range parts {
					if part.Get("type").String() == "text" {
						editTextElem(blockPath+".content."+strconv.Itoa(pi)+".text", part.Get("text"), len(parts) == 1, true)
					}
				}
			}
		}
		return true
	})
	if len(edits) == 0 {
		return body, 0, nil
	}
	out = body
	for _, e := range edits {
		if e.drop {
			continue
		}
		if out, err = sjson.SetBytes(out, e.path, e.text); err != nil {
			return body, 0, err
		}
	}
	// Drops run last and in reverse traversal order so indices stay valid.
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		if !e.drop {
			continue
		}
		if out, err = sjson.DeleteBytes(out, strings.TrimSuffix(e.path, ".text")); err != nil {
			return body, 0, err
		}
	}
	return out, removed, nil
}
