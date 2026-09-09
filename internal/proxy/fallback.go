package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

// preludeBuffer absorbs pre-upstream writes (the eager SSE Prelude) so
// per-request failover can retry on another binding without the client
// having seen bytes from a primary that ultimately failed.
//
// Flow: Seal() ends the buffering phase; the first Write/WriteHeader after
// that calls commit(), flushing the buffered status+body to inner and going
// pass-through. If the attempt errors before ever writing, Committed() is
// false and the caller can Discard() and retry or render the error itself.
// Committed() is the retry gate (replaces firstByteGuard.written).
type preludeBuffer struct {
	inner          http.ResponseWriter
	initialHeaders http.Header
	bufStatus      int
	bufBody        bytes.Buffer
	sealed         bool
	preludeSent    bool
	committed      bool
}

func newPreludeBuffer(w http.ResponseWriter) *preludeBuffer {
	// Snapshot headers so Discard() can restore them; Prelude only
	// Set/Del's, never appends, so sharing the inner []string is safe.
	snap := make(http.Header, len(w.Header()))
	for k, vs := range w.Header() {
		cp := make([]string, len(vs))
		copy(cp, vs)
		snap[k] = cp
	}
	return &preludeBuffer{inner: w, initialHeaders: snap}
}

func (b *preludeBuffer) Header() http.Header { return b.inner.Header() }

func (b *preludeBuffer) Write(p []byte) (int, error) {
	if b.committed {
		return b.inner.Write(p)
	}
	if b.sealed {
		// Post-Seal first write: commit buffered prelude, then pass through.
		if err := b.commit(); err != nil {
			return 0, err
		}
		return b.inner.Write(p)
	}
	// Pre-Seal: buffer.
	return b.bufBody.Write(p)
}

func (b *preludeBuffer) WriteHeader(status int) {
	if b.committed {
		b.inner.WriteHeader(status)
		return
	}
	if b.sealed {
		// Buffer success headers so an empty or retryable stream can still
		// avoid committing a downstream 200 before upstream output arrives.
		b.bufStatus = status
		return
	}
	b.bufStatus = status
}

func (b *preludeBuffer) Flush() {
	if !b.preludeSent && !b.committed {
		// Pre-commit Flush is a no-op — we don't want partial Prelude bytes
		// reaching the client before commit decides.
		return
	}
	if f, ok := b.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// Seal marks the end of the Prelude phase. The next Write or WriteHeader
// will trigger commit().
func (b *preludeBuffer) Seal() { b.sealed = true }

// PreludeSent reports whether the buffered prelude is already client-visible.
func (b *preludeBuffer) PreludeSent() bool { return b.preludeSent }

// CommitPrelude makes the buffered prelude client-visible without marking the
// attempt committed. A later provider error can still be retried or fail over.
func (b *preludeBuffer) CommitPrelude() error {
	if !b.preludeSent && b.bufStatus != 0 {
		b.inner.WriteHeader(b.bufStatus)
	}
	if b.bufBody.Len() > 0 {
		if _, err := b.inner.Write(b.bufBody.Bytes()); err != nil {
			return err
		}
	}
	b.bufStatus = 0
	b.bufBody.Reset()
	b.preludeSent = true
	if f, ok := b.inner.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// Committed reports whether provider output has reached the inner writer.
func (b *preludeBuffer) Committed() bool { return b.committed }

// Discard resets buffered Prelude bytes and headers to the construction-time
// snapshot. No-op once committed. Called between failed attempts and before
// the exhaustion error renderer writes via the unwrapped inner writer.
func (b *preludeBuffer) Discard() {
	if b.committed {
		return
	}
	b.bufStatus = 0
	b.bufBody.Reset()
	b.sealed = false
	if b.preludeSent {
		return
	}
	h := b.inner.Header()
	for k := range h {
		delete(h, k)
	}
	for k, vs := range b.initialHeaders {
		cp := make([]string, len(vs))
		copy(cp, vs)
		h[k] = cp
	}
}

func (b *preludeBuffer) commit() error {
	if b.committed {
		return nil
	}
	b.committed = true
	if !b.preludeSent {
		if b.bufStatus != 0 {
			b.inner.WriteHeader(b.bufStatus)
		}
		if b.bufBody.Len() > 0 {
			if _, err := b.inner.Write(b.bufBody.Bytes()); err != nil {
				return err
			}
		}
		b.preludeSent = true
	}
	b.bufStatus = 0
	b.bufBody.Reset()
	if f, ok := b.inner.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// dispatchAttempt does one per-binding dispatch. Returns the upstream error
// unmodified so dispatchWithFallback can decide whether to retry.
type dispatchAttempt func(ctx context.Context, decision router.Decision, p providers.Client) error

// failoverInputs bundles the inputs dispatchWithFallback needs that don't
// belong to a single attempt.
type failoverInputs struct {
	// w is the real client writer; flushErr writes here directly (bypassing
	// buf) so the client sees the upstream error in its expected format.
	w http.ResponseWriter
	// buf is the writer per-attempt code writes through; its Committed()
	// bit gates retry. nil when failover is impossible (single binding,
	// BYOK, legacy mode) — the loop then skips discard/commit entirely.
	buf *preludeBuffer
	// initialDecision carries the model + cluster metadata from the
	// router. dispatchWithFallback rewrites Provider per-attempt.
	initialDecision router.Decision
	// bindings is the ordered (provider, upstream-id, price) list, filtered
	// to providers wired in this deploy. Index 0 is primary, >0 fallbacks.
	bindings []catalog.ProviderBinding
	// attempt does one per-binding dispatch.
	attempt dispatchAttempt
	// flushErr renders the final-attempt upstream error to w in the
	// entry point's wire format. Optional — nil means do nothing on
	// exhaustion (the error is still returned to the caller).
	flushErr func(w http.ResponseWriter, err error)
	// deferFlushOnExhaustion suppresses flushErr on exhaustion (still
	// discards the buffer and returns the error), letting the caller run a
	// higher-level fallback instead (e.g. ProxyMessages' baseline failover
	// re-dispatching the Anthropic model when a routed OSS model exhausts).
	deferFlushOnExhaustion bool
	// purpose names the registered operation the walk is authorized under;
	// origin names the override source that fixed the decision's model.
	purpose inference.Purpose
	origin  policy.OverrideSource
}

// errDispatchWithoutPurpose rejects a walk no registered purpose authorizes:
// every dispatch must resolve a plan before bytes reach a provider.
var errDispatchWithoutPurpose = errors.New("dispatchWithFallback: no inference purpose")

// dispatchWithFallback authorizes the routed decision under in.purpose and
// runs the attempt closure against each binding in order through the
// dispatch executor, retrying on providers.IsRetryable errors while no bytes
// have reached the client. Returns the winning (or last-tried) index and
// error. On final-attempt error it flushes the upstream's own envelope to w
// instead of a generic 502.
func (s *Service) dispatchWithFallback(ctx context.Context, in failoverInputs) (winnerIdx int, err error) {
	if len(in.purpose) == 0 {
		return -1, errDispatchWithoutPurpose
	}
	if len(in.bindings) == 0 {
		if in.initialDecision.Reason == translate.ReasonUserForceModel {
			err := &providers.UpstreamErrorResponse{
				Status: http.StatusServiceUnavailable,
				Body: []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"api_error"}}`,
					fmt.Sprintf("forced model %s unavailable: provider %s not configured", in.initialDecision.Model, in.initialDecision.Provider))),
			}
			if in.flushErr != nil && !in.deferFlushOnExhaustion {
				in.flushErr(in.w, err)
			}
			return -1, err
		}
		return -1, &providers.UpstreamStatusError{Status: http.StatusBadGateway}
	}
	plans, err := s.inferencePlans()
	if err != nil {
		return -1, err
	}
	plan, err := plans.ResolveRouted(policy.RoutedResolutionRequest{
		Purpose:  policy.Purpose(in.purpose),
		Decision: in.initialDecision,
		Bindings: in.bindings,
		Origin:   in.origin,
	})
	if err != nil {
		return -1, err
	}
	return s.dispatchPlanned(ctx, in, plan)
}

// committed is a nil-safe shorthand for in.buf.Committed(); the
// single-binding fast path passes buf=nil and is treated as not committed.
func committed(b *preludeBuffer) bool {
	if b == nil {
		return false
	}
	return b.Committed()
}

// sameBindingRetryBudget caps wall-clock across managed-subscription account
// rotations on one binding; per-target transient retries are bounded by the
// dispatch executor.
const sameBindingRetryBudget = 10 * time.Second

// clockNow reads the current time through the injectable clock, falling back
// to time.Now when no fake is wired.
func (s *Service) clockNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// shouldFailover reports failover eligibility. Customer-supplied
// credentials (BYOK or client key) bind to a single provider — retrying
// elsewhere would 401 unexpectedly, so we skip failover. A BYOK row for ANY
// provider disables it too: a failover binding re-resolves credentials and
// would prefer that key even if the primary binding used the deployment key.
func (s *Service) shouldFailover(ctx context.Context) bool {
	if s.byokOnly {
		return false
	}
	if CredentialsFromContext(ctx) != nil {
		return false
	}
	if len(externalKeysFromContext(ctx)) > 0 {
		return false
	}
	return true
}

// resolveBindingsForDispatch returns the ordered binding list to walk.
// When failover is disabled or unavailable, returns a single-element
// slice carrying the already-resolved decision provider.
func (s *Service) resolveBindingsForDispatch(ctx context.Context, decision router.Decision) []catalog.ProviderBinding {
	primary := catalog.ProviderBinding{Provider: decision.Provider}
	if !s.shouldFailover(ctx) {
		return []catalog.ProviderBinding{primary}
	}
	available := s.deploymentKeyedProviders
	if available == nil {
		// Legacy "all registered" mode — fall back to single-attempt to
		// avoid retrying on providers whose keys aren't actually wired.
		return []catalog.ProviderBinding{primary}
	}
	// Exclusions must hold during failover too, or a fallback binding could
	// resurrect a provider the scorer already filtered out.
	excluded := s.excludedProvidersForRequest(ctx)
	if len(excluded) > 0 {
		filtered := make(map[string]struct{}, len(available))
		for p := range available {
			if _, drop := excluded[p]; !drop {
				filtered[p] = struct{}{}
			}
		}
		available = filtered
	}
	_, primaryExcluded := excluded[decision.Provider]
	indexedBindings := catalog.EnumerateBindings(decision.Model, available)
	bindings := make([]catalog.ProviderBinding, 0, len(indexedBindings))
	for _, binding := range indexedBindings {
		bindings = append(bindings, binding.ProviderBinding)
	}
	if len(bindings) == 0 {
		if primaryExcluded {
			// Decision names an excluded provider with no other bindings (a
			// bug upstream) — return nil so dispatch 502s instead of
			// dispatching to the forbidden provider.
			return nil
		}
		if decision.Reason == translate.ReasonUserForceModel {
			if _, known := catalog.ByID(decision.Model); known {
				return nil
			}
		}
		return []catalog.ProviderBinding{primary}
	}
	if decision.Metadata != nil && decision.Metadata.SelectedArmID != "" {
		bindings, found := prioritizeSelectedArmBinding(
			indexedBindings,
			decision.Model,
			decision.Provider,
			decision.Metadata.SelectedUpstreamID,
			decision.Metadata.BindingIndex,
		)
		if found {
			return bindings
		}
	}
	if len(bindings) == 1 && !primaryExcluded {
		return []catalog.ProviderBinding{primary}
	}
	// Defensive: if the runtime decision's provider differs from
	// bindings[0], keep the decision's as primary and dedupe the rest —
	// unless it's excluded, in which case serve only eligible bindings.
	if bindings[0].Provider != decision.Provider {
		if primaryExcluded {
			return bindings
		}
		out := []catalog.ProviderBinding{primary}
		for _, b := range bindings {
			if b.Provider != decision.Provider {
				out = append(out, b)
			}
		}
		return out
	}
	return bindings
}

func prioritizeSelectedArmBinding(
	bindings []catalog.IndexedBinding,
	catalogID string,
	provider string,
	upstreamID string,
	bindingIndex int,
) ([]catalog.ProviderBinding, bool) {
	ordered := make([]catalog.ProviderBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Index != bindingIndex || binding.Provider != provider {
			continue
		}
		if catalog.UpstreamIDFor(catalogID, binding.UpstreamID) != upstreamID {
			return nil, false
		}
		ordered = append(ordered, binding.ProviderBinding)
		break
	}
	if len(ordered) == 0 {
		return nil, false
	}
	for _, binding := range bindings {
		if binding.Index != bindingIndex {
			ordered = append(ordered, binding.ProviderBinding)
		}
	}
	return ordered, true
}

// flushBufferedIfPresent writes an *UpstreamErrorResponse through to the
// client verbatim. No-op for other error types.
//
// Content-Length/Encoding are dropped: MaxBufferedErrorBytes caps the body
// we hold, so the upstream's advertised length may exceed what we actually
// write, breaking HTTP framing. net/http recomputes Content-Length itself.
func flushBufferedIfPresent(w http.ResponseWriter, err error) {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) {
		return
	}
	for k, vs := range resp.Headers {
		canon := http.CanonicalHeaderKey(k)
		if _, hop := providers.HopByHopHeaders[canon]; hop {
			continue
		}
		if canon == "Content-Length" || canon == "Content-Encoding" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// emitAnthropicSSEErrorEvent writes an Anthropic-shape `event: error` SSE
// frame and returns *UpstreamStatusError so flushErr becomes a no-op.
// Used when the upstream errors after Prelude already committed HTTP 200 +
// message_start — a JSON error envelope would corrupt the SSE stream, but
// an `event: error` frame terminates cleanly.
func emitAnthropicSSEErrorEvent(sink http.ResponseWriter, err error) error {
	var resp *providers.UpstreamErrorResponse
	status := http.StatusBadGateway
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"upstream stream failed"}}`)
	switch cls, classified := ClassifyDispatchError(err); {
	case errors.As(err, &resp):
		status = resp.Status
		body = translate.OpenAIToAnthropicError(resp.Body)
	case classified:
		status = cls.Status
		body = anthropicErrorFrameBody(cls)
	}
	_, _ = sink.Write([]byte("event: error\ndata: "))
	_, _ = sink.Write(body)
	_, _ = sink.Write([]byte("\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
	return &providers.UpstreamStatusError{Status: status}
}

// emitOpenAISSEErrorEvent is emitAnthropicSSEErrorEvent's OpenAI-shape
// counterpart: writes a verbatim `data: {...}` frame, used after
// OpenAIRoutingMarkerWriter has already committed HTTP 200.
func emitOpenAISSEErrorEvent(sink http.ResponseWriter, err error) error {
	var resp *providers.UpstreamErrorResponse
	status := http.StatusBadGateway
	body := []byte(`{"error":{"message":"upstream stream failed","type":"server_error","code":"upstream_error"}}`)
	switch cls, classified := ClassifyDispatchError(err); {
	case errors.As(err, &resp):
		status = resp.Status
		body = resp.Body
	case classified:
		status = cls.Status
		body = openAIErrorFrameBody(cls)
	}
	_, _ = sink.Write([]byte("data: "))
	_, _ = sink.Write(body)
	_, _ = sink.Write([]byte("\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
	return &providers.UpstreamStatusError{Status: status}
}

// anthropicErrorFrameBody renders a classified dispatch error as an
// Anthropic error body. The ingress handler cannot write its own envelope
// once the stream is committed, so the frame carries the class's own message
// rather than the generic upstream-failure text.
func anthropicErrorFrameBody(cls DispatchErrorClass) []byte {
	return []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`,
		errorFrameType(cls), cls.Message))
}

// openAIErrorFrameBody is anthropicErrorFrameBody's OpenAI-shape counterpart.
func openAIErrorFrameBody(cls DispatchErrorClass) []byte {
	return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":%q,"code":"upstream_error"}}`,
		cls.Message, errorFrameType(cls)))
}

func errorFrameType(cls DispatchErrorClass) string {
	if cls.Kind.IsClientError() {
		return "invalid_request_error"
	}
	return "api_error"
}

// emitGeminiSSEErrorEvent writes a terminal error frame to a committed
// Gemini stream; called only on post-commit transport or truncation failure.
func emitGeminiSSEErrorEvent(sink http.ResponseWriter) {
	_, _ = sink.Write([]byte("data: {\"error\":{\"code\":\"UNAVAILABLE\",\"message\":\"upstream stream failed\"}}\n\n"))
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
}

// flushUpstreamErrorAsAnthropic is ProxyMessages' flushErr: translates the
// OpenAI-compat upstream's error body to Anthropic-shape JSON. No-op when
// err is not an *UpstreamErrorResponse.
func flushUpstreamErrorAsAnthropic(w http.ResponseWriter, err error) {
	var resp *providers.UpstreamErrorResponse
	if !errors.As(err, &resp) {
		return
	}
	for k, vs := range resp.Headers {
		canon := http.CanonicalHeaderKey(k)
		if _, hop := providers.HopByHopHeaders[canon]; hop {
			continue
		}
		if canon == "Content-Type" || canon == "Content-Length" {
			continue // body is rewritten below
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(translate.OpenAIToAnthropicError(resp.Body))
}

// attemptIdxLabel formats i for the x-router-fallback-attempt header.
// Caller gates on i > 0.
func attemptIdxLabel(i int) string {
	return strconv.Itoa(i)
}
