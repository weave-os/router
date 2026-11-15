package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/sse"
)

const ReasonCyberRefusalRetry = "cyber-refusal-retry"

const reasonCyberRefusalRepin = "cyber-refusal-repin"

const cyberRefusalHoldCap = 1 << 20

const cyberRefusalHoldCapRelease = "hold_cap"

var responsesPreambleEventTypes = map[string]struct{}{
	"response.created":     {},
	"response.queued":      {},
	"response.in_progress": {},
	"response.metadata":    {},
}

var responsesPreOutputFailureEventTypes = map[string]struct{}{
	"response.failed": {},
	"error":           {},
}

var responsesReasoningEventTypes = map[string]struct{}{
	"response.reasoning_summary_part.added": {},
	"response.reasoning_summary_part.done":  {},
	"response.reasoning_summary_text.delta": {},
	"response.reasoning_summary_text.done":  {},
	"response.reasoning_text.delta":         {},
	"response.reasoning_text.done":          {},
}

func responsesReasoningEvent(eventType string, payload []byte) bool {
	if _, reasoning := responsesReasoningEventTypes[eventType]; reasoning {
		return true
	}
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		return gjson.GetBytes(payload, "item.type").String() == "reasoning"
	}
	return false
}

type cyberRefusalGate struct {
	inner           http.ResponseWriter
	body            io.Writer
	held            bytes.Buffer
	holding         bool
	refused         bool
	withheld        bool
	holdFailure     bool
	failureWithheld bool
	scanned         int
	framing         sse.Scanner
	releaseEvent    string
	releaseItemType string
}

func newCyberRefusalGate(inner http.ResponseWriter, armed bool) *cyberRefusalGate {
	return &cyberRefusalGate{inner: inner, body: inner, holding: armed}
}

func (g *cyberRefusalGate) holdPreOutputFailure() *cyberRefusalGate {
	g.holdFailure, g.holding = true, true
	return g
}

func (g *cyberRefusalGate) Header() http.Header { return g.inner.Header() }

func (g *cyberRefusalGate) WriteHeader(status int) { g.inner.WriteHeader(status) }

func (g *cyberRefusalGate) Write(p []byte) (int, error) {
	if g.withheld || g.failureWithheld {
		return len(p), nil
	}
	if !g.holding {
		g.refused = g.refused || providers.ContainsCyberPolicyRefusal(p)
		return g.body.Write(p)
	}
	g.held.Write(p)
	refusal, failure, release := g.scanHeld()
	switch {
	case refusal:
		g.refused, g.withheld, g.holding = true, true, false
		g.discardHeld()
		return len(p), nil
	case failure:
		g.failureWithheld, g.holding = true, false
		g.discardHeld()
		return len(p), nil
	case release:
		g.holding = false
		return len(p), g.release()
	}
	return len(p), nil
}

func (g *cyberRefusalGate) scanHeld() (refusal, failure, release bool) {
	b := g.held.Bytes()[g.scanned:]
	for {
		event, n := g.framing.Next(b)
		if n == 0 {
			if g.held.Len() >= cyberRefusalHoldCap {
				g.releaseEvent = cyberRefusalHoldCapRelease
				return false, false, true
			}
			return false, false, false
		}
		b = b[n:]
		g.scanned += n
		if providers.ContainsCyberPolicyRefusal(event) {
			return true, false, false
		}
		_, payload := sse.ParseEvent(event)
		if len(payload) == 0 {
			continue
		}
		eventType := gjson.GetBytes(payload, "type").String()
		if _, failed := responsesPreOutputFailureEventTypes[eventType]; failed && g.holdFailure {
			return false, true, false
		}
		if _, preamble := responsesPreambleEventTypes[eventType]; preamble {
			continue
		}
		if g.holdFailure && responsesReasoningEvent(eventType, payload) {
			continue
		}
		g.releaseEvent = eventType
		g.releaseItemType = gjson.GetBytes(payload, "item.type").String()
		return false, false, true
	}
}

func (g *cyberRefusalGate) Flush() {
	if g.holding || g.withheld || g.failureWithheld {
		return
	}
	if f, ok := g.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *cyberRefusalGate) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := g.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

func (g *cyberRefusalGate) Finalize() error {
	if !g.holding {
		return nil
	}
	g.holding = false
	if providers.ContainsCyberPolicyRefusal(g.held.Bytes()) {
		g.refused, g.withheld = true, true
		g.discardHeld()
		return nil
	}
	return g.release()
}

func (g *cyberRefusalGate) finalizeAttempt(attemptErr error) error {
	if g.holdFailure && g.holding && attemptErr != nil && !providers.ContainsCyberPolicyRefusal(g.held.Bytes()) {
		g.failureWithheld = true
		g.Abandon()
		return nil
	}
	return g.Finalize()
}

func (g *cyberRefusalGate) Abandon() {
	g.holding = false
	g.discardHeld()
}

func (g *cyberRefusalGate) release() error {
	if g.held.Len() == 0 {
		return nil
	}
	out := g.held.Bytes()
	g.discardHeld()
	g.refused = g.refused || providers.ContainsCyberPolicyRefusal(out)
	_, err := g.body.Write(out)
	return err
}

func (g *cyberRefusalGate) discardHeld() {
	g.held.Reset()
	g.framing.Reset()
}

func (s *Service) cyberRefusalRetryTarget(
	ctx context.Context,
	failed router.Decision,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	est, sigSavings, outputReserve int,
) (router.Decision, bool) {
	repinTarget, _, ok := s.cyberRefusalFallback(ctx, sessionKey, role, failed, failed.Provider)
	if !ok {
		return router.Decision{}, false
	}
	for _, model := range []string{repinTarget, s.ResolveCyberRefusalFallbackModel(ctx)} {
		if model == "" || model == failed.Model ||
			!modelPermittedByAllowlist(ctx, model) || !modelInRequestSubset(ctx, model) {
			continue
		}
		target, found := s.rescueDecision(ctx, failed, []string{model}, ReasonCyberRefusalRetry, est, sigSavings, outputReserve)
		if found && target.Provider != providers.ProviderOpenAI {
			return target, true
		}
	}
	return router.Decision{}, false
}

func (s *Service) cyberRefusalFallback(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	served router.Decision,
	avoidProvider string,
) (model, provider string, ok bool) {
	model = s.ResolveCyberRefusalFallbackModel(ctx)
	if s.pinStore != nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		if existing, found, err := s.pinStore.Get(context.Background(), sessionKey, role); err == nil && found &&
			pinMatchesEffectiveStrategy(ctx, existing) && existing.PairedModel != "" &&
			!providerAvoided(providerForModel(existing.PairedProvider, existing.PairedModel), avoidProvider) {
			model, provider = existing.PairedModel, existing.PairedProvider
		}
	}
	provider = providerForModel(provider, model)
	if model == "" || provider == "" || model == served.Model || providerAvoided(provider, avoidProvider) {
		return "", "", false
	}
	return model, provider, true
}

func providerForModel(provider, model string) string {
	if provider != "" {
		return provider
	}
	if m, known := catalog.ByID(model); known && len(m.Providers) > 0 {
		return m.Providers[0].Provider
	}
	return ""
}

func providerAvoided(provider, avoidProvider string) bool {
	return avoidProvider != "" && provider == avoidProvider
}
