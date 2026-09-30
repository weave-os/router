// Package health tracks local serving capacity without consulting dependencies.
package health

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
)

// Limits are instance-local admission bounds. Zero disables that bound.
type Limits struct {
	MaxRequests         int
	ResumeRequests      int
	MaxBufferedBytes    int64
	ResumeBufferedBytes int64
	MemoryHighBytes     uint64
	MemoryLowBytes      uint64
}

type Reason string

const (
	ReasonAvailable Reason = "available"
	ReasonRequests  Reason = "active_requests"
	ReasonBuffers   Reason = "buffered_bytes"
	ReasonMemory    Reason = "container_memory"
	ReasonShutdown  Reason = "shutdown"
)

// Snapshot contains only local counters and the latest sampled resources.
type Snapshot struct {
	Ready          bool      `json:"ready"`
	Reason         Reason    `json:"reason"`
	ActiveRequests int       `json:"active_requests"`
	BufferedBytes  int64     `json:"buffered_bytes"`
	Resources      Resources `json:"resources"`
}

type Capacity struct {
	mu             sync.Mutex
	limits         Limits
	activeRequests int
	bufferedBytes  int64
	requestsFull   bool
	buffersFull    bool
	memoryFull     bool
	shutdown       bool
	resources      Resources
}

func NewCapacity(limits Limits) (*Capacity, error) {
	if limits.MaxRequests < 0 || limits.ResumeRequests < 0 ||
		limits.MaxRequests == 0 && limits.ResumeRequests != 0 ||
		limits.MaxRequests > 0 && limits.ResumeRequests >= limits.MaxRequests ||
		limits.MaxBufferedBytes < 0 || limits.ResumeBufferedBytes < 0 ||
		limits.MaxBufferedBytes == 0 && limits.ResumeBufferedBytes != 0 ||
		limits.MaxBufferedBytes > 0 && limits.ResumeBufferedBytes >= limits.MaxBufferedBytes ||
		limits.MemoryHighBytes == 0 && limits.MemoryLowBytes != 0 ||
		limits.MemoryHighBytes > 0 && limits.MemoryLowBytes >= limits.MemoryHighBytes {
		return nil, errors.New("capacity bounds must be nonnegative with recovery below each enabled limit")
	}
	return &Capacity{limits: limits, resources: Resources{Source: ResourceUnavailable}}, nil
}

// Permit remains owned until the underlying work finishes, including streaming.
type Permit struct {
	capacity      *Capacity
	bufferedBytes int64
	released      bool
}

func (c *Capacity) TryAcquire() *Permit {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reasonLocked() != ReasonAvailable {
		return nil
	}
	c.activeRequests++
	c.refreshLocked()
	return &Permit{capacity: c}
}

// ResizeBufferedBytes reserves the live allocation before it is made. Growth
// that copies a buffer must reserve both allocations until the copy completes.
func (p *Permit) ResizeBufferedBytes(bytes int64) bool {
	if p == nil {
		return true
	}
	c := p.capacity
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.released || bytes < 0 {
		return false
	}
	delta := bytes - p.bufferedBytes
	if delta > 0 && c.limits.MaxBufferedBytes > 0 && delta > c.limits.MaxBufferedBytes-c.bufferedBytes {
		// Variable-sized requests need not land exactly on the byte limit.
		// Withdraw only while existing retained work exceeds recovery capacity.
		c.buffersFull = c.bufferedBytes > c.limits.ResumeBufferedBytes
		return false
	}
	c.bufferedBytes += delta
	p.bufferedBytes = bytes
	c.refreshLocked()
	return true
}

// Release is idempotent, so cancellation and ordinary completion can share cleanup.
func (p *Permit) Release() {
	if p == nil {
		return
	}
	c := p.capacity
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.released {
		return
	}
	p.released = true
	c.activeRequests--
	c.bufferedBytes -= p.bufferedBytes
	c.refreshLocked()
}

func (c *Capacity) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdown = true
}

func (c *Capacity) UpdateResources(resources Resources) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resources = resources
	if c.limits.MemoryHighBytes > 0 && resources.MemoryUsageBytes != nil {
		if *resources.MemoryUsageBytes >= c.limits.MemoryHighBytes {
			c.memoryFull = true
		} else if *resources.MemoryUsageBytes <= c.limits.MemoryLowBytes {
			c.memoryFull = false
		}
	}
}

func (c *Capacity) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	reason := c.reasonLocked()
	return Snapshot{Ready: reason == ReasonAvailable, Reason: reason, ActiveRequests: c.activeRequests, BufferedBytes: c.bufferedBytes, Resources: c.resources}
}

func (c *Capacity) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		snapshot := c.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !snapshot.Ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(snapshot)
	})
}

func (c *Capacity) refreshLocked() {
	if c.limits.MaxRequests > 0 {
		if c.activeRequests >= c.limits.MaxRequests {
			c.requestsFull = true
		} else if c.activeRequests <= c.limits.ResumeRequests {
			c.requestsFull = false
		}
	}
	if c.limits.MaxBufferedBytes > 0 {
		if c.bufferedBytes >= c.limits.MaxBufferedBytes {
			c.buffersFull = true
		} else if c.bufferedBytes <= c.limits.ResumeBufferedBytes {
			c.buffersFull = false
		}
	}
}

func (c *Capacity) reasonLocked() Reason {
	switch {
	case c.shutdown:
		return ReasonShutdown
	case c.memoryFull:
		return ReasonMemory
	case c.requestsFull:
		return ReasonRequests
	case c.buffersFull:
		return ReasonBuffers
	default:
		return ReasonAvailable
	}
}
