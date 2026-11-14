package dispatch

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/sse"
)

// earlyStopHoldCap bounds the response bytes the gate withholds; a longer
// response is released and served as is.
const earlyStopHoldCap = 1 << 20

// earlyStopHoldMax bounds how long the gate withholds a response. Claude Code
// drops a stream after 180s without bytes, and a redo still has to stream
// inside that window. A timer enforces it, since an upstream that is only
// reasoning may write nothing the gate could check against.
const earlyStopHoldMax = 60 * time.Second

// EarlyStopGate sits above PreludeBuffer on a Claude Code tool-result turn and
// classifies the Anthropic stream the client would receive. Armed, it
// withholds the stream until the turn proves it calls a tool (released at the
// first tool_use block) or stops for another reason; a turn that ends on
// end_turn without one stays withheld, so the caller can re-serve it before
// the client has seen any of it. Shadow, it only observes.
type EarlyStopGate struct {
	inner http.ResponseWriter
	// body is inner's write half. Forwarding through io.Writer keeps upstream
	// SSE bytes out of CodeQL's reflected-XSS sink model, which reads every
	// ResponseWriter.Write in a proxy chain as an HTML response.
	body io.Writer
	// mu serializes the attempt's writes with the hold timer's release.
	mu      sync.Mutex
	holdMax time.Duration
	timer   *time.Timer
	// log is the request logger; the hold timer has no ctx of its own.
	log *slog.Logger
	// active turns classification on; armed additionally withholds.
	active  bool
	armed   bool
	holding bool
	held    bytes.Buffer
	// status is the attempt's buffered HTTP status, restored with held bytes.
	status int

	// scan holds the unconsumed tail of the stream for frame parsing.
	scan    []byte
	framing sse.Scanner

	toolUse    bool
	stopReason string
	// releaseCause names what ended the withholding, for logs.
	releaseCause string
}

func NewEarlyStopGate(inner http.ResponseWriter, log *slog.Logger) *EarlyStopGate {
	return &EarlyStopGate{inner: inner, body: inner, holdMax: earlyStopHoldMax, log: log}
}

// Observe starts classifying the stream; withhold additionally holds it back.
func (g *EarlyStopGate) Observe(withhold bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = true
	g.armed = withhold
	g.holding = withhold
	g.resetAttemptLocked()
}

func (g *EarlyStopGate) Header() http.Header { return g.inner.Header() }

func (g *EarlyStopGate) WriteHeader(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.holding {
		g.status = status
	}
	g.inner.WriteHeader(status)
}

func (g *EarlyStopGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.classify(p)
	if !g.holding {
		return g.body.Write(p)
	}
	if g.held.Len() == 0 && g.timer == nil {
		g.timer = time.AfterFunc(g.holdMax, g.releaseOnTimeout)
	}
	g.held.Write(p)
	switch {
	case g.toolUse:
		g.releaseCause = "tool_use"
	case g.stopReason != "" && g.stopReason != stopReasonEndTurn:
		g.releaseCause = "stop_reason_" + g.stopReason
	case g.held.Len() >= earlyStopHoldCap:
		g.releaseCause = "hold_cap"
	default:
		return len(p), nil
	}
	return len(p), g.release()
}

const stopReasonEndTurn = "end_turn"

// classify records the tool-call and stop-reason signals of complete frames.
func (g *EarlyStopGate) classify(p []byte) {
	if !g.active || (g.toolUse && g.stopReason != "") {
		return
	}
	g.scan = append(g.scan, p...)
	consumed := 0
	for {
		event, n := g.framing.Next(g.scan[consumed:])
		if n == 0 {
			break
		}
		consumed += n
		_, payload := sse.ParseEvent(event)
		if len(payload) == 0 {
			continue
		}
		switch gjson.GetBytes(payload, "type").String() {
		case "content_block_start":
			if blockType := gjson.GetBytes(payload, "content_block.type").String(); blockType == "tool_use" || blockType == "server_tool_use" {
				g.toolUse = true
			}
		case "message_delta":
			if reason := gjson.GetBytes(payload, "delta.stop_reason").String(); reason != "" {
				g.stopReason = reason
			}
		}
	}
	if consumed > 0 {
		g.scan = append(g.scan[:0], g.scan[consumed:]...)
		g.framing.Reset()
	}
}

// EarlyStop reports a complete response that ended the turn without a tool
// call. Only a withheld one can still be re-served.
func (g *EarlyStopGate) EarlyStop() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopReason == stopReasonEndTurn && !g.toolUse
}

// Withheld reports whether the client has seen none of the response.
func (g *EarlyStopGate) Withheld() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.holding
}

// ReleaseReason names what ended the withholding, or "" while withheld.
func (g *EarlyStopGate) ReleaseReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releaseCause
}

// releaseOnTimeout gives up the redo once the hold has lasted holdMax.
func (g *EarlyStopGate) releaseOnTimeout() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.holding {
		return
	}
	g.releaseCause = "hold_timeout"
	if err := g.release(); err != nil {
		g.log.Warn("Early-stop gate failed to release a withheld turn on timeout", "err", err)
	}
}

func (g *EarlyStopGate) Flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.flushLocked()
}

func (g *EarlyStopGate) flushLocked() {
	if g.holding {
		return
	}
	if f, ok := g.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the stall-watchdog hook to the writer below, so
// withheld bytes do not count as client-visible progress.
func (g *EarlyStopGate) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := g.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// release delivers anything withheld and passes the rest through. Callers
// hold mu.
func (g *EarlyStopGate) release() error {
	g.stopTimer()
	g.holding = false
	if g.held.Len() == 0 {
		return nil
	}
	out := slices.Clone(g.held.Bytes())
	g.held.Reset()
	_, err := g.body.Write(out)
	if err == nil {
		g.flushLocked()
	}
	return err
}

func (g *EarlyStopGate) stopTimer() {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// TakeWithheld hands the withheld response to the caller and stops gating, so
// a re-serve streams straight through.
func (g *EarlyStopGate) TakeWithheld() (status int, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopTimer()
	status, body = g.status, slices.Clone(g.held.Bytes())
	g.armed, g.holding = false, false
	g.held.Reset()
	return status, body
}

// Disarm releases anything still withheld and stops gating, for the rescues
// that run after the primary dispatch.
func (g *EarlyStopGate) Disarm() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = false
	return g.release()
}

// ResetAttempt drops a failed attempt's withheld bytes and signals; wired as
// the prelude buffer's discard hook so retries start clean.
func (g *EarlyStopGate) ResetAttempt() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetAttemptLocked()
}

func (g *EarlyStopGate) resetAttemptLocked() {
	g.stopTimer()
	g.holding = g.armed
	g.held.Reset()
	g.status = 0
	g.scan = g.scan[:0]
	g.framing.Reset()
	g.toolUse, g.stopReason, g.releaseCause = false, "", ""
}
