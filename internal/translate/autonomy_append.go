package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const AutonomySystemText = "You are operating autonomously. The user is not watching in real time and cannot answer questions mid-task, so asking 'Want me to...?' or 'Shall I...?' will block the work. For reversible actions that follow from the original request, proceed without asking. Stop only for destructive actions or genuine scope changes the user must decide. End your turn only when the task is complete or you are blocked on input only the user can provide."

const autonomySystemMarker = "operating autonomously"

func (e *RequestEnvelope) HasAutonomySystemText() bool {
	return strings.Contains(strings.ToLower(e.SystemText()), autonomySystemMarker)
}

func appendAutonomySystem(body []byte) ([]byte, error) {
	return appendSystemText(body, AutonomySystemText)
}

func appendSystemText(body []byte, text string) ([]byte, error) {
	system := gjson.GetBytes(body, "system")
	var (
		out []byte
		err error
	)
	switch {
	case system.Type == gjson.String:
		out, err = sjson.SetBytes(body, "system", system.String()+"\n\n"+text)
	case system.IsArray():
		block, _ := sjson.SetBytes([]byte(`{"type":"text"}`), "text", text)
		out, err = sjson.SetRawBytes(body, "system.-1", block)
	default:
		out, err = sjson.SetBytes(body, "system", text)
	}
	if err != nil {
		return nil, fmt.Errorf("append system block: %w", err)
	}
	return out, nil
}

func (e *RequestEnvelope) withAutonomySystemAppended(opts EmitOptions) (*RequestEnvelope, error) {
	if !opts.AppendAutonomySystem || e.format != FormatAnthropic {
		return e, nil
	}
	body, err := appendAutonomySystem(e.body)
	if err != nil {
		return nil, err
	}
	return &RequestEnvelope{body: body, format: e.format}, nil
}
