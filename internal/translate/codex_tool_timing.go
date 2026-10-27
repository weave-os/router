package translate

import (
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	codexCodeExec codexExecTool = "exec"
	codexCodeWait codexExecTool = "wait"
)

var codexCompletedScriptRuntime = regexp.MustCompile(`^Script completed\nWall time ([0-9]+(?:\.[0-9]+)?) seconds\nOutput:\n`)
var codexExecCallRuntime = regexp.MustCompile(`^(?:Chunk ID: [^\r\n]+\n)?Wall time: ([0-9]+(?:\.[0-9]+)?) seconds\n(Process exited with code -?[0-9]+|Process running with session ID [0-9]+)\n(?:Original token count: [0-9]+\n)?Output:(?:\n|$)`)

type ClientToolTiming struct {
	CallDuration time.Duration
	Yielded      bool
}

func CodexReportedToolTiming(body []byte) (ClientToolTiming, bool) {
	items := gjson.GetBytes(body, "input").Array()
	if len(items) < 2 {
		return ClientToolTiming{}, false
	}
	toolOutput := items[len(items)-1]
	var callType escalationWireType
	switch escalationWireType(toolOutput.Get("type").String()) {
	case escalationWireCustomOutput:
		callType = escalationWireCustomCall
	case escalationWireFunctionOutput:
		callType = escalationWireFunctionCall
	default:
		return ClientToolTiming{}, false
	}
	callID := toolOutput.Get("call_id").String()
	if callID == "" {
		return ClientToolTiming{}, false
	}
	matched := false
	var tool codexExecTool
	for index := len(items) - 2; index >= 0; index-- {
		call := items[index]
		if escalationWireType(call.Get("type").String()) != callType || call.Get("call_id").String() != callID {
			continue
		}
		if namespace := call.Get("namespace").String(); namespace != "" && namespace != responsesDefaultToolNamespace {
			return ClientToolTiming{}, false
		}
		tool = codexExecTool(call.Get("name").String())
		switch tool {
		case codexCodeExec, codexCodeWait:
			matched = callType == escalationWireCustomCall
		case codexExecCommand, codexWriteStdin:
			matched = callType == escalationWireFunctionCall
		}
		break
	}
	if !matched {
		return ClientToolTiming{}, false
	}
	output := toolOutput.Get("output")
	if output.IsArray() {
		fragments := output.Array()
		if len(fragments) == 0 || escalationWireType(fragments[0].Get("type").String()) != escalationWireInputText {
			return ClientToolTiming{}, false
		}
		output = fragments[0].Get("text")
	}
	if output.Type != gjson.String {
		return ClientToolTiming{}, false
	}
	header := codexCompletedScriptRuntime.FindStringSubmatch(output.String())
	if tool == codexExecCommand || tool == codexWriteStdin {
		header = codexExecCallRuntime.FindStringSubmatch(output.String())
	}
	if header == nil {
		return ClientToolTiming{}, false
	}
	runtime, err := time.ParseDuration(header[1] + "s")
	return ClientToolTiming{CallDuration: runtime, Yielded: len(header) > 2 && strings.HasPrefix(header[2], "Process running")}, err == nil
}
