package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AutonomySystemText is Anthropic's own unattended-run instruction, the one
// Claude Code injects itself for some models; the marker below is what makes
// the append idempotent against that.
const AutonomySystemText = "You are operating autonomously. The user is not watching in real time and cannot answer questions mid-task, so asking 'Want me to...?' or 'Shall I...?' will block the work. For reversible actions that follow from the original request, proceed without asking. Stop only for destructive actions or genuine scope changes the user must decide. End your turn only when the task is complete or you are blocked on input only the user can provide."

const autonomySystemMarker = "operating autonomously"

func (e *RequestEnvelope) HasAutonomySystemText() bool {
	return strings.Contains(strings.ToLower(e.SystemText()), autonomySystemMarker)
}

func appendAutonomySystem(body []byte) ([]byte, error) {
	return appendSystemText(body, AutonomySystemText)
}

// appendSystemText adds text as the final element of an Anthropic-format
// body's system field, carrying no cache_control. Appending (rather than
// prepending) keeps every byte the client marked cacheable in place, so the
// only cost is one small uncached segment per request.
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

// Cross-format emitters translate the appended Anthropic system field, so the
// block lands in the OpenAI system message / Responses instructions without
// per-format code. Claude Code only speaks the Anthropic format, so other
// source formats are left alone. PrepareGemini deliberately does not call
// this: a "keep going until complete" system nudge is what tipped Gemini 3.x
// into explore-loop spirals and text-only tool markup (see
// geminiSystemReminder), so a Gemini-served attempt keeps the client prompt.
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
