package sse

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// maxRecordSepLen is the longest SSE record separator ("\r\n\r\n"), and so the
// number of trailing bytes that must be carried across writes to recognize one.
const maxRecordSepLen = 4

// endsOnBlankLine reports whether b ends on an SSE record separator (blank
// line). A trailing lone CR is treated as unterminated — it may be the first
// half of a CRLF still in flight; injecting between the two would split the record.
func endsOnBlankLine(b []byte) bool {
	rest, ok := trimLineEnd(b)
	if !ok {
		return false
	}
	_, ok = trimLineEnd(rest)
	return ok
}

func trimLineEnd(b []byte) ([]byte, bool) {
	if !bytes.HasSuffix(b, []byte("\n")) {
		return nil, false
	}
	return bytes.TrimSuffix(b[:len(b)-1], []byte("\r")), true
}

// KeepaliveWriter injects a caller-supplied SSE frame whenever a committed
// stream has sent the client nothing for interval. Clients time out on bytes,
// not semantic events; a long reasoning phase produces no translatable frames,
// so the router must pad the gap itself.
//
// Arms on first byte (the preludeBuffer commit point) so a keepalive can never
// force a response that the router still wants to retry. Emits only at a record
// boundary so it can never split an event.
type KeepaliveWriter struct {
	inner    http.ResponseWriter
	flusher  http.Flusher
	frame    []byte
	interval time.Duration

	mu   sync.Mutex
	last time.Time
	// tail holds the last maxRecordSepLen bytes so a record separator
	// that straddles two writes is still detected.
	tail  []byte
	armed bool
	// stopped halts emission (write error or Close); closed records that Close
	// has run. Distinct: a write error must not let Close skip the goroutine reap.
	stopped bool
	closed  bool

	stop chan struct{}
	done chan struct{}
}

// NewKeepaliveWriter wraps w so that frame is emitted after every interval of
// client-facing silence. A non-positive interval yields a transparent writer.
func NewKeepaliveWriter(w http.ResponseWriter, frame []byte, interval time.Duration) *KeepaliveWriter {
	flusher, _ := w.(http.Flusher)
	return &KeepaliveWriter{
		inner:    w,
		flusher:  flusher,
		frame:    frame,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (k *KeepaliveWriter) Header() http.Header { return k.inner.Header() }

func (k *KeepaliveWriter) WriteHeader(code int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.inner.WriteHeader(code)
}

func (k *KeepaliveWriter) Write(p []byte) (n int, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	n, err = k.inner.Write(p)
	k.last = time.Now()
	k.noteWritten(p[:n])
	if !k.armed && !k.stopped && k.interval > 0 {
		k.armed = true
		go k.loop()
	}
	return n, err
}

func (k *KeepaliveWriter) Flush() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.flusher != nil {
		k.flusher.Flush()
	}
}

// Close stops the keepalive, blocking until any in-flight frame is written.
// Safe on an unarmed writer and idempotent.
func (k *KeepaliveWriter) Close() {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return
	}
	k.closed = true
	k.stopped = true
	armed := k.armed
	k.mu.Unlock()

	if !armed {
		return
	}
	close(k.stop)
	<-k.done
}

func (k *KeepaliveWriter) loop() {
	defer close(k.done)

	// Tick at half the interval so a frame lands within interval of the last
	// byte instead of up to twice as late.
	tick := time.NewTicker(max(k.interval/2, time.Millisecond))
	defer tick.Stop()

	for {
		select {
		case <-k.stop:
			return
		case <-tick.C:
			if !k.emitIfSilent() {
				return
			}
		}
	}
}

// emitIfSilent reports whether the caller should keep ticking.
func (k *KeepaliveWriter) emitIfSilent() bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.stopped {
		return false
	}
	if !endsOnBlankLine(k.tail) || time.Since(k.last) < k.interval {
		return true
	}
	if _, err := k.inner.Write(k.frame); err != nil {
		// A broken client connection is the handler's error to report; stop
		// emitting rather than spinning on a dead socket.
		k.stopped = true
		return false
	}
	if k.flusher != nil {
		k.flusher.Flush()
	}
	k.noteWritten(k.frame)
	k.last = time.Now()
	return true
}

// noteWritten keeps the trailing maxRecordSepLen bytes of the client-facing
// stream, bounded regardless of how large p is.
func (k *KeepaliveWriter) noteWritten(p []byte) {
	if len(p) >= maxRecordSepLen {
		k.tail = append(k.tail[:0], p[len(p)-maxRecordSepLen:]...)
		return
	}
	k.tail = append(k.tail, p...)
	if len(k.tail) > maxRecordSepLen {
		k.tail = append(k.tail[:0], k.tail[len(k.tail)-maxRecordSepLen:]...)
	}
}
