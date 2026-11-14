package proxy_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/sse"
)

const (
	ingressAnthropic = "anthropic_messages"
	ingressOpenAI    = "openai_chat"

	goldenInstallationID = "00000000-0000-4000-8000-000000000001"
)

type goldenScenario struct {
	name      string
	ingress   string
	decisions []router.Decision
	routeErr  error
	upstreams map[string][]goldenStep
	requests  []goldenRequest
	configure func(*proxy.Service, *goldenHarness) *proxy.Service
}

type goldenRequest struct {
	body string
	// advance moves the fake clock before the request is served.
	advance         time.Duration
	decorateContext func(context.Context) context.Context
	// noTelemetry marks requests refused before a request row is emitted.
	noTelemetry bool
	// bodyFrom builds the body from the previous raw response when set.
	bodyFrom func(previous []byte) string
}

type goldenRecorder struct {
	mu                sync.Mutex
	calls             map[string]int
	steps             map[string][]goldenStep
	routes            []goldenRouteCall
	attempts          []goldenAttempt
	inferenceAttempts []any
	pinEvents         []goldenPinEvent
	storeEvents       []goldenPinEvent
	debits            []any
	telemetry         []any
	cancel            context.CancelFunc
}

// goldenRecordLengths marks where one request's records start.
type goldenRecordLengths struct {
	routes, attempts, inferenceAttempts, pinEvents, storeEvents, debits, telemetry int
}

func (r *goldenRecorder) lengths() goldenRecordLengths {
	return goldenRecordLengths{
		routes: len(r.routes), attempts: len(r.attempts), inferenceAttempts: len(r.inferenceAttempts),
		pinEvents: len(r.pinEvents), storeEvents: len(r.storeEvents), debits: len(r.debits), telemetry: len(r.telemetry),
	}
}

func (r *goldenRecorder) nextStep(p *goldenProvider) goldenStep {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.calls[p.name]
	r.calls[p.name]++
	if len(p.steps) == 0 {
		return goldenUpstreamError(http.StatusInternalServerError, `{"error":"unscripted upstream call"}`)
	}
	return p.steps[min(i, len(p.steps)-1)]
}

func (r *goldenRecorder) recordAttempt(a goldenAttempt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, a)
}

func (r *goldenRecorder) cancelClient() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	cancel()
}

type goldenHarness struct {
	clock     *goldenClock
	rec       *goldenRecorder
	telemetry *goldenTelemetry
	pins      *goldenPinStore
	svc       *proxy.Service
	lastBody  []byte
}

// newGoldenHarness wires a real Service the way cmd/router/main.go does, with
// every store and upstream replaced by an in-memory fake.
func newGoldenHarness(t *testing.T, sc goldenScenario) *goldenHarness {
	t.Helper()
	h := &goldenHarness{clock: &goldenClock{now: goldenEpoch}, rec: &goldenRecorder{calls: map[string]int{}}}
	h.telemetry = &goldenTelemetry{rec: h.rec, rows: make(chan struct{}, 64)}
	h.pins = &goldenPinStore{clock: h.clock, rec: h.rec, pins: map[string]sessionpin.Pin{}}

	providerMap := make(map[string]providers.Client, len(sc.upstreams))
	available := make(map[string]struct{}, len(sc.upstreams))
	for name, steps := range sc.upstreams {
		providerMap[name] = &goldenProvider{name: name, steps: steps, rec: h.rec}
		available[name] = struct{}{}
	}
	executor, err := dispatch.NewExecutor(dispatch.NewClients(providerMap),
		dispatch.WithAttemptSink(proxy.NewAttemptSink(h.telemetry, nil)),
		dispatch.WithClock(h.clock.Now),
		dispatch.WithSleep(h.clock.Sleep))
	require.NoError(t, err)
	auxiliary := policy.DefaultRegistry().FixedCatalogTargetSet(available)
	for id := range catalog.RoutingTargetSet(available) {
		auxiliary[id] = struct{}{}
	}
	plans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		auxiliary, available, func(m catalog.Model) string { return m.ID }, policy.ProviderPolicy{}))
	require.NoError(t, err)

	rt := &goldenRouter{decisions: sc.decisions, err: sc.routeErr, rec: h.rec}
	svc := proxy.NewService(rt, providerMap, nil, h.pins, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", h.telemetry).
		WithDeploymentKeyedProviders(available).
		WithProviderOverloaded(false, breaker.New(breaker.Defaults(), h.clock.Now)).
		WithRetrySleep(h.clock.Sleep).
		WithBillingService(billing.NewService(&goldenBilling{rec: h.rec})).
		WithInferenceExecutor(executor).
		WithInferencePlans(plans).
		WithInferenceDeployment(policy.DeploymentPolicyConfig{AvailableProviders: available})
	if sc.configure != nil {
		svc = sc.configure(svc, h)
	}
	h.svc = svc
	return h
}

type goldenResponse struct {
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
	Events  []goldenSSEEvent  `json:"events,omitempty"`
}

type goldenSSEEvent struct {
	Event string `json:"event,omitempty"`
	Data  any    `json:"data"`
}

type goldenRequestTrace struct {
	Ingress           string               `json:"ingress"`
	Stream            bool                 `json:"stream"`
	Routes            []goldenRouteCall    `json:"routing_decisions"`
	Attempts          []goldenAttempt      `json:"dispatch_attempts"`
	InferenceAttempts []any                `json:"attempt_events,omitempty"`
	Response          goldenResponse       `json:"response"`
	DispatchError     *goldenDispatchError `json:"dispatch_error,omitempty"`
	PinEvents         []goldenPinEvent     `json:"pin_events,omitempty"`
	StoreEvents       []goldenPinEvent     `json:"store_events,omitempty"`
	Billing           []any                `json:"billing,omitempty"`
	Telemetry         []any                `json:"telemetry,omitempty"`
}

func (h *goldenHarness) serve(t *testing.T, ingress string, req goldenRequest) goldenRequestTrace {
	t.Helper()
	h.clock.Advance(req.advance)
	base := context.WithValue(context.Background(), proxy.APIKeyIDContextKey{}, "golden-key")
	base = context.WithValue(base, proxy.InstallationIDContextKey{}, goldenInstallationID)
	base = context.WithValue(base, proxy.ExternalIDContextKey{}, "golden-org")
	if req.decorateContext != nil {
		base = req.decorateContext(base)
	}
	ctx, cancel := context.WithCancel(base)
	defer cancel()

	h.rec.mu.Lock()
	h.rec.cancel = cancel
	before := h.rec.lengths()
	h.rec.mu.Unlock()

	path := "/v1/messages"
	if ingress == ingressOpenAI {
		path = "/v1/chat/completions"
	}
	httpReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(req.body)).WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var err error
	if ingress == ingressOpenAI {
		err = h.svc.ProxyOpenAIChatCompletion(ctx, []byte(req.body), rec, httpReq)
	} else {
		err = h.svc.ProxyMessages(ctx, []byte(req.body), rec, httpReq)
	}
	if !req.noTelemetry {
		h.awaitTelemetry(t, before.telemetry)
	}

	var parsed struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal([]byte(req.body), &parsed)
	h.lastBody = rec.Body.Bytes()
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	trace := goldenRequestTrace{
		Ingress:           ingress,
		Stream:            parsed.Stream,
		Routes:            append([]goldenRouteCall{}, h.rec.routes[before.routes:]...),
		Attempts:          append([]goldenAttempt{}, h.rec.attempts[before.attempts:]...),
		InferenceAttempts: h.rec.inferenceAttempts[before.inferenceAttempts:],
		Response:          goldenClientResponse(rec),
		PinEvents:         h.rec.pinEvents[before.pinEvents:],
		StoreEvents:       h.rec.storeEvents[before.storeEvents:],
		Billing:           h.rec.debits[before.debits:],
		Telemetry:         h.rec.telemetry[before.telemetry:],
	}
	if err != nil {
		trace.DispatchError = goldenClassify(err)
	}
	return trace
}

// awaitTelemetry waits for the request row a handled request emits
// asynchronously; signals from earlier rows only trigger a recount.
func (h *goldenHarness) awaitTelemetry(t *testing.T, before int) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		h.rec.mu.Lock()
		have := len(h.rec.telemetry)
		h.rec.mu.Unlock()
		if have > before {
			return
		}
		select {
		case <-h.telemetry.rows:
		case <-timeout:
			t.Fatalf("no telemetry row within 5s; set noTelemetry on requests that emit none")
		}
	}
}

// goldenDispatchError is what the ingress handler renders from a returned
// error when nothing was written yet (see proxy.ClassifyDispatchError).
type goldenDispatchError struct {
	Outcome       string `json:"outcome"`
	Kind          int    `json:"kind,omitempty"`
	Status        int    `json:"status,omitempty"`
	Message       string `json:"message,omitempty"`
	RetryAfter    bool   `json:"retry_after,omitempty"`
	NoClientRetry bool   `json:"no_client_retry,omitempty"`
}

func goldenClassify(err error) *goldenDispatchError {
	out := &goldenDispatchError{Outcome: goldenOutcome(err)}
	if cls, ok := proxy.ClassifyDispatchError(err); ok {
		out.Kind, out.Status, out.Message = int(cls.Kind), cls.Status, scrubGoldenString(cls.Message)
		out.RetryAfter, out.NoClientRetry = cls.RetryAfter, cls.NoClientRetry
	}
	return out
}

var goldenHeaderAllowlist = []string{"Content-Type", "Retry-After", "X-Should-Retry"}

func goldenClientResponse(rec *httptest.ResponseRecorder) goldenResponse {
	out := goldenResponse{Headers: map[string]string{}}
	if rec.Body.Len() > 0 || rec.Code != http.StatusOK {
		out.Status = rec.Code
	}
	sent := rec.Result().Header
	for name, values := range sent {
		canonical := http.CanonicalHeaderKey(name)
		allowed := strings.HasPrefix(canonical, "X-Router-")
		for _, a := range goldenHeaderAllowlist {
			allowed = allowed || canonical == a
		}
		if allowed && !goldenVolatileHeader.MatchString(canonical) {
			out.Headers[canonical] = scrubGoldenString(strings.Join(values, ","))
		}
	}
	body := rec.Body.Bytes()
	if strings.HasPrefix(sent.Get("Content-Type"), "text/event-stream") {
		for len(body) > 0 {
			event, n := sse.SplitNext(body)
			if n == 0 {
				event, n = body, len(body)
			}
			body = body[n:]
			eventType, data := sse.ParseEvent(event)
			if len(eventType) == 0 && len(data) == 0 {
				continue
			}
			out.Events = append(out.Events, goldenSSEEvent{Event: string(eventType), Data: goldenWireJSON(data)})
		}
		return out
	}
	out.Body = goldenWireJSON(body)
	return out
}

var goldenVolatileHeader = regexp.MustCompile(`(?i)(latency|duration|-ms$|request-id|trace|feedback)`)

// goldenWireJSON decodes a client- or upstream-visible JSON payload and
// replaces generated ids so only behavior remains.
func goldenWireJSON(raw []byte) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return scrubGoldenString(string(trimmed))
	}
	redactVolatile(v)
	return scrubGoldenJSON(v)
}

// goldenJSON digests oversized outbound bodies so goldens stay reviewable.
func goldenJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > 16<<10 {
		return map[string]any{"bytes": len(raw), "sha256": fmt.Sprintf("%x", sha256.Sum256(raw))}
	}
	return goldenWireJSON(raw)
}

func scrubGoldenJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if goldenVolatileKey.MatchString(k) {
				delete(x, k)
				continue
			}
			x[k] = scrubGoldenJSON(val)
		}
	case []any:
		for i := range x {
			x[i] = scrubGoldenJSON(x[i])
		}
	case string:
		return scrubGoldenString(x)
	}
	return v
}

var goldenUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// goldenVolatileKey names wall-clock or latency keys inside recorded JSON.
var goldenVolatileKey = regexp.MustCompile(`(?i)(_at|_ms|latency|timestamp)$`)

// goldenMintedID matches response ids the router mints per request.
var goldenJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

var goldenMintedID = regexp.MustCompile(`\bchatcmpl-[0-9a-f]{16}\b`)

func scrubGoldenString(s string) string {
	s = goldenJWT.ReplaceAllString(s, "<jwt>")
	s = goldenMintedID.ReplaceAllString(s, "chatcmpl-<id>")
	return goldenUUID.ReplaceAllStringFunc(s, func(id string) string {
		if id == goldenInstallationID {
			return id
		}
		return "<uuid>"
	})
}

// goldenVolatileField names struct fields holding wall-clock readings,
// durations or per-request ids.
var goldenVolatileField = regexp.MustCompile(`(Latency|Ms$|MS$|Sec$|Seconds$|Timestamp|Elapsed|Duration|At$|Until$|^RequestID$|^RouterRequestID$|^TraceID$|^SpanID$|^OperationID$|^SessionKey$|^IdempotencyKey$)`)

// goldenValue projects a recorded value into JSON-friendly form, dropping
// zero values and volatile fields so goldens show behavior only.
func goldenValue(v any) any {
	out, _ := goldenReflect(reflect.ValueOf(v))
	return out
}

func goldenReflect(v reflect.Value) (any, bool) {
	if !v.IsValid() {
		return nil, false
	}
	switch v.Interface().(type) {
	case time.Time, time.Duration:
		return nil, false
	case error:
		if v.Kind() == reflect.Pointer && v.IsNil() {
			return nil, false
		}
		return v.Interface().(error).Error(), true
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, false
		}
		return goldenReflect(v.Elem())
	case reflect.Struct:
		m := map[string]any{}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || goldenVolatileField.MatchString(f.Name) {
				continue
			}
			if val, ok := goldenReflect(v.Field(i)); ok {
				m[f.Name] = val
			}
		}
		return m, len(m) > 0
	case reflect.Map:
		if v.Len() == 0 {
			return nil, false
		}
		m := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			val, ok := goldenReflect(iter.Value())
			if !ok {
				val = true
			}
			m[scrubGoldenString(reflect.ValueOf(iter.Key().Interface()).String())] = val
		}
		return m, true
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			if v.Len() == 0 {
				return nil, false
			}
			if b := v.Bytes(); json.Valid(b) {
				return goldenWireJSON(b), true
			}
			return "<bytes>", true
		}
		if v.Len() == 0 {
			return nil, false
		}
		list := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			val, _ := goldenReflect(v.Index(i))
			list = append(list, val)
		}
		return list, true
	case reflect.String:
		return scrubGoldenString(v.String()), v.Len() > 0
	default:
		if v.IsZero() {
			return nil, false
		}
		return v.Interface(), true
	}
}

func runGoldenScenario(t *testing.T, sc goldenScenario) {
	h := newGoldenHarness(t, sc)
	traces := make([]goldenRequestTrace, 0, len(sc.requests))
	for _, req := range sc.requests {
		if req.bodyFrom != nil {
			req.body = req.bodyFrom(h.lastBody)
		}
		traces = append(traces, h.serve(t, sc.ingress, req))
	}
	got, err := json.MarshalIndent(map[string]any{"scenario": sc.name, "requests": traces}, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	path := filepath.Join("testdata", "golden", sc.name+".json")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden; run go test ./internal/proxy -run TestGolden -update")
	require.Equal(t, string(want), string(got), "decision trace changed for %s; a golden diff in a refactor PR means behavior changed", sc.name)
}

func TestGolden(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	scenarios := append(goldenInferenceScenarios(), goldenInferenceExecutionScenarios()...)
	for _, sc := range append(scenarios, goldenConversationScenarios()...) {
		require.False(t, names[sc.name], "duplicate golden scenario %s", sc.name)
		names[sc.name] = true
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			runGoldenScenario(t, sc)
		})
	}
	if *updateGolden {
		return
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "golden"))
	require.NoError(t, err)
	var orphans []string
	for _, e := range entries {
		if name := strings.TrimSuffix(e.Name(), ".json"); !names[name] {
			orphans = append(orphans, e.Name())
		}
	}
	sort.Strings(orphans)
	require.Empty(t, orphans, "golden files without a scenario")
}
