package usage

import (
	"context"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"weave-os/router/internal/observability"
)

const (
	seatUsageRecordInterval = time.Minute
	seatUsageRecordTimeout  = 2 * time.Second
	seatUsageRecentUsers    = 8192
)

type SeatUsage struct {
	Provider string
	Snapshot Snapshot
}

type SeatUsageRecorder interface {
	RecordSeatUsage(ctx context.Context, routerUserID, provider string, snapshot Snapshot) error
}

type seatRecording struct {
	recorder SeatUsageRecorder
	mu       sync.Mutex
	recorded *lru.Cache[string, time.Time]
}

func (o *Observer) WithSeatUsageRecorder(recorder SeatUsageRecorder) *Observer {
	recorded, _ := lru.New[string, time.Time](seatUsageRecentUsers)
	o.seats = &seatRecording{recorder: recorder, recorded: recorded}
	return o
}

func (o *Observer) RecordSeat(ctx context.Context, routerUserID, provider string, snapshot Snapshot) {
	if o == nil || o.seats == nil || routerUserID == "" {
		return
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = o.now()
	}
	seats := o.seats
	key := routerUserID + ":" + provider
	seats.mu.Lock()
	last, seen := seats.recorded.Get(key)
	if seen && snapshot.ObservedAt.Sub(last) < seatUsageRecordInterval {
		seats.mu.Unlock()
		return
	}
	seats.recorded.Add(key, snapshot.ObservedAt)
	seats.mu.Unlock()
	log := observability.FromContext(ctx)
	observability.SafeGo(log, seatUsageRecordTimeout, "record_caller_seat_usage", func(writeCtx context.Context) {
		if err := seats.recorder.RecordSeatUsage(writeCtx, routerUserID, provider, snapshot); err != nil {
			log.Warn("Failed to record caller seat usage", "provider", provider, "err", err)
		}
	})
}
