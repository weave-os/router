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

// ToolResultFailed recognizes explicit protocol errors and Codex exec exit status.
// It leaves the observed wire untouched: replay digests must survive feature fixes.
func (block EscalationBlock) ToolResultFailed(call EscalationBlock, codexToolResults bool) bool {
	if block.IsError != nil {
		return *block.IsError
	}
	if !codexToolResults || (call.Namespace != "" && call.Namespace != responsesDefaultToolNamespace) {
		return false
	}
	switch codexExecTool(call.Name) {
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
