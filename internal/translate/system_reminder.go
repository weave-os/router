package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func openRouterSystemReminder(model string) string {
	if strings.HasPrefix(model, "deepseek/") {
		return deepseekToolUseReminder
	}
	return ""
}

func geminiSystemReminder(model string) string {
	if usesGemini3Contract(model) {
		return geminiToolUseReminder
	}
	return ""
}

const deepseekToolUseReminder = "When using file-edit tools, copy `old_string` byte-for-byte from the most recent file read — preserve tabs, leading and trailing whitespace, and unicode characters (em-dash —, smart quotes, non-breaking spaces) exactly. If an Edit call fails, re-read the file before retrying. Never fall back to shell commands (sed, awk, python) to modify files."

const geminiToolUseReminder = "When the user has asked you to apply a change, prefer calling the Edit or Write tool over describing the edit in prose or markdown; once you have read enough to know what to change, the next step is the Edit/Write call, not a summary. If the user only asked for analysis, a review, or what could be fixed, answer in prose — do not edit, commit, or push unless they asked you to."

func applySystemReminderToBody(body []byte, reminder string) ([]byte, error) {
	if reminder == "" {
		return body, nil
	}
	msgsResult := gjson.GetBytes(body, "messages")
	if !msgsResult.IsArray() {
		jw := newJSONWriter()
		jw.Arr()
		jw.Obj()
		jw.Key("role")
		jw.Str("system")
		jw.Key("content")
		jw.Str(reminder)
		jw.EndObj()
		jw.EndArr()
		return sjson.SetRawBytes(body, "messages", jw.Bytes())
	}

	sysIdx := -1
	msgsResult.ForEach(func(key, value gjson.Result) bool {
		if value.Get("role").String() == "system" {
			sysIdx = int(key.Int())
			return false
		}
		return true
	})

	if sysIdx >= 0 {
		contentPath := fmt.Sprintf("messages.%d.content", sysIdx)
		contentResult := gjson.GetBytes(body, contentPath)

		if contentResult.IsArray() {
			jw := newJSONWriter()
			jw.Obj()
			jw.Key("type")
			jw.Str("text")
			jw.Key("text")
			jw.Str(reminder)
			jw.EndObj()
			return sjson.SetRawBytes(body, contentPath+".-1", jw.Bytes())
		}
		newContent := contentResult.String() + "\n\n" + reminder
		return sjson.SetBytes(body, contentPath, newContent)
	}

	jw := newJSONWriter()
	jw.Arr()
	jw.Obj()
	jw.Key("role")
	jw.Str("system")
	jw.Key("content")
	jw.Str(reminder)
	jw.EndObj()
	msgsResult.ForEach(func(_, value gjson.Result) bool {
		jw.Raw(value.Raw)
		return true
	})
	jw.EndArr()
	return sjson.SetRawBytes(body, "messages", jw.Bytes())
}
