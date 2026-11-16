package sse

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

const maxRecordSepLen = 4

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

type KeepaliveWriter struct {
	inner    http.ResponseWriter
	flusher  http.Flusher
	frame    []byte
	interval time.Duration

	mu      sync.Mutex
	last    time.Time
	tail    []byte
	armed   bool
	stopped bool
	closed  bool

	stop chan struct{}
	done chan struct{}
}

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
	PrepareJSONResponse(k.inner)
	k.inner.WriteHeader(code)
}

func (k *KeepaliveWriter) Write(p []byte) (n int, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	n, err = WriteJSONChunk(k.inner, p)
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
