package dispatch

import (
	"bytes"
	"net/http"
	"sync/atomic"

	"weave-os/router/internal/sse"
)

type PreludeBuffer struct {
	inner          http.ResponseWriter
	initialHeaders http.Header
	bufStatus      int
	bufBody        bytes.Buffer
	sealed         bool
	preludeSent    bool
	committed      atomic.Bool
	OnDiscard      func()
}

func NewPreludeBuffer(w http.ResponseWriter) *PreludeBuffer {
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
		if err := b.commit(); err != nil {
			return 0, err
		}
		return sse.WriteJSONChunk(b.inner, p)
	}
	return b.bufBody.Write(p)
}

func (b *PreludeBuffer) WriteHeader(status int) {
	if b.committed.Load() {
		sse.PrepareJSONResponse(b.inner)
		b.inner.WriteHeader(status)
		return
	}
	b.bufStatus = status
}

func (b *PreludeBuffer) Flush() {
	if !b.preludeSent && !b.committed.Load() {
		return
	}
	if f, ok := b.inner.(http.Flusher); ok {
		f.Flush()
	}
}

func (b *PreludeBuffer) Seal() { b.sealed = true }

func (b *PreludeBuffer) PreludeSent() bool { return b.preludeSent }

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

func (b *PreludeBuffer) Committed() bool { return b.committed.Load() }

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
