// Package modelstatus tracks the transient health of catalog provider bindings.
package modelstatus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"workweave/router/internal/providers"
)

// Status is the current routing health of a provider binding.
type Status int

const (
	// StatusUnknown is the defensive zero value for an untracked binding.
	StatusUnknown Status = iota
	// StatusOnline participates in routing normally.
	StatusOnline
	// StatusOffline is excluded from routing.
	StatusOffline
	// StatusRateLimited participates with a score penalty.
	StatusRateLimited
	// StatusMaintenance is administratively excluded from routing.
	StatusMaintenance
	// StatusError is excluded until recovery.
	StatusError
)

// String returns the status wire label.
func (s Status) String() string {
	switch s {
	case StatusOnline:
		return "online"
	case StatusOffline:
		return "offline"
	case StatusRateLimited:
		return "rate_limited"
	case StatusMaintenance:
		return "maintenance"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// ParseStatus parses a status wire label.
func ParseStatus(value string) (Status, error) {
	switch value {
	case "online":
		return StatusOnline, nil
	case "offline":
		return StatusOffline, nil
	case "rate_limited":
		return StatusRateLimited, nil
	case "maintenance":
		return StatusMaintenance, nil
	case "error":
		return StatusError, nil
	default:
		return StatusUnknown, fmt.Errorf("invalid status: %s", value)
	}
}

// Source identifies what most recently changed an entry.
type Source string

const (
	// SourceBoot is derived from deployment-key wiring at startup.
	SourceBoot Source = "boot"
	// SourceRequest comes from an upstream request outcome.
	SourceRequest Source = "request"
	// SourceAdmin is a pinned administrative override.
	SourceAdmin Source = "admin_override"
	// SourceAutoRecover marks an expired cooldown.
	SourceAutoRecover Source = "auto_recover"
)

// Key identifies one logical-model/provider binding.
type Key struct {
	ModelID  string
	Provider string
}

// Entry is a snapshot of one binding's current state.
type Entry struct {
	Key
	Status      Status
	Reason      string
	Source      Source
	UpdatedAt   time.Time
	ExpiresAt   time.Time
	AdminPinned bool
	Wired       bool
}

// ChangeLogger receives actual state transitions. It must not retain Entry pointers.
type ChangeLogger func(context.Context, Entry, Status)

// Store is a concurrent in-memory binding-status registry.
type Store struct {
	mu                sync.RWMutex
	now               func() time.Time
	rateLimitCooldown time.Duration
	errorCooldown     time.Duration
	rows              map[Key]*Entry
	logChange         ChangeLogger
}

// New constructs an empty Store.
func New(now func() time.Time, rateLimitCooldown, errorCooldown time.Duration, logChange ChangeLogger) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, rateLimitCooldown: rateLimitCooldown, errorCooldown: errorCooldown, rows: make(map[Key]*Entry), logChange: logChange}
}

// Initialize records the boot-derived state of a binding.
func (s *Store) Initialize(ctx context.Context, key Key, wired bool) Entry {
	status, reason := StatusOffline, "no deployment key"
	if wired {
		status, reason = StatusOnline, ""
	}
	return s.update(ctx, key, status, reason, SourceBoot, false, 0, &wired, false)
}

// SetStatus applies an automatic or administrative state update.
func (s *Store) SetStatus(ctx context.Context, key Key, status Status, reason string, source Source, admin bool, cooldown time.Duration) Entry {
	return s.update(ctx, key, status, reason, source, admin, cooldown, nil, true)
}

func (s *Store) update(ctx context.Context, key Key, status Status, reason string, source Source, admin bool, cooldown time.Duration, wired *bool, respectPin bool) Entry {
	s.mu.Lock()
	row, ok := s.rows[key]
	if !ok {
		row = &Entry{Key: key}
		s.rows[key] = row
	}
	if respectPin && row.AdminPinned && !admin {
		out := *row
		s.mu.Unlock()
		return out
	}
	if admin && row.AdminPinned && row.Status == status && row.Reason == reason {
		out := *row
		s.mu.Unlock()
		return out
	}
	previous := row.Status
	now := s.now()
	row.Status = status
	row.Reason = reason
	row.Source = source
	row.UpdatedAt = now
	row.AdminPinned = admin
	if wired != nil {
		row.Wired = *wired
	}
	row.ExpiresAt = time.Time{}
	if cooldown > 0 {
		row.ExpiresAt = now.Add(cooldown)
	}
	out := *row
	changed := previous != status
	s.mu.Unlock()
	if changed && s.logChange != nil {
		s.logChange(ctx, out, previous)
	}
	return out
}

// ResetToAuto clears an admin override and restores the boot-derived state.
func (s *Store) ResetToAuto(ctx context.Context, key Key) (Entry, bool) {
	s.mu.Lock()
	row, ok := s.rows[key]
	if !ok {
		s.mu.Unlock()
		return Entry{}, false
	}
	wired := row.Wired
	s.mu.Unlock()
	return s.Initialize(ctx, key, wired), true
}

// Lookup returns the effective status, lazily recovering expired cooldowns.
func (s *Store) Lookup(ctx context.Context, key Key) Status {
	entry, ok := s.Get(ctx, key)
	if !ok {
		return StatusUnknown
	}
	return entry.Status
}

// Get returns an entry copy, lazily recovering an expired cooldown.
func (s *Store) Get(ctx context.Context, key Key) (Entry, bool) {
	s.mu.RLock()
	row, ok := s.rows[key]
	if !ok {
		s.mu.RUnlock()
		return Entry{}, false
	}
	shouldRecover := !row.AdminPinned && !row.ExpiresAt.IsZero() && !s.now().Before(row.ExpiresAt)
	if !shouldRecover {
		out := *row
		s.mu.RUnlock()
		return out, true
	}
	s.mu.RUnlock()

	s.mu.Lock()
	row, ok = s.rows[key]
	if !ok {
		s.mu.Unlock()
		return Entry{}, false
	}
	if !row.AdminPinned && !row.ExpiresAt.IsZero() && !s.now().Before(row.ExpiresAt) {
		previous := row.Status
		row.Status = StatusOnline
		row.Reason = "cooldown expired"
		row.Source = SourceAutoRecover
		row.UpdatedAt = s.now()
		row.ExpiresAt = time.Time{}
		out := *row
		s.mu.Unlock()
		if s.logChange != nil {
			s.logChange(ctx, out, previous)
		}
		return out, true
	}
	out := *row
	s.mu.Unlock()
	return out, true
}

// Snapshot returns all entries sorted by provider and model ID.
func (s *Store) Snapshot(ctx context.Context) []Entry {
	s.mu.RLock()
	keys := make([]Key, 0, len(s.rows))
	for key := range s.rows {
		keys = append(keys, key)
	}
	s.mu.RUnlock()
	out := make([]Entry, 0, len(keys))
	for _, key := range keys {
		if entry, ok := s.Get(ctx, key); ok {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].ModelID < out[j].ModelID
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// RecordOutcome updates a binding from one deployment-key dispatch attempt.
func (s *Store) RecordOutcome(ctx context.Context, key Key, err error, byok bool) {
	if byok {
		return
	}
	current, ok := s.Get(ctx, key)
	if !ok || current.AdminPinned {
		return
	}
	if err == nil {
		if current.Status == StatusRateLimited || current.Status == StatusError {
			s.SetStatus(ctx, key, StatusOnline, "request succeeded", SourceRequest, false, 0)
		}
		return
	}
	status := upstreamStatus(err)
	if status == http.StatusTooManyRequests || status == 529 || status == 530 {
		s.SetStatus(ctx, key, StatusRateLimited, fmt.Sprintf("upstream %d", status), SourceRequest, false, s.rateLimitCooldown)
		return
	}
	if status >= 500 && status <= 511 && status != http.StatusNotImplemented && status != http.StatusHTTPVersionNotSupported {
		s.SetStatus(ctx, key, StatusError, fmt.Sprintf("upstream %d", status), SourceRequest, false, s.errorCooldown)
		return
	}
	if status == 0 && providers.IsRetryable(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		s.SetStatus(ctx, key, StatusError, "upstream transport error", SourceRequest, false, s.errorCooldown)
	}
}

func upstreamStatus(err error) int {
	var buffered *providers.UpstreamErrorResponse
	if errors.As(err, &buffered) {
		return buffered.Status
	}
	var flushed *providers.UpstreamStatusError
	if errors.As(err, &flushed) {
		return flushed.Status
	}
	return 0
}
