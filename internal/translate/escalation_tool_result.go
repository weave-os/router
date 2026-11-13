package translate

import (
	"regexp"
	"strconv"

	"github.com/tidwall/gjson"
)

type codexExecTool string

const (
	codexExecCommand codexExecTool = "exec_command"
	codexWriteStdin  codexExecTool = "write_stdin"
)

// Codex emits this status before stdout, without a wire is_error field.
// Anchor the whole header so command output cannot supply the exit verdict.
var codexExecExitHeader = regexp.MustCompile(`^(?:Chunk ID: [^\r\n]+\n)?Wall time: [0-9]+(?:\.[0-9]+)? seconds\nProcess exited with code (-?[0-9]+)\n(?:Original token count: [0-9]+\n)?Output:(?:\n|$)`)

// Code-mode exec reports its script verdict first, then one fragment per
// printed value. Nested commands print Codex's exec envelope, whose key order
// is fixed, so only a fragment that starts with it can supply an exit status.
var codexScriptHeader = regexp.MustCompile(`^Script (completed|failed|running with cell ID [0-9]+)\nWall time [0-9]+(?:\.[0-9]+)? seconds\nOutput:(?:\n|$)`)
var codexScriptExitEnvelope = regexp.MustCompile(`^(?:Warning: truncated output \(original token count: [0-9]+\)\nTotal output lines: [0-9]+\n\n)?\{"chunk_id":"[^"\\]*","wall_time_seconds":[0-9.eE+-]+,"exit_code":(-?[0-9]+),`)

// ToolResultFailed recognizes explicit protocol errors and Codex exec and code-mode exit status.
// It leaves the observed wire untouched: replay digests must survive feature fixes.
func (block EscalationBlock) ToolResultFailed(call EscalationBlock, codexToolResults bool) bool {
	if block.IsError != nil {
		return *block.IsError
	}
	if !codexToolResults || (call.Namespace != "" && call.Namespace != responsesDefaultToolNamespace) {
		return false
	}
	switch codexExecTool(call.Name) {
	case codexCodeExec:
		return codexScriptFailed(block.ContentJSON)
	case codexExecCommand, codexWriteStdin:
	default:
		return false
	}
	output := gjson.Parse(block.ContentJSON)
	if output.IsArray() {
		fragments := output.Array()
		if len(fragments) != 1 || escalationWireType(fragments[0].Get("type").String()) != escalationWireInputText {
			return false
		}
		output = fragments[0].Get("text")
	}
	if output.Type != gjson.String {
		return false
	}
	header := codexExecExitHeader.FindStringSubmatch(output.String())
	if header == nil {
		return false
	}
	exitCode, err := strconv.ParseInt(header[1], 10, 32)
	return err == nil && exitCode != 0
}

func codexScriptFailed(contentJSON string) bool {
	content := gjson.Parse(contentJSON)
	fragments := []gjson.Result{content}
	if content.IsArray() {
		fragments = content.Array()
		for index, fragment := range fragments {
			if escalationWireType(fragment.Get("type").String()) != escalationWireInputText {
				return false
			}
			fragments[index] = fragment.Get("text")
		}
	}
	if len(fragments) == 0 || fragments[0].Type != gjson.String {
		return false
	}
	header := codexScriptHeader.FindStringSubmatch(fragments[0].String())
	if header == nil {
		return false
	}
	if header[1] == "failed" {
		return true
	}
	for _, fragment := range fragments[1:] {
		envelope := codexScriptExitEnvelope.FindStringSubmatch(fragment.String())
		if envelope == nil {
			continue
		}
		if exitCode, err := strconv.ParseInt(envelope[1], 10, 32); err == nil && exitCode != 0 {
			return true
		}
	}
	return false
}
