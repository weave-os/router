package timing

import (
	"context"
	"sync/atomic"
	"time"
)

type Timing struct {
	EntryNanos               atomic.Int64
	UpstreamRequestNanos     atomic.Int64
	UpstreamHeadersNanos     atomic.Int64
	UpstreamFirstByteNanos   atomic.Int64
	UpstreamFirstOutputNanos atomic.Int64
	UpstreamEOFNanos         atomic.Int64
}

func stampOnce(field *atomic.Int64) {
	field.CompareAndSwap(0, time.Now().UnixNano())
}

func (t *Timing) StampEntry() {
	if t == nil {
		return
	}
	stampOnce(&t.EntryNanos)
}

func (t *Timing) StampUpstreamRequest() {
	if t == nil {
		return
	}
	stampOnce(&t.UpstreamRequestNanos)
}

func (t *Timing) StampUpstreamHeaders() {
	if t == nil {
		return
	}
	stampOnce(&t.UpstreamHeadersNanos)
}

func (t *Timing) StampUpstreamFirstByte() {
	if t == nil {
		return
	}
	stampOnce(&t.UpstreamFirstByteNanos)
}

func (t *Timing) StampUpstreamFirstOutput() {
	if t == nil {
		return
	}
	stampOnce(&t.UpstreamFirstOutputNanos)
}

func (t *Timing) StampUpstreamEOF() {
	if t == nil {
		return
	}
	stampOnce(&t.UpstreamEOFNanos)
}

func (t *Timing) Ms(from, to *atomic.Int64) int64 {
	if t == nil {
		return 0
	}
	f, tt := from.Load(), to.Load()
	if f == 0 || tt == 0 {
		return 0
	}
	return (tt - f) / int64(time.Millisecond)
}

func (t *Timing) MsSince(from *atomic.Int64) int64 {
	if t == nil {
		return 0
	}
	f := from.Load()
	if f == 0 {
		return 0
	}
	return (time.Now().UnixNano() - f) / int64(time.Millisecond)
}

type timingKey struct{}

func WithTiming(ctx context.Context) (context.Context, *Timing) {
	t := &Timing{}
	return context.WithValue(ctx, timingKey{}, t), t
}

func TimingFrom(ctx context.Context) *Timing {
	t, _ := ctx.Value(timingKey{}).(*Timing)
	return t
}

func FirstOutputMark(ctx context.Context, mark func()) func() {
	t := TimingFrom(ctx)
	return func() {
		t.StampUpstreamFirstOutput()
		mark()
	}
}
