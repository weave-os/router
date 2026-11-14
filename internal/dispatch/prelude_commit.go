package dispatch

import (
	"bytes"
	"net/http"
	"sync/atomic"

	"weave-os/router/internal/sse"
)

// PreludeBuffer absorbs pre-upstream writes (the eager SSE Prelude) so
// per-request failover can retry on another binding without the client
// having seen bytes from a primary that ultimately failed.
//
// Flow: Seal() ends the buffering phase; the first Write/WriteHeader after
// that calls commit(), flushing the buffered status+body to inner and going
// pass-through. If the attempt errors before ever writing, Committed() is
// false and the caller can Discard() and retry or render the error itself.
// Committed() is the retry gate (replaces firstByteGuard.written).
type PreludeBuffer struct {
	inner          http.ResponseWriter
	initialHeaders http.Header
	bufStatus      int
	bufBody        bytes.Buffer
	sealed         bool
	preludeSent    bool
	committed      atomic.Bool
	// OnDiscard lets a writer holding output above this buffer drop it with
	// the failed attempt, so a retry never appends to a stale response.
	OnDiscard func()
}

func NewPreludeBuffer(w http.ResponseWriter) *PreludeBuffer {
	// Snapshot headers so Discard() can restore them; Prelude only
	// Set/Del's, never appends, so sharing the inner []string is safe.
	snap := make(http.Header, len(w.Header()))
	for k, vs := range w.Header() {
		cp := make([]string, len(vs))
		copy(cp, vs)
		snap[k] = cp
	}
	return &PreludeBuffer{inner: w, initialHeaders: snap}
}

func (b *PreludeBuffer) Header() http.Header { return b.inner.Header() }

func (b *PreludeBuffer) Write(p []byte) (int, error) {
	if b.committed.Load() {
		return sse.WriteJSONChunk(b.inner, p)
	}
	if b.sealed {
		// Post-Seal first write: commit buffered prelude, then pass through.
		if err := b.commit(); err != nil {
			return 0, err
		}
		return sse.WriteJSONChunk(b.inner, p)
	}
	// Pre-Seal: buffer.
	return b.bufBody.Write(p)
}

func (b *PreludeBuffer) WriteHeader(status int) {
	if b.committed.Load() {
		sse.PrepareJSONResponse(b.inner)
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

func (b *PreludeBuffer) Flush() {
	if !b.preludeSent && !b.committed.Load() {
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
func (b *PreludeBuffer) Seal() { b.sealed = true }

// PreludeSent reports whether the buffered prelude is already client-visible.
func (b *PreludeBuffer) PreludeSent() bool { return b.preludeSent }

// CommitPrelude makes the buffered prelude client-visible without marking the
// attempt committed. A later provider error can still be retried or fail over.
func (b *PreludeBuffer) CommitPrelude() error {
	if !b.preludeSent && b.bufStatus != 0 {
		sse.PrepareJSONResponse(b.inner)
		b.inner.WriteHeader(b.bufStatus)
	}
	if b.bufBody.Len() > 0 {
		if _, err := sse.WriteJSONChunk(b.inner, b.bufBody.Bytes()); err != nil {
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
func (b *PreludeBuffer) Committed() bool { return b.committed.Load() }

// Discard resets buffered Prelude bytes and headers to the construction-time
// snapshot. No-op once committed. Called between failed attempts and before
// the exhaustion error renderer writes via the unwrapped inner writer.
func (b *PreludeBuffer) Discard() {
	if b.committed.Load() {
		return
	}
	if b.OnDiscard != nil {
		b.OnDiscard()
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

func (b *PreludeBuffer) commit() error {
	if b.committed.Swap(true) {
		return nil
	}
	if !b.preludeSent {
		if b.bufStatus != 0 {
			sse.PrepareJSONResponse(b.inner)
			b.inner.WriteHeader(b.bufStatus)
		}
		if b.bufBody.Len() > 0 {
			if _, err := sse.WriteJSONChunk(b.inner, b.bufBody.Bytes()); err != nil {
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
