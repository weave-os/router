package translate

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var claudeCodeOnlyToolNames = map[string]struct{}{
	"Task":                     {},
	"Agent":                    {},
	"TaskCreate":               {},
	"TaskUpdate":               {},
	"TaskGet":                  {},
	"TaskList":                 {},
	"TaskOutput":               {},
	"TaskStop":                 {},
	"SendMessage":              {},
	"EnterPlanMode":            {},
	"ExitPlanMode":             {},
	"UpdatePlan":               {},
	"Skill":                    {},
	"Workflow":                 {},
	"AskUserQuestion":          {},
	"ToolSearch":               {},
	"TodoWrite":                {},
	"PushNotification":         {},
	"RemoteTrigger":            {},
	"EnterWorktree":            {},
	"ExitWorktree":             {},
	"LSP":                      {},
	"ListMcpResourcesTool":     {},
	"ReadMcpResourceTool":      {},
	"ListMcpResources":         {},
	"ListMcpResourceTemplates": {},
	"ReadMcpResource":          {},
}

func isClaudeCodeOnlyTool(name string) bool {
	_, ok := claudeCodeOnlyToolNames[name]
	return ok
}

var claudeCodeOrchestrationToolNames = map[string]struct{}{
	"Task":          {},
	"Agent":         {},
	"TaskOutput":    {},
	"TaskStop":      {},
	"SendMessage":   {},
	"Workflow":      {},
	"Skill":         {},
	"EnterPlanMode": {},
	"ExitPlanMode":  {},
	"UpdatePlan":    {},
}

func isCrossVendorOrchestrationTool(name string) bool {
	_, ok := claudeCodeOrchestrationToolNames[name]
	return ok
}

var claudeCodeAlwaysKeptToolNames = map[string]struct{}{
	"ToolSearch": {},
}

func isAlwaysKeptCrossVendorTool(name string) bool {
	_, ok := claudeCodeAlwaysKeptToolNames[name]
	return ok
}

type ccToolFilterOptions struct {
	KeepOrchestration bool
	KeepTaskTools     bool
}

func (o EmitOptions) ccToolFilter() ccToolFilterOptions {
	return ccToolFilterOptions{
		KeepOrchestration: o.KeepCrossVendorOrchestrationTools,
		KeepTaskTools:     o.KeepCrossVendorTaskTools,
	}
}

func shouldStripCCTool(name string, opts ccToolFilterOptions) bool {
	if !isClaudeCodeOnlyTool(name) {
		return false
	}
	if isAlwaysKeptCrossVendorTool(name) {
		return false
	}
	if !opts.KeepOrchestration {
		return true
	}
	if isTaskBookkeepingTool(name) {
		return !opts.KeepTaskTools
	}
	return !isCrossVendorOrchestrationTool(name)
}

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

const (
	taskToolReminderMarker    = "task tools haven't been used recently"
	taskToolReminderRawPrefix = "task tools haven"
	systemReminderOpenTag     = "<system-reminder>"
	systemReminderCloseTag    = "</system-reminder>"
)

type ccToolFilterResult struct {
	ToolsRemoved         int
	TaskRemindersRemoved int
}

func filterClaudeCodeOnlyToolsFromAnthropicBody(body []byte, opts ccToolFilterOptions) (out []byte, res ccToolFilterResult, err error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, res, nil
	}

	bookkeepingRemoved := false
	tools.ForEach(func(_, t gjson.Result) bool {
		name := t.Get("name").String()
		if shouldStripCCTool(name, opts) {
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
		if !shouldStripCCTool(t.Get("name").String(), opts) {
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

type reminderEdit struct {
	path string
	text string
	drop bool
}

func stripTaskToolReminders(body []byte) (out []byte, removed int, err error) {
	if !bytes.Contains(body, []byte(taskToolReminderRawPrefix)) {
		return body, 0, nil
	}
	var edits []reminderEdit
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
