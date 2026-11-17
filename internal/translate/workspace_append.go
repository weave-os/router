package translate

import (
	"strings"
)

const WorkspaceSystemText = "You are working inside a specific project checkout. Questions refer to this project. Before answering, use your tools to inspect the workspace (files, schemas, configuration, data) and base the answer on what you find; do not answer from general knowledge alone. Complete the task rather than describing how the user could do it."

const workspaceSystemMarker = "inspect the workspace"

func (e *RequestEnvelope) HasWorkspaceSystemText() bool {
	return strings.Contains(strings.ToLower(e.SystemText()), workspaceSystemMarker)
}

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

func (e *RequestEnvelope) withRouterSystemAppends(opts EmitOptions) (*RequestEnvelope, error) {
	e, err := e.withAutonomySystemAppended(opts)
	if err != nil {
		return nil, err
	}
	return e.withWorkspaceSystemAppended(opts)
}
