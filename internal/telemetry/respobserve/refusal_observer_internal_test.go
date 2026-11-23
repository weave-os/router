package respobserve

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type progressArmingWriter struct {
	*httptest.ResponseRecorder
	armed bool
}

func (w *progressArmingWriter) ArmOutputProgress(func()) bool {
	w.armed = true
	return true
}

// The Anthropic client arms its idle and stall watchdogs through the response
// writer chain; an observer in that chain must not disarm them.
func TestPassiveRefusalObserver_ForwardsOutputProgressArming(t *testing.T) {
	inner := &progressArmingWriter{ResponseRecorder: httptest.NewRecorder()}

	assert.True(t, NewPassiveRefusalObserver(inner).ArmOutputProgress(func() {}))
	assert.True(t, inner.armed)
	assert.False(t, NewPassiveRefusalObserver(httptest.NewRecorder()).ArmOutputProgress(func() {}))
}

func TestRefusalCategory(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"api_refusal_category", `{"api_refusal_category":"reasoning_extraction"}`, "reasoning_extraction"},
		{"rendered client text", "safeguards flagged this message. Details: [reasoning_extraction] Request ID: req_1", "reasoning_extraction"},
		{"no category", `"stop_reason":"refusal"`, ""},
		{"unterminated field", `{"api_refusal_category":"cyber`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RefusalCategory([]byte(c.in)); got != c.want {
				t.Fatalf("RefusalCategory(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRefusalObserver_CapturesCategory(t *testing.T) {
	rec := httptest.NewRecorder()
	obs := NewRefusalObserver(rec)
	if _, err := obs.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"refusal","api_refusal_category":"reasoning_extraction"}}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !obs.Refused {
		t.Fatal("refusal not detected")
	}
	if obs.Category != "reasoning_extraction" {
		t.Fatalf("category = %q, want reasoning_extraction", obs.Category)
	}
}

func TestDetectRefusalSignal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"stop_reason refusal", `"stop_reason":"refusal"`, true},
		{"refusal content block", `{"type":"refusal","text":"..."}`, true},
		{"api_refusal_category", `"api_refusal_category":"cyber"`, true},
		{"safeguard text mixed case", "This request triggered cyber-related SAFEGUARDS FLAGGED for review", true},
		{"normal end_turn", `"stop_reason":"end_turn"`, false},
		{"benign cybersecurity mention", "here is a summary of cybersecurity best practices", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectRefusalSignal([]byte(c.in)); got != c.want {
				t.Fatalf("DetectRefusalSignal(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestRefusalObserver_ForwardsUnchangedAndDetects(t *testing.T) {
	// Signal split across two Write calls must still be caught, and every byte
	// must be forwarded to inner unchanged (observe-only).
	rec := httptest.NewRecorder()
	obs := NewRefusalObserver(rec)
	half := len(refusalSSE) / 2
	if _, err := obs.Write([]byte(refusalSSE[:half])); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if _, err := obs.Write([]byte(refusalSSE[half:])); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if !obs.Refused {
		t.Fatal("observer did not detect the refusal split across writes")
	}
	if got := rec.Body.String(); got != refusalSSE {
		t.Fatalf("observer altered forwarded bytes:\n got=%q\nwant=%q", got, refusalSSE)
	}

	rec2 := httptest.NewRecorder()
	obs2 := NewRefusalObserver(rec2)
	if _, err := obs2.Write([]byte(normalSSE)); err != nil {
		t.Fatalf("write normal: %v", err)
	}
	if obs2.Refused {
		t.Fatal("observer false-positived on a normal end_turn response")
	}
	if rec2.Body.String() != normalSSE {
		t.Fatal("observer altered a normal response")
	}
}

func TestRefusalObserver_CatchesSignalSplitDeepIntoPreviousChunk(t *testing.T) {
	signal := `"stop_reason":"refusal"`
	split := len(signal) - 3
	rec := httptest.NewRecorder()
	obs := NewRefusalObserver(rec)
	if _, err := obs.Write([]byte(`data: {"type":"message_delta","delta":{` + strings.Repeat(" ", 256) + signal[:split])); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if obs.Refused {
		t.Fatal("refusal detected before the signal completed")
	}
	if _, err := obs.Write([]byte(signal[split:] + `}}`)); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if !obs.Refused {
		t.Fatal("refusal split across writes was not detected")
	}
}

func TestRefusalCategory_RejectsOverlongValue(t *testing.T) {
	if got := RefusalCategory([]byte(`"category":"` + strings.Repeat("a", 65) + `"`)); got != "" {
		t.Fatalf("refusalCategory accepted a %d-byte value", len(got))
	}
}

func TestRefusalObserver_CategoryBeforeOverlapWindowIsKept(t *testing.T) {
	obs := NewRefusalObserver(httptest.NewRecorder())
	if _, err := obs.Write([]byte(`data: {"category":"cyber"` + strings.Repeat(" ", 4*refusalScanOverlap))); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if _, err := obs.Write([]byte(`,"stop_reason":"refusal"}`)); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if !obs.Refused {
		t.Fatal("refusal not detected")
	}
	if obs.Category != "cyber" {
		t.Fatalf("category = %q, want cyber from the earlier chunk", obs.Category)
	}
}

// A realistic Anthropic-native refusal SSE (the router-visible wire shape:
// stop_reason "refusal" on HTTP 200; see catalog.go). The real opus cyber
// refusal also carries api_refusal_category "cyber" / "safeguards flagged".
const refusalSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-8","stop_reason":null}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}
`

const normalSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_2","model":"claude-opus-4-8"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}
`
