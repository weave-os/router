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

const earlyStopHoldCap = 1 << 20

const earlyStopHoldMax = 60 * time.Second

type EarlyStopGate struct {
	inner   http.ResponseWriter
	body    io.Writer
	mu      sync.Mutex
	holdMax time.Duration
	timer   *time.Timer
	log     *slog.Logger
	active  bool
	armed   bool
	holding bool
	held    bytes.Buffer
	status  int

	scan    []byte
	framing sse.Scanner

	toolUse      bool
	stopReason   string
	releaseCause string
}

func NewEarlyStopGate(inner http.ResponseWriter, log *slog.Logger) *EarlyStopGate {
	return &EarlyStopGate{inner: inner, body: inner, holdMax: earlyStopHoldMax, log: log}
}

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

func (g *EarlyStopGate) EarlyStop() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopReason == stopReasonEndTurn && !g.toolUse
}

func (g *EarlyStopGate) Withheld() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.holding
}

func (g *EarlyStopGate) ReleaseReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releaseCause
}

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

func (g *EarlyStopGate) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := g.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

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

func (g *EarlyStopGate) TakeWithheld() (status int, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopTimer()
	status, body = g.status, slices.Clone(g.held.Bytes())
	g.armed, g.holding = false, false
	g.held.Reset()
	return status, body
}

func (g *EarlyStopGate) Disarm() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = false
	return g.release()
}

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
