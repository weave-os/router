package proxy

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

// A slow start is surfaced when the request's wait for provider content
// exceeds max(slowStartFloor, slowStartMultiple × p50), where p50 is the
// median per-attempt time to first content over the last slowStartSamples
// streaming attempts on that provider, endpoint and model within
// slowStartSampleAge. Below slowStartMinSamples the floor alone applies. The
// estimate is per process: each replica learns its own baseline.
const (
	slowStartFloor      = 10 * time.Second
	slowStartMultiple   = 3
	slowStartSamples    = 64
	slowStartMinSamples = 8
	slowStartSampleAge  = time.Hour
	slowStartCooldown   = 5 * time.Minute
	slowStartKeys       = 1024
	slowStartSessions   = 16384
)

type ttftSample struct {
	ttft time.Duration
	at   time.Time
}

type ttftWindow struct {
	samples [slowStartSamples]ttftSample
	next    int
	count   int
}

func (w *ttftWindow) median(now time.Time) (time.Duration, int) {
	fresh := make([]time.Duration, 0, w.count)
	for _, s := range w.samples[:w.count] {
		if now.Sub(s.at) <= slowStartSampleAge {
			fresh = append(fresh, s.ttft)
		}
	}
	if len(fresh) == 0 {
		return 0, 0
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i] < fresh[j] })
	return fresh[len(fresh)/2], len(fresh)
}

func (w *ttftWindow) add(ttft time.Duration, now time.Time) {
	w.samples[w.next] = ttftSample{ttft: ttft, at: now}
	w.next = (w.next + 1) % slowStartSamples
	w.count = min(w.count+1, slowStartSamples)
}

// slowStartTracker keeps the rolling TTFT baselines and the per-session
// cooldown that bounds how often a slow-start notice is shown.
type slowStartTracker struct {
	mu        sync.Mutex
	baselines *lru.Cache[string, *ttftWindow]
	shown     *lru.Cache[string, time.Time]
}

func newSlowStartTracker() *slowStartTracker {
	baselines, _ := lru.New[string, *ttftWindow](slowStartKeys)
	shown, _ := lru.New[string, time.Time](slowStartSessions)
	return &slowStartTracker{baselines: baselines, shown: shown}
}

var slowStarts = newSlowStartTracker()

// observe records an attempt's time to first content and returns the
// baseline median as it stood before the sample.
func (t *slowStartTracker) observe(key string, ttft time.Duration, now time.Time) (time.Duration, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.baselines.Get(key)
	if !ok {
		w = &ttftWindow{}
		t.baselines.Add(key, w)
	}
	p50, n := w.median(now)
	w.add(ttft, now)
	return p50, n
}

func (t *slowStartTracker) claim(session string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.shown.Get(session); ok && now.Sub(last) < slowStartCooldown {
		return false
	}
	t.shown.Add(session, now)
	return true
}

type slowStartAttempt struct {
	current          tracedAttempt
	attemptAt        time.Time
	firstAttemptAt   time.Time
	lastFailure      tracedAttempt
	failed           bool
	clientToolTiming *translate.ClientToolTiming
}

func (t *attemptTrace) slowStartSnapshot() (slowStartAttempt, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attemptAt.IsZero() || t.slowStartMuted {
		return slowStartAttempt{}, false
	}
	snap := slowStartAttempt{
		current:        tracedAttempt{Target: t.current, BaseURL: t.baseURL},
		attemptAt:      t.attemptAt,
		firstAttemptAt: t.firstAttemptAt,
	}
	for i := len(t.attempts) - 1; i >= 0; i-- {
		if t.attempts[i].Err != nil {
			snap.lastFailure, snap.failed = t.attempts[i], true
			break
		}
	}
	return snap, true
}

// slowStartNotice runs when the serving attempt's first provider content
// arrives: it records the attempt's TTFT and returns a notice when the
// request's wait was well above the provider's usual.
func slowStartNotice(ctx context.Context, trace *attemptTrace, tracker *slowStartTracker, now time.Time) (translate.RouterNotice, bool) {
	snap, ok := trace.slowStartSnapshot()
	if !ok {
		return translate.RouterNotice{}, false
	}
	p50, samples := tracker.observe(slowStartKey(snap.current), now.Sub(snap.attemptAt), now)
	threshold := slowStartFloor
	if samples >= slowStartMinSamples {
		threshold = max(threshold, slowStartMultiple*p50)
	} else {
		p50 = 0
	}
	wait := now.Sub(snap.firstAttemptAt)
	if wait <= threshold || trace.hideDetails() || isHeadlessClientUserAgent(ClientIdentityFrom(ctx).UserAgent) {
		return translate.RouterNotice{}, false
	}
	if cooldownKey, ok := slowStartCooldownKey(ctx); !ok || !tracker.claim(cooldownKey, now) {
		return translate.RouterNotice{}, false
	}
	observability.FromContext(ctx).Info("Slow-start notice emitted",
		"model", snap.current.Target.CatalogID,
		"provider", snap.current.Target.Provider,
		"wait_ms", wait.Milliseconds(),
		"usual_p50_ms", p50.Milliseconds(),
		"threshold_ms", threshold.Milliseconds(),
	)
	if ClientIdentityFrom(ctx).ClientApp == ClientAppCodex {
		if timing, reported := translate.CodexReportedToolTiming(trace.inbound); reported {
			snap.clientToolTiming = &timing
		}
	}
	text := slowStartLine(snap, wait, p50)
	return translate.NewRouterNotice(translate.NoticeSlowStart, text, snap.current.Target.CatalogID, ""), true
}

// slowStartCooldownKey scopes the notice cooldown to the client session, or
// to the API key when the client sends no session id, so unrelated
// session-less callers don't share one cooldown.
func slowStartCooldownKey(ctx context.Context) (string, bool) {
	if session := observability.ClientSessionIDFromContext(ctx); session != "" {
		return "session:" + session, true
	}
	if key := apiKeyIDFromContext(ctx); key != "" {
		return "key:" + key, true
	}
	return "", false
}

// slowStartLine separates the upstream wait from client-reported tool runtime.
func slowStartLine(snap slowStartAttempt, wait, p50 time.Duration) string {
	provider := snap.current.providerName()
	var details []string
	if p50 > 0 {
		details = append(details, "usually ~"+roundSeconds(p50))
	}
	if snap.failed {
		failed := snap.lastFailure
		what := failed.Target.CatalogID
		if name := failed.providerName(); name != provider {
			what += " on " + name
		}
		details = append(details, what+" "+failureWordingFor(failed).badge+" first")
	}
	details = append(details, "client tool wait excluded")
	if snap.clientToolTiming != nil {
		toolDescription := "last tool call reported "
		if snap.clientToolTiming.Yielded {
			toolDescription = "last tool call yielded after "
		}
		details = append(details, toolDescription+snap.clientToolTiming.CallDuration.Round(time.Millisecond).String())
	}
	return translate.SlowStartPrefix + provider + " took " + roundSeconds(wait) + " to first output (" + strings.Join(details, "; ") + ")\n\n"
}

func roundSeconds(d time.Duration) string {
	return strconv.Itoa(max(int(d.Round(time.Second)/time.Second), 1)) + "s"
}

func slowStartKey(a tracedAttempt) string {
	endpoint := ""
	if u, err := url.Parse(a.BaseURL); err == nil {
		endpoint = u.Host + strings.TrimRight(u.Path, "/")
	}
	return a.Target.Provider + "|" + endpoint + "|" + a.Target.CatalogID
}
