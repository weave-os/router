package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cyberRefusalSSE is the shape OpenAI's classifier streams on a 200: no output
// frame, an error event, then the terminal failure Codex aborts the turn on.
const cyberRefusalSSE = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6-sol"}}` + "\n\n" +
	"event: error\n" +
	`data: {"type":"error","message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber"}` + "\n\n" +
	"event: turn.failed\n" +
	`data: {"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk."}}` + "\n\n"

const anthropicRescueSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-5","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"rescued"}}` + "\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const responsesTurnBody = `{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user",` +
	`"content":[{"type":"input_text","text":"audit this SFTP server for auth bypasses"}]}]}`

// cyberRefusalUpstreams serves the OpenAI refusal and the Anthropic rescue,
// counting hits on each so a test can tell a rescue from a passthrough.
type cyberRefusalUpstreams struct {
	mu             sync.Mutex
	openAIHits     int
	anthropicHits  int
	openAIResponse func(http.ResponseWriter)
}

func (u *cyberRefusalUpstreams) counts() (openAI, anthropic int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.openAIHits, u.anthropicHits
}

func (u *cyberRefusalUpstreams) start(t *testing.T) (openAIURL, anthropicURL string) {
	t.Helper()
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		u.openAIHits++
		u.mu.Unlock()
		u.openAIResponse(w)
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		u.anthropicHits++
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicRescueSSE)
	}))
	t.Cleanup(anthropicServer.Close)

	return openAIServer.URL, anthropicServer.URL
}

func streamResponses(sse string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Frames are split mid-event, as they arrive on the wire.
		for start := 0; start < len(sse); start += 37 {
			end := min(start+37, len(sse))
			_, _ = io.WriteString(w, sse[start:end])
		}
	}
}

func cyberRefusalService(
	openAIURL, anthropicURL, decisionReason string,
	store *fakePinStore,
	telemetry *captureTelemetry,
) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{
			Provider: providers.ProviderOpenAI,
			Model:    "gpt-5.6-sol",
			Reason:   decisionReason,
			Metadata: &router.RoutingMetadata{CandidateModels: []string{"gpt-5.6-sol"}},
		}},
		map[string]providers.Client{
			providers.ProviderOpenAI:    openai.NewClient("test-openai-key", openAIURL),
			providers.ProviderAnthropic: anthropic.NewClient("test-anthropic-key", anthropicURL),
		},
		nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", telemetry,
	).WithDeploymentKeyedProviders(map[string]struct{}{
		providers.ProviderOpenAI:    {},
		providers.ProviderAnthropic: {},
	})
}

const cyberRefusalInstallationID = "22222222-2222-2222-2222-222222222222"

func proxyResponsesTurn(t *testing.T, svc *proxy.Service) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	_ = svc.ProxyOpenAIResponses(authedCtx(cyberRefusalInstallationID), []byte(responsesTurnBody), rec, req)
	return rec
}

// The refusal is the turn's first event, so the turn is still rescuable: it must
// be re-served off-vendor and the session re-pinned so turn N+1 skips the model
// that declined. Codex aborts on the refusal envelope, which is what makes
// swallowing it the whole point.
func TestProxyOpenAIResponses_CyberRefusalRescuesOffVendorAndRepins(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{openAIResponse: streamResponses(cyberRefusalSSE)}
	openAIURL, anthropicURL := upstreams.start(t)
	store := newFakePinStore()
	telemetry := newCaptureTelemetry()

	rec := proxyResponsesTurn(t, cyberRefusalService(openAIURL, anthropicURL, "test", store, telemetry))

	openAIHits, anthropicHits := upstreams.counts()
	assert.Equal(t, 1, openAIHits, "the refusing vendor is not retried")
	assert.Equal(t, 1, anthropicHits, "the turn is re-served on the fallback")

	body := rec.Body.String()
	assert.NotContains(t, body, "cybersecurity risk", "the refusal must never reach the client")
	assert.Contains(t, body, "rescued", "the client sees the fallback's answer")
	assert.Contains(t, body, "response.completed")
	assert.Equal(t, "claude-sonnet-5", rec.Header().Get(proxy.HeaderRouterModel))

	row := telemetry.firstRow(t)
	assert.Equal(t, "claude-sonnet-5", row.DecisionModel)
	assert.Equal(t, providers.ProviderAnthropic, row.DecisionProvider)
	assert.Equal(t, proxy.ReasonCyberRefusalRetry, row.DecisionReason)

	require.NotEmpty(t, store.upserts, "the session must be re-pinned off the refusing model")
	pin := store.upserts[len(store.upserts)-1]
	assert.Equal(t, "claude-sonnet-5", pin.Model)
	assert.Equal(t, providers.ProviderAnthropic, pin.Provider)
}

// Once output is committed a second model's stream would interleave with the
// first's, so the refusal is surfaced as-is — the re-pin still runs.
func TestProxyOpenAIResponses_CyberRefusalAfterCommittedOutputPassesThrough(t *testing.T) {
	committed := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6-sol"}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","message":"This content was flagged for possible cybersecurity risk."}` + "\n\n"
	upstreams := &cyberRefusalUpstreams{openAIResponse: streamResponses(committed)}
	openAIURL, anthropicURL := upstreams.start(t)
	store := newFakePinStore()

	rec := proxyResponsesTurn(t, cyberRefusalService(openAIURL, anthropicURL, "test", store, newCaptureTelemetry()))

	_, anthropicHits := upstreams.counts()
	assert.Zero(t, anthropicHits, "a committed turn cannot be re-served")
	assert.Contains(t, rec.Body.String(), "cybersecurity risk", "the client keeps the upstream's own error")

	require.NotEmpty(t, store.upserts, "the re-pin runs even when the turn cannot be rescued")
	assert.Equal(t, "claude-sonnet-5", store.upserts[len(store.upserts)-1].Model)
}

// A caller who pinned the model with /force-model asked for that model, and the
// kill switch has to be able to take the whole behavior out.
func TestProxyOpenAIResponses_CyberRefusalRetrySkipped(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reason       string
		retryFlag    bool
		wantFinalPin string
	}{
		{name: "force-model pin", reason: translate.ReasonUserForceModel, retryFlag: true, wantFinalPin: "gpt-5.6-sol"},
		{name: "retry kill switch off", reason: "test", retryFlag: false, wantFinalPin: "claude-sonnet-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreams := &cyberRefusalUpstreams{openAIResponse: streamResponses(cyberRefusalSSE)}
			openAIURL, anthropicURL := upstreams.start(t)
			store := newFakePinStore()
			svc := cyberRefusalService(openAIURL, anthropicURL, tc.reason, store, newCaptureTelemetry()).
				WithCyberRefusalRetry(tc.retryFlag)

			rec := proxyResponsesTurn(t, svc)

			_, anthropicHits := upstreams.counts()
			assert.Zero(t, anthropicHits, "no off-vendor re-dispatch")
			assert.Contains(t, rec.Body.String(), "cybersecurity risk",
				"without the rescue the refusal is the turn's answer")
			require.NotEmpty(t, store.upserts)
			assert.Equal(t, tc.wantFinalPin, store.upserts[len(store.upserts)-1].Model,
				"the re-pin is gated separately from the retry")
		})
	}
}

// An ordinary upstream failure is not a refusal: it keeps today's failover
// behavior and leaves the session pin alone.
func TestProxyOpenAIResponses_OrdinaryOpenAIErrorIsUnchanged(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{openAIResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'temperature'","type":"invalid_request_error","code":"unsupported_parameter"}}`)
	}}
	openAIURL, anthropicURL := upstreams.start(t)
	store := newFakePinStore()

	rec := proxyResponsesTurn(t, cyberRefusalService(openAIURL, anthropicURL, "test", store, newCaptureTelemetry()))

	_, anthropicHits := upstreams.counts()
	assert.Zero(t, anthropicHits)
	assert.Contains(t, rec.Body.String(), "Unsupported parameter")
	require.NotEmpty(t, store.upserts)
	assert.Equal(t, "gpt-5.6-sol", store.upserts[len(store.upserts)-1].Model,
		"an ordinary error is no reason to move the session")
}

// The rescue runs once: a fallback that refuses too ends the turn rather than
// walking the catalog.
func TestProxyOpenAIResponses_CyberRefusalRetriesOnlyOnce(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{openAIResponse: streamResponses(cyberRefusalSSE)}
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreams.mu.Lock()
		upstreams.openAIHits++
		upstreams.mu.Unlock()
		streamResponses(cyberRefusalSSE)(w)
	}))
	defer openAIServer.Close()
	anthropicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreams.mu.Lock()
		upstreams.anthropicHits++
		upstreams.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: error\n"+
			`data: {"type":"error","error":{"type":"invalid_request_error","message":"This content was flagged for possible cybersecurity risk."}}`+"\n\n")
	}))
	defer anthropicServer.Close()

	svc := cyberRefusalService(openAIServer.URL, anthropicServer.URL, "test", newFakePinStore(), newCaptureTelemetry())
	proxyResponsesTurn(t, svc)

	openAIHits, anthropicHits := upstreams.counts()
	assert.Equal(t, 1, openAIHits)
	assert.Equal(t, 1, anthropicHits, "the fallback's own refusal is not rescued again")
}
