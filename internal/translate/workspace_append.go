package translate

import (
	"strings"
)

// WorkspaceSystemText tells a model that a bare question refers to the
// checkout it is running in, so it inspects before answering. Claude Code's
// own system prompt says nothing of the kind (the SDK prompt only names the
// working directory), and a GPT-family model given a short support-style
// question answers it from memory in one turn without touching a tool.
const WorkspaceSystemText = "You are working inside a specific project checkout. Questions refer to this project. Before answering, use your tools to inspect the workspace (files, schemas, configuration, data) and base the answer on what you find; do not answer from general knowledge alone. Complete the task rather than describing how the user could do it."

const workspaceSystemMarker = "inspect the workspace"

func (e *RequestEnvelope) HasWorkspaceSystemText() bool {
	return strings.Contains(strings.ToLower(e.SystemText()), workspaceSystemMarker)
}

// withWorkspaceSystemAppended is the non-Anthropic counterpart of
// withAutonomySystemAppended: same final-uncached-block position, same
// Anthropic-source-only rule, but never applied by PrepareAnthropic, since a
// Claude-served turn already inspects the workspace on its own. Unlike the
// autonomy text it is also carried into Gemini's systemInstruction.
func (e *RequestEnvelope) withWorkspaceSystemAppended(opts EmitOptions) (*RequestEnvelope, error) {
	if !opts.AppendWorkspaceSystem || e.format != FormatAnthropic {
		return e, nil
	}
	body, err := appendSystemText(e.body, WorkspaceSystemText)
	if err != nil {
		return nil, err
	}
	return &RequestEnvelope{body: body, format: e.format}, nil
}

// withRouterSystemAppends applies every proxy-requested system append in a
// fixed order (autonomy, then workspace) so the outgoing prompt is stable
// across attempts.
func (e *RequestEnvelope) withRouterSystemAppends(opts EmitOptions) (*RequestEnvelope, error) {
	e, err := e.withAutonomySystemAppended(opts)
	if err != nil {
		return nil, err
	}
	return e.withWorkspaceSystemAppended(opts)
}
