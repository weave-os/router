package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
)

// goldenEpoch anchors the fake clock; pins and cooldowns compare against it.
var goldenEpoch = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

type goldenClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *goldenClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *goldenClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Sleep advances virtual time instead of blocking so retry backoff is free.
func (c *goldenClock) Sleep(ctx context.Context, d time.Duration) error {
	c.Advance(d)
	return ctx.Err()
}

// goldenStep scripts one upstream response. A step with err and no frames
// fails before the first byte; frames followed by err cut a committed stream.
type goldenStep struct {
	status  int
	headers map[string]string
	frames  []string
	err     error
	// cancel cancels the client's request context after frames are written.
	cancel bool
}

func goldenUpstreamError(status int, body string) goldenStep {
	return goldenStep{err: &providers.UpstreamErrorResponse{
		Status:    status,
		Headers:   http.Header{"Content-Type": {"application/json"}},
		Body:      []byte(body),
		BodyBytes: int64(len(body)),
	}}
}

type goldenAttempt struct {
	Provider string `json:"provider"`
	Model    string `json:"wire_model"`
	Endpoint string `json:"endpoint,omitempty"`
	Body     any    `json:"outbound_body,omitempty"`
	Outcome  string `json:"outcome"`
}

type goldenProvider struct {
	name  string
	steps []goldenStep
	rec   *goldenRecorder
}

func (p *goldenProvider) SupportsSubscriptions() bool { return true }

func (p *goldenProvider) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	step := p.rec.nextStep(p)
	if step.status != 0 || len(step.frames) > 0 {
		for k, v := range step.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(max(step.status, http.StatusOK))
		for _, frame := range step.frames {
			_, _ = w.Write([]byte(frame))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
	err := step.err
	if step.cancel {
		p.rec.cancelClient()
		err = ctx.Err()
	}
	p.rec.recordAttempt(goldenAttempt{
		Provider: p.name,
		Model:    decision.Model,
		Endpoint: goldenEndpoint(prep.Endpoint),
		Body:     goldenJSON(prep.Body),
		Outcome:  goldenOutcome(err),
	})
	return err
}

func (p *goldenProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	p.rec.recordAttempt(goldenAttempt{Provider: p.name, Outcome: "passthrough_unsupported"})
	return providers.ErrNotImplemented
}

func goldenEndpoint(e providers.Endpoint) string {
	if e == providers.EndpointResponses {
		return "responses"
	}
	return ""
}

func goldenOutcome(err error) string {
	var upstream *providers.UpstreamErrorResponse
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &upstream):
		return fmt.Sprintf("upstream_status_%d", upstream.Status)
	case errors.Is(err, context.Canceled):
		return "client_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error: " + err.Error()
	}
}

type goldenRouteCall struct {
	RequestedModel string   `json:"requested_model,omitempty"`
	ForceModel     string   `json:"force_model,omitempty"`
	Excluded       []string `json:"excluded,omitempty"`
	Model          string   `json:"model,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// goldenRouter returns the first decision whose model the request does not
// exclude, falling back to the last, like a ranked cluster would.
type goldenRouter struct {
	decisions []router.Decision
	err       error
	rec       *goldenRecorder
}

func (g *goldenRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	excluded := make([]string, 0, len(req.ExcludedModels)+len(req.AutomaticExcludedModels))
	for m := range req.ExcludedModels {
		excluded = append(excluded, m)
	}
	for m := range req.AutomaticExcludedModels {
		excluded = append(excluded, m)
	}
	sort.Strings(excluded)
	g.rec.mu.Lock()
	defer g.rec.mu.Unlock()
	call := goldenRouteCall{RequestedModel: req.RequestedModel, ForceModel: req.ForceModel, Excluded: excluded}
	if g.err != nil {
		call.Error = g.err.Error()
		g.rec.routes = append(g.rec.routes, call)
		return router.Decision{}, g.err
	}
	d := g.decisions[len(g.decisions)-1]
	for _, candidate := range g.decisions {
		if _, skip := req.ExcludedModels[candidate.Model]; skip {
			continue
		}
		if _, skip := req.AutomaticExcludedModels[candidate.Model]; skip {
			continue
		}
		d = candidate
		break
	}
	call.Model, call.Provider, call.Reason = d.Model, d.Provider, d.Reason
	g.rec.routes = append(g.rec.routes, call)
	return d, nil
}

type goldenPinEvent struct {
	Op     string `json:"op"`
	Role   string `json:"role,omitempty"`
	Detail any    `json:"detail,omitempty"`
}

// goldenPinStore is an in-memory sessionpin.Store keyed by session and role.
type goldenPinStore struct {
	clock *goldenClock
	rec   *goldenRecorder
	pins  map[string]sessionpin.Pin
}

func pinKey(key [sessionpin.SessionKeyLen]byte, role string) string {
	return fmt.Sprintf("%x/%s", key, role)
}

func (s *goldenPinStore) event(op, role string, detail any) {
	s.rec.pinEvents = append(s.rec.pinEvents, goldenPinEvent{Op: op, Role: role, Detail: goldenValue(detail)})
}

func (s *goldenPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	p, ok := s.pins[pinKey(key, role)]
	if !ok || !p.PinnedUntil.After(time.Now()) {
		return sessionpin.Pin{}, false, nil
	}
	return p, true, nil
}

func (s *goldenPinStore) Consume(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role string, strategy router.Strategy) (sessionpin.Pin, bool, error) {
	p, ok, err := s.Get(ctx, key, role)
	if !ok || err != nil || p.Strategy != strategy {
		return sessionpin.Pin{}, false, err
	}
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	delete(s.pins, pinKey(key, role))
	s.event("consume", role, nil)
	return p, true, nil
}

func (s *goldenPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.pins[pinKey(p.SessionKey, p.Role)] = p
	s.event("upsert", p.Role, p)
	return nil
}

func (s *goldenPinStore) UpdateUsage(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.event("update_usage", role, usage)
	return nil
}

func (s *goldenPinStore) bump(key [sessionpin.SessionKeyLen]byte, role, op string, field func(*sessionpin.Pin) *int) int {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	p := s.pins[pinKey(key, role)]
	n := field(&p)
	if op == "reset" {
		*n = 0
	} else {
		*n++
	}
	if _, ok := s.pins[pinKey(key, role)]; ok {
		s.pins[pinKey(key, role)] = p
	}
	return *n
}

func (s *goldenPinStore) IncrementUpstreamErrors(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string, _ router.Strategy) (int, error) {
	n := s.bump(key, role, "increment", func(p *sessionpin.Pin) *int { return &p.ConsecutiveUpstreamErrors })
	s.recordEvent("increment_upstream_errors", role, n)
	return n, nil
}

func (s *goldenPinStore) ResetUpstreamErrors(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string, _ router.Strategy) error {
	s.bump(key, role, "reset", func(p *sessionpin.Pin) *int { return &p.ConsecutiveUpstreamErrors })
	return nil
}

func (s *goldenPinStore) IncrementOverloadErrors(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string, _ router.Strategy) (int, error) {
	n := s.bump(key, role, "increment", func(p *sessionpin.Pin) *int { return &p.ConsecutiveOverloadErrors })
	s.recordEvent("increment_overload_errors", role, n)
	return n, nil
}

func (s *goldenPinStore) ResetOverloadErrors(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string, _ router.Strategy) error {
	s.bump(key, role, "reset", func(p *sessionpin.Pin) *int { return &p.ConsecutiveOverloadErrors })
	return nil
}

func (s *goldenPinStore) recordEvent(op, role string, detail any) {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.event(op, role, detail)
}

func (s *goldenPinStore) DisableProvider(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role, provider string, _ router.Strategy) error {
	s.recordEvent("disable_provider", role, provider)
	return nil
}

func (s *goldenPinStore) ExpireAndDemoteModel(_ context.Context, expired sessionpin.Pin, model string, reason sessionpin.DemotionReason) error {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	delete(s.pins, pinKey(expired.SessionKey, expired.Role))
	s.event("expire_and_demote", expired.Role, map[string]any{"model": model, "reason": reason})
	return nil
}

func (s *goldenPinStore) ExpireAndCoolDownModel(_ context.Context, expired sessionpin.Pin, model string, until time.Time, reason sessionpin.DemotionReason) error {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	delete(s.pins, pinKey(expired.SessionKey, expired.Role))
	s.event("expire_and_cool_down", expired.Role, map[string]any{"model": model, "reason": reason, "cooldown": until.Sub(s.clock.Now()).String()})
	return nil
}

func (s *goldenPinStore) SweepExpired(context.Context) error { return nil }

// goldenTelemetry records request rows and inference attempts; request rows
// arrive asynchronously, so each insert signals rows.
type goldenTelemetry struct {
	// Read methods are unused by the serving path; calling one panics.
	proxy.TelemetryRepository
	rec  *goldenRecorder
	rows chan struct{}
}

func (g *goldenTelemetry) InsertRequestTelemetry(_ context.Context, p proxy.InsertTelemetryParams) error {
	g.rec.mu.Lock()
	g.rec.telemetry = append(g.rec.telemetry, goldenValue(p))
	g.rec.mu.Unlock()
	g.rows <- struct{}{}
	return nil
}

func (g *goldenTelemetry) InsertInferenceAttempt(_ context.Context, p proxy.InsertInferenceAttemptParams) error {
	g.rec.mu.Lock()
	defer g.rec.mu.Unlock()
	g.rec.inferenceAttempts = append(g.rec.inferenceAttempts, goldenValue(p.Event))
	return nil
}

// goldenBilling is a funded billing.Repo that records every debit.
type goldenBilling struct{ rec *goldenRecorder }

func (b *goldenBilling) GetBalance(context.Context, string) (int64, error) { return 1_000_000_000, nil }

func (b *goldenBilling) HasActiveOverride(context.Context, string) (bool, error) { return false, nil }

func (b *goldenBilling) DebitInference(_ context.Context, p billing.DebitParams) (int64, error) {
	b.rec.mu.Lock()
	defer b.rec.mu.Unlock()
	b.rec.debits = append(b.rec.debits, goldenValue(p))
	return 1_000_000_000 - p.DeltaUsdMicros, nil
}

func (b *goldenBilling) GetAPIKeySpend(context.Context, string) (int64, *int64, bool, error) {
	return 0, nil, false, nil
}

func (b *goldenBilling) GetUserMonthlySpendAndLimit(context.Context, string, string) (int64, *int64, error) {
	return 0, nil, nil
}

func (b *goldenBilling) GetOrgMonthlySpendAndLimit(context.Context, string) (int64, *int64, error) {
	return 0, nil, nil
}

func (b *goldenBilling) GetAutopayConfig(context.Context, billing.Owner) (bool, int64, error) {
	return false, 0, nil
}

func (b *goldenBilling) BillingTablesExist(context.Context) (bool, error) { return true, nil }
