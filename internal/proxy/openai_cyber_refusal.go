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

// ReasonCyberRefusalRetry marks a turn re-dispatched to a non-OpenAI model
// after OpenAI's cybersecurity classifier declined it.
const ReasonCyberRefusalRetry = "cyber-refusal-retry"

// reasonCyberRefusalRepin is the pin reason recorded when a safety refusal
// moves the session off the refusing model.
const reasonCyberRefusalRepin = "cyber-refusal-repin"

// cyberRefusalHoldCap bounds the memory the withheld preamble may occupy.
// /v1/responses echoes the request's instructions and tools in
// response.created, so a Codex preamble alone runs to hundreds of kilobytes;
// the cap only decides when to stop buffering and commit the stream.
const cyberRefusalHoldCap = 1 << 20

// cyberRefusalHoldCapRelease is the release event recorded when the hold cap,
// not a frame, ended the withholding.
const cyberRefusalHoldCapRelease = "hold_cap"

// responsesPreambleEventTypes are the /v1/responses events that precede any
// output. While the stream carries only these, nothing is committed yet, so a
// refusal can still be withheld and the turn re-dispatched elsewhere.
var responsesPreambleEventTypes = map[string]struct{}{
	"response.created":     {},
	"response.queued":      {},
	"response.in_progress": {},
}

// responsesPreOutputFailureEventTypes end a /v1/responses stream without
// output. The ChatGPT backend reports overload this way inside an HTTP 200,
// and the provider client already returns it as a retryable upstream error.
var responsesPreOutputFailureEventTypes = map[string]struct{}{
	"response.failed": {},
	"error":           {},
}

// responsesReasoningEventTypes carry reasoning, which a failed attempt can drop
// without contradicting a rescue: the client keeps nothing of it.
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

// cyberRefusalGate tees an OpenAI response to inner while classifying it. When
// armed it withholds the preamble so a refusal arriving before any output can be
// swallowed and the turn re-dispatched; the bytes are released as soon as the
// stream proves it carries output. Unarmed it only observes, leaving today's
// pass-through behavior byte-for-byte intact.
type cyberRefusalGate struct {
	inner http.ResponseWriter
	// body is inner's write half. Forwarding through io.Writer keeps upstream
	// SSE bytes out of CodeQL's reflected-XSS sink model, which reads every
	// ResponseWriter.Write in a proxy chain as an HTML response.
	body io.Writer
	// held buffers the withheld preamble; empty once released.
	held bytes.Buffer
	// holding is true while bytes are withheld from inner.
	holding bool
	// refused records a cyber-policy refusal anywhere in the response.
	refused bool
	// withheld is true when the refusal was classified before any byte
	// reached inner, so the turn is still rescuable.
	withheld bool
	// holdFailure extends the withholding to a pre-output failure frame, for
	// an attempt another dispatch can still serve.
	holdFailure bool
	// failureWithheld is true when a failure frame ended the stream before any
	// byte reached inner. The attempt's own error reports the failure.
	failureWithheld bool
	// scanned counts the held bytes already classified, so a stream arriving in
	// many writes is scanned once end to end.
	scanned int
	// framing resumes the delimiter search inside held[scanned:] across writes;
	// it is rewound whenever held is discarded.
	framing sse.Scanner
	// releaseEvent and releaseItemType name what ended the withholding, so a
	// failure after release can be traced to the frame that committed the turn.
	releaseEvent    string
	releaseItemType string
}

// newCyberRefusalGate wraps w. When armed the gate withholds the stream
// preamble; otherwise it observes only.
func newCyberRefusalGate(inner http.ResponseWriter, armed bool) *cyberRefusalGate {
	return &cyberRefusalGate{inner: inner, body: inner, holding: armed}
}

// holdPreOutputFailure also withholds the preamble of a stream that fails
// before output, so the failed attempt leaves nothing on the wire for a rescue
// to contradict.
func (g *cyberRefusalGate) holdPreOutputFailure() *cyberRefusalGate {
	g.holdFailure, g.holding = true, true
	return g
}

func (g *cyberRefusalGate) Header() http.Header { return g.inner.Header() }

func (g *cyberRefusalGate) WriteHeader(status int) { g.inner.WriteHeader(status) }

func (g *cyberRefusalGate) Write(p []byte) (int, error) {
	if g.withheld || g.failureWithheld {
		// The refusal decided the attempt; trailing frames belong to a
		// response that is never delivered.
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

// scanHeld classifies the withheld bytes frame by frame, in order: a refusal
// frame reached before any output frame is rescuable, whereas an output frame
// ahead of it means the turn is already being served and must be delivered.
// Chat/completions chunks carry no Responses event type and so read as output.
func (g *cyberRefusalGate) scanHeld() (refusal, failure, release bool) {
	b := g.held.Bytes()[g.scanned:]
	for {
		event, n := g.framing.Next(b)
		if n == 0 {
			// Hold for the rest of an incomplete frame, unless buffering it would
			// outgrow the cap.
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

// Flush holds back while the preamble is withheld: flushing it would defeat the
// withholding it exists to enable.
func (g *cyberRefusalGate) Flush() {
	if g.holding || g.withheld || g.failureWithheld {
		return
	}
	if f, ok := g.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the stall-watchdog hook; a withheld preamble
// carries no output frame, so hiding it would disarm the watchdog.
func (g *cyberRefusalGate) ArmOutputProgress(mark func()) (armed bool) {
	arm, ok := g.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// Finalize releases anything still withheld. A non-streaming body completes no
// SSE frame, so this is where it is classified and passed on.
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

// finalizeAttempt is Finalize for an attempt that returned attemptErr. With the
// failure hold on, a failed attempt drops a still-withheld preamble even when
// no failure frame arrived (a cut stream, an idle timeout): released, the
// preamble would commit the client to a response the rescue then cannot replace.
func (g *cyberRefusalGate) finalizeAttempt(attemptErr error) error {
	if g.holdFailure && g.holding && attemptErr != nil && !providers.ContainsCyberPolicyRefusal(g.held.Bytes()) {
		g.failureWithheld = true
		g.Abandon()
		return nil
	}
	return g.Finalize()
}

// Abandon drops anything still withheld without delivering it, for an attempt
// that will be retried: released, the preamble would commit the client to it.
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
	// The released bytes may carry a refusal that trails output — unrescuable,
	// but still the signal that re-pins the session.
	g.refused = g.refused || providers.ContainsCyberPolicyRefusal(out)
	_, err := g.body.Write(out)
	return err
}

// discardHeld drops the withheld bytes together with the framing cursor that
// pointed into them.
func (g *cyberRefusalGate) discardHeld() {
	g.held.Reset()
	g.framing.Reset()
}

// cyberRefusalRetryTarget resolves the model a refused turn is re-dispatched to:
// the same choice repinOffRefusingModel makes (the pin's runner-up, else the
// configured fallback), admitted only if the shared rescue rules and the org's
// allowlist let this request reach it, and only off the refusing vendor.
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
	// The pin's runner-up can itself be an OpenAI model, which this rescue cannot
	// use; the configured fallback backs it up.
	for _, model := range []string{repinTarget, s.ResolveCyberRefusalFallbackModel(ctx)} {
		if model == "" || model == failed.Model ||
			!modelPermittedByAllowlist(ctx, model) || !modelInRequestSubset(ctx, model) {
			continue
		}
		target, found := s.rescueDecision(ctx, failed, []string{model}, ReasonCyberRefusalRetry, est, sigSavings, outputReserve)
		// A rescue on the refusing vendor would meet the same classifier.
		if found && target.Provider != providers.ProviderOpenAI {
			return target, true
		}
	}
	return router.Decision{}, false
}

// cyberRefusalFallback resolves the model/provider a safety refusal moves off
// to: the scorer's runner-up carried on the session pin, else the configured
// fallback model. avoidProvider, when set, rules out a target on that vendor —
// OpenAI's classifier declines the request whichever of its models serves it,
// so the pin's runner-up is no escape if it is another OpenAI model. Anthropic
// refusals name a single model and pass "". context.Background() reads the pin
// because the request ctx may already be canceled once the response has been
// written.
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

// providerForModel keeps a pin's own provider and otherwise reads the model's
// first catalog binding.
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
