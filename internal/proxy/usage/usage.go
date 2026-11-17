package usage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CredentialKey string

func KeyFor(salt, token []byte) CredentialKey {
	mac := hmac.New(sha256.New, salt)
	mac.Write(token)
	sum := mac.Sum(nil)
	return CredentialKey(hex.EncodeToString(sum[:8]))
}

type Window struct {
	UsedPercent float64

	WindowMinutes int

	ResetAt time.Time

	ObservedAt time.Time
}

func (w Window) present() bool { return w.WindowMinutes > 0 || w.UsedPercent > 0 }

type Snapshot struct {
	Primary             Window
	Secondary           Window
	RepresentativeClaim AnthropicClaim
	OverageInUse        bool
	UnifiedResetAt      time.Time
	ObservedAt          time.Time
}

func (s Snapshot) hasData() bool {
	return s.OverageInUse || s.RepresentativeClaim != "" || s.Primary.present() || s.Secondary.present()
}

type AnthropicClaim string

const (
	AnthropicClaimFiveHour                AnthropicClaim = "five_hour"
	AnthropicClaimSevenDay                AnthropicClaim = "seven_day"
	AnthropicClaimOverage                 AnthropicClaim = "overage"
	AnthropicClaimSevenDayOverageIncluded AnthropicClaim = "seven_day_overage_included"
)

func PaidAnthropicOverage(claim AnthropicClaim, overageInUse string) bool {
	return claim == AnthropicClaimOverage && overageInUse == "true"
}

func AnthropicOverageCapRejected(h http.Header) bool {
	return h.Get("anthropic-ratelimit-unified-status") == "rejected" &&
		h.Get("anthropic-ratelimit-unified-overage-status") == "rejected"
}

func (s Snapshot) BillableOrExhausted() bool { return s.OverageInUse || s.Exhausted() }

const exhaustedFraction = 0.999

func (s Snapshot) Exhausted() bool {
	return s.ExhaustedAsOf(s.ObservedAt)
}

func (s Snapshot) ExhaustedAsOf(now time.Time) bool {
	if !s.hasData() {
		return false
	}
	return windowExhausted(s.Primary, s.ObservedAt, now) || windowExhausted(s.Secondary, s.ObservedAt, now)
}

func windowExhausted(window Window, observedAt, now time.Time) bool {
	return window.UsedPercent >= exhaustedFraction && window.currentAsOf(observedAt, now)
}

func (w Window) currentAsOf(observedAt, now time.Time) bool {
	if !w.ResetAt.IsZero() {
		return w.ResetAt.After(now)
	}
	if w.WindowMinutes <= 0 {
		return false
	}
	if !w.ObservedAt.IsZero() {
		observedAt = w.ObservedAt
	}
	return observedAt.Add(time.Duration(w.WindowMinutes) * time.Minute).After(now)
}

type Observer struct {
	mu           sync.RWMutex
	salt         []byte
	ttl          time.Duration
	now          func() time.Time
	data         map[CredentialKey]Snapshot
	observations map[CredentialKey]*observationEpoch
	rejected     map[CredentialKey]time.Time
}

func NewObserver(salt []byte, ttl time.Duration, now func() time.Time) *Observer {
	if now == nil {
		now = time.Now
	}
	return &Observer{salt: salt, ttl: ttl, now: now, data: make(map[CredentialKey]Snapshot), rejected: make(map[CredentialKey]time.Time)}
}

const windowConstrainedFraction = 0.5

func (o *Observer) freshFor(s Snapshot) time.Duration {
	horizon := o.ttl
	if s.OverageInUse {
		untilReset := time.Duration(1<<63 - 1)
		if !s.UnifiedResetAt.IsZero() {
			if reportedResetDelay := s.UnifiedResetAt.Sub(s.ObservedAt); reportedResetDelay > 0 {
				untilReset = min(reportedResetDelay, 7*24*time.Hour)
			}
		}
		if untilReset > horizon {
			horizon = untilReset
		}
	}
	for _, w := range [...]Window{s.Primary, s.Secondary} {
		if w.UsedPercent < windowConstrainedFraction {
			continue
		}
		d := time.Duration(w.WindowMinutes) * time.Minute

		if !w.ResetAt.IsZero() {
			if untilReset := w.ResetAt.Sub(s.ObservedAt); untilReset > 0 && untilReset < d {
				d = untilReset
			}
		}
		if d > horizon {
			horizon = d
		}
	}
	return horizon
}

func (o *Observer) Key(token []byte) CredentialKey { return KeyFor(o.salt, token) }

func (o *Observer) RecordRejected(key CredentialKey, until time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rejected[key] = until
}

func (o *Observer) Rejected(key CredentialKey) bool {
	o.mu.RLock()
	until, ok := o.rejected[key]
	o.mu.RUnlock()
	if !ok {
		return false
	}
	if o.now().Before(until) {
		return true
	}
	o.mu.Lock()
	if cur, still := o.rejected[key]; still && !o.now().Before(cur) {
		delete(o.rejected, key)
	}
	o.mu.Unlock()
	return false
}

func (o *Observer) Snapshot(key CredentialKey) (Snapshot, bool) {
	o.mu.RLock()
	snap, ok := o.data[key]
	o.mu.RUnlock()
	if !ok {
		return Snapshot{}, false
	}
	now := o.now()
	if now.Sub(snap.ObservedAt) > o.freshFor(snap) {
		o.mu.Lock()
		if cur, still := o.data[key]; still && o.now().Sub(cur.ObservedAt) > o.freshFor(cur) {
			delete(o.data, key)
		}
		o.mu.Unlock()
		return Snapshot{}, false
	}

	if snap.OverageInUse && snap.UnifiedResetAt.After(snap.ObservedAt) && !snap.UnifiedResetAt.After(now) {
		snap.OverageInUse = false
	}
	if !snap.Primary.currentAsOf(snap.ObservedAt, now) {
		snap.Primary = Window{}
	}
	if !snap.Secondary.currentAsOf(snap.ObservedAt, now) {
		snap.Secondary = Window{}
	}
	return snap, true
}

func (o *Observer) Sweep() {
	cutoff := o.now()
	o.mu.Lock()
	for k, s := range o.data {
		if cutoff.Sub(s.ObservedAt) > o.freshFor(s) {
			delete(o.data, k)
		}
	}
	o.sweepObservationsLocked()
	for k, until := range o.rejected {
		if !cutoff.Before(until) {
			delete(o.rejected, k)
		}
	}
	o.mu.Unlock()
}

func ParseCodexHeaders(h http.Header) (Snapshot, bool) {
	primary, pOK := parseCodexWindow(h, "primary", 5*60)
	secondary, sOK := parseCodexWindow(h, "secondary", 7*24*60)
	if !pOK && !sOK {
		return Snapshot{}, false
	}
	return Snapshot{Primary: primary, Secondary: secondary}, true
}

func parseCodexWindow(h http.Header, which string, defaultWindowMinutes int) (Window, bool) {
	used, ok := parsePercent(h.Get("x-codex-" + which + "-used-percent"))
	if !ok {
		return Window{}, false
	}
	resetAt, _ := ParseResetTime(h.Get("x-codex-" + which + "-reset-at"))
	w := Window{UsedPercent: used, WindowMinutes: defaultWindowMinutes, ResetAt: resetAt}
	if m, ok := parseInt(h.Get("x-codex-" + which + "-window-minutes")); ok && m > 0 && int64(m) <= int64((1<<63-1)/time.Minute) {
		w.WindowMinutes = m
	}
	return w, true
}

func ParseAnthropicUnifiedHeaders(h http.Header) (Snapshot, bool) {
	primary, pOK := parseAnthropicWindow(h, "5h", 5*60)
	secondary, sOK := parseAnthropicWindow(h, "7d", 7*24*60)
	if !sOK {
		secondary, sOK = parseAnthropicWindow(h, "weekly", 7*24*60)
	}
	claim := AnthropicClaim(h.Get("anthropic-ratelimit-unified-representative-claim"))
	switch claim {
	case AnthropicClaimFiveHour, AnthropicClaimSevenDay, AnthropicClaimOverage, AnthropicClaimSevenDayOverageIncluded:
	default:
		claim = ""
	}
	if !pOK && !sOK && claim == "" {
		return Snapshot{}, false
	}
	resetAt, _ := ParseResetTime(h.Get("anthropic-ratelimit-unified-reset"))
	return Snapshot{
		Primary: primary, Secondary: secondary, RepresentativeClaim: claim,
		OverageInUse:   PaidAnthropicOverage(claim, h.Get("anthropic-ratelimit-unified-overage-in-use")),
		UnifiedResetAt: resetAt,
	}, true
}

func parseAnthropicWindow(h http.Header, which string, windowMinutes int) (Window, bool) {
	prefix := "anthropic-ratelimit-unified-" + which + "-"
	resetAt, _ := ParseResetTime(h.Get(prefix + "reset"))
	if used, ok := parseFraction(h.Get(prefix + "utilization")); ok {
		return Window{UsedPercent: used, WindowMinutes: windowMinutes, ResetAt: resetAt}, true
	}
	limit, lOK := parseFloat(h.Get(prefix + "limit"))
	remaining, rOK := parseFloat(h.Get(prefix + "remaining"))
	if !lOK || !rOK || limit <= 0 {
		return Window{}, false
	}
	used := math.Max(0, 1-remaining/limit)
	return Window{UsedPercent: used, WindowMinutes: windowMinutes, ResetAt: resetAt}, true
}

func ParseResetTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if secs, ok := parseFloat(s); ok && secs > 0 {
		return time.Unix(int64(secs), 0).UTC(), true
	}
	return time.Time{}, false
}

func parsePercent(s string) (float64, bool) {
	v, ok := parseFloat(s)
	if !ok {
		return 0, false
	}
	v /= 100
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v, true
}

func parseFraction(s string) (float64, bool) {
	v, ok := parseFloat(s)
	if !ok {
		return 0, false
	}
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v, true
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return v, true
}
