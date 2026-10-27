package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

// ReasonEarlyStopRedo marks a Claude Code tool-result turn re-served on a
// higher-tier candidate after the routed model ended the agent turn without
// calling a tool.
const ReasonEarlyStopRedo = "early-stop-redo"

const markerReasonEarlyStop = "re-served after an early stop"

// earlyStopHoldCap bounds the response bytes the gate withholds; a longer
// response is released and served as is.
const earlyStopHoldCap = 1 << 20

// earlyStopHoldMax bounds how long the gate withholds a response. Claude Code
// drops a stream after 180s without bytes, and a redo still has to stream
// inside that window. A timer enforces it, since an upstream that is only
// reasoning may write nothing the gate could check against.
const earlyStopHoldMax = 60 * time.Second

// earlyStopGate sits above preludeBuffer on a Claude Code tool-result turn and
// classifies the Anthropic stream the client would receive. Armed, it
// withholds the stream until the turn proves it calls a tool (released at the
// first tool_use block) or stops for another reason; a turn that ends on
// end_turn without one stays withheld, so the caller can re-serve it before
// the client has seen any of it. Shadow, it only observes.
type earlyStopGate struct {
	inner http.ResponseWriter
	// body is inner's write half, kept separate for the same CodeQL reason as
	// cyberRefusalGate.body.
	body io.Writer
	// mu serializes the attempt's writes with the hold timer's release.
	mu      sync.Mutex
	holdMax time.Duration
	timer   *time.Timer
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

func newEarlyStopGate(inner http.ResponseWriter) *earlyStopGate {
	return &earlyStopGate{inner: inner, body: inner, holdMax: earlyStopHoldMax}
}

// observe starts classifying the stream; withhold additionally holds it back.
func (g *earlyStopGate) observe(withhold bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = true
	g.armed = withhold
	g.holding = withhold
	g.resetAttemptLocked()
}

func (g *earlyStopGate) Header() http.Header { return g.inner.Header() }

func (g *earlyStopGate) WriteHeader(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.holding {
		g.status = status
	}
	g.inner.WriteHeader(status)
}

func (g *earlyStopGate) Write(p []byte) (int, error) {
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
func (g *earlyStopGate) classify(p []byte) {
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

// earlyStop reports a complete response that ended the turn without a tool
// call. Only a withheld one can still be re-served.
func (g *earlyStopGate) earlyStop() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopReason == stopReasonEndTurn && !g.toolUse
}

// withheld reports whether the client has seen none of the response.
func (g *earlyStopGate) withheld() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.holding
}

// releaseReason names what ended the withholding, or "" while withheld.
func (g *earlyStopGate) releaseReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releaseCause
}

// releaseOnTimeout gives up the redo once the hold has lasted holdMax.
func (g *earlyStopGate) releaseOnTimeout() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.holding {
		return
	}
	g.releaseCause = "hold_timeout"
	_ = g.release()
}

func (g *earlyStopGate) Flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.flushLocked()
}

func (g *earlyStopGate) flushLocked() {
	if g.holding {
		return
	}
	if f, ok := g.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the stall-watchdog hook; see cyberRefusalGate.
func (g *earlyStopGate) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := g.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// release delivers anything withheld and passes the rest through. Callers
// hold mu.
func (g *earlyStopGate) release() error {
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

func (g *earlyStopGate) stopTimer() {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// takeWithheld hands the withheld response to the caller and stops gating, so
// a re-serve streams straight through.
func (g *earlyStopGate) takeWithheld() (status int, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopTimer()
	status, body = g.status, slices.Clone(g.held.Bytes())
	g.armed, g.holding = false, false
	g.held.Reset()
	return status, body
}

// disarm releases anything still withheld and stops gating, for the rescues
// that run after the primary dispatch.
func (g *earlyStopGate) disarm() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = false
	return g.release()
}

// resetAttempt drops a failed attempt's withheld bytes and signals; wired as
// the prelude buffer's discard hook so retries start clean.
func (g *earlyStopGate) resetAttempt() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetAttemptLocked()
}

func (g *earlyStopGate) resetAttemptLocked() {
	g.stopTimer()
	g.holding = g.armed
	g.held.Reset()
	g.status = 0
	g.scan = g.scan[:0]
	g.framing.Reset()
	g.toolUse, g.stopReason, g.releaseCause = false, "", ""
}

// earlyStopRedoTarget picks the candidate a stopped tool-result turn is
// re-served on: the highest-tier model the policy scored for this turn that
// the request may reach, strictly above the served model's tier. ok is false
// when the served model is already the top of its candidates.
func (s *Service) earlyStopRedoTarget(ctx context.Context, served router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool) {
	servedTier := catalog.TierFor(served.Model)
	if served.Metadata == nil || servedTier == catalog.TierUnknown {
		return router.Decision{}, false
	}
	ordered := higherTierCandidates(servedTier, served.Metadata.CandidateModels, served.Metadata.CandidateScores)
	admitted := s.rescueDecisions(ctx, served, ordered, ReasonEarlyStopRedo, est, sigSavings, outputReserve)
	// The rescue walk reorders for provider diversity; the redo wants the
	// strongest admitted model, so pick in tier-and-score order.
	for _, model := range ordered {
		for _, target := range admitted {
			if target.Model == model {
				return target, true
			}
		}
	}
	return router.Decision{}, false
}

// higherTierCandidates orders the candidates above tier from the top tier
// down, best score first within a tier.
func higherTierCandidates(tier catalog.Tier, candidates []string, scores map[string]float32) []string {
	var ordered []string
	for _, model := range candidates {
		if catalog.TierFor(model) > tier && !slices.Contains(ordered, model) {
			ordered = append(ordered, model)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		ti, tj := catalog.TierFor(ordered[i]), catalog.TierFor(ordered[j])
		if ti != tj {
			return ti > tj
		}
		return scores[ordered[i]] > scores[ordered[j]]
	})
	return ordered
}

// lowerTierCandidates lists the candidates below the redo target's tier. They
// are kept out for the rest of the agent turn once a redo serves, so the turn
// cannot be redone twice.
func lowerTierCandidates(target string, candidates []string) []string {
	tier := catalog.TierFor(target)
	var lower []string
	for _, model := range candidates {
		if t := catalog.TierFor(model); t != catalog.TierUnknown && t < tier {
			lower = append(lower, model)
		}
	}
	return lower
}

// earlyStopRedoMode resolves the mode for this turn: off unless it is a
// streamed Claude Code main-thread tool-result turn the router chose the model
// for.
func (s *Service) earlyStopRedoMode(ctx context.Context, routeRes turnLoopResult, decision router.Decision, streaming, agentShadow bool) flags.EarlyStopRedoMode {
	mode := s.ResolveEarlyStopRedo(ctx)
	if mode == flags.EarlyStopRedoOff || !streaming || agentShadow ||
		ClientIdentityFrom(ctx).ClientApp != ClientAppClaudeCode ||
		routeRes.TurnType != turntype.ToolResult ||
		routeRes.HardPinned || routeRes.CallerModelPassthrough || routeRes.UsageBypass ||
		strings.HasPrefix(decision.Reason, translate.ReasonUserForceModel) {
		return flags.EarlyStopRedoOff
	}
	return mode
}

// earlyStopRedo carries what ProxyMessages lends a re-serve: the gate holding
// the stopped response, the closures that build and dispatch the candidate,
// and the served-attempt state the redo replaces (or restores on failure).
type earlyStopRedo struct {
	gate     *earlyStopGate
	prelude  *preludeBuffer
	header   http.Header
	log      *slog.Logger
	target   router.Decision
	build    func(target router.Decision) (dispatchAttempt, []catalog.ProviderBinding, string, effortResolution, context.Context, error)
	dispatch func(ctx context.Context, target router.Decision, bindings []catalog.ProviderBinding, attempt dispatchAttempt) (int, error)
	served   *earlyStopServed
}

// earlyStopServed points at ProxyMessages' record of the attempt that served.
type earlyStopServed struct {
	decision   *router.Decision
	bindings   *[]catalog.ProviderBinding
	marker     *string
	winnerIdx  *int
	extractor  **otel.UsageExtractor
	summary    *translate.ResponseSummary
	reqStats   *providers.RequestMutationStats
	fastServed *bool
	effort     *effortResolution
}

type earlyStopServedSnapshot struct {
	decision   router.Decision
	bindings   []catalog.ProviderBinding
	marker     string
	winnerIdx  int
	extractor  *otel.UsageExtractor
	summary    translate.ResponseSummary
	reqStats   providers.RequestMutationStats
	fastServed bool
	effort     effortResolution
}

func (p *earlyStopServed) save() earlyStopServedSnapshot {
	return earlyStopServedSnapshot{
		decision: *p.decision, bindings: *p.bindings, marker: *p.marker, winnerIdx: *p.winnerIdx,
		extractor: *p.extractor, summary: *p.summary, reqStats: *p.reqStats, fastServed: *p.fastServed, effort: *p.effort,
	}
}

func (p *earlyStopServed) restore(s earlyStopServedSnapshot) {
	*p.decision, *p.bindings, *p.marker, *p.winnerIdx = s.decision, s.bindings, s.marker, s.winnerIdx
	*p.extractor, *p.summary, *p.reqStats, *p.fastServed, *p.effort = s.extractor, s.summary, s.reqStats, s.fastServed, s.effort
}

// redoEarlyStop re-serves a withheld early stop on redo.target. If the candidate
// cannot be dispatched, nothing changes and the caller releases the stopped
// response. If the redo fails before output, the stopped response is
// delivered instead, so the client never loses a turn the router discarded.
func (s *Service) redoEarlyStop(ctx context.Context, redo earlyStopRedo) (redone bool, err error) {
	attempt, bindings, marker, effort, redoCtx, buildErr := redo.build(redo.target)
	if buildErr != nil || len(bindings) == 0 {
		redo.log.Warn("Early-stop redo: candidate is not dispatchable; serving the stopped response",
			"redo_model", redo.target.Model, "redo_provider", redo.target.Provider, "err", buildErr)
		return false, nil
	}
	stopped := redo.served.save()
	header := redo.header.Clone()
	status, body := redo.gate.takeWithheld()
	redo.prelude.Discard()
	*redo.served.summary = translate.ResponseSummary{}
	*redo.served.reqStats = providers.RequestMutationStats{}
	redo.log.Info("Early-stop redo: re-serving the turn on a higher-tier candidate",
		"stopped_model", stopped.decision.Model, "redo_model", redo.target.Model, "redo_provider", redo.target.Provider,
		"stopped_output_bytes", len(body))
	winnerIdx, err := redo.dispatch(redoCtx, redo.target, bindings, attempt)
	if err == nil || redo.prelude.Committed() {
		*redo.served.decision, *redo.served.bindings, *redo.served.marker = redo.target, bindings, marker
		*redo.served.winnerIdx, *redo.served.effort = winnerIdx, effort
		return err == nil, err
	}
	redo.log.Warn("Early-stop redo failed before output; serving the stopped response",
		"redo_model", redo.target.Model, "upstream_status", upstreamStatus(err), "err", err)
	redo.served.restore(stopped)
	redo.prelude.Discard()
	clear(redo.header)
	maps.Copy(redo.header, header)
	redo.prelude.Seal()
	if status != 0 {
		redo.prelude.WriteHeader(status)
	}
	if _, err := redo.gate.Write(body); err != nil {
		return false, err
	}
	redo.gate.Flush()
	return false, nil
}
