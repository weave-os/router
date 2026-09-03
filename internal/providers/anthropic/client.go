// Package anthropic is the providers.Client adapter for Anthropic's Messages API.
package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"workweave/router/internal/providers"
	"workweave/router/internal/providers/httputil"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/timing"
)

const DefaultBaseURL = "https://api.anthropic.com"

// WaferMessagesBaseURL is Wafer Serverless' Anthropic-compatible Messages
// endpoint; pair with WithAuthScheme(AuthBearer) and a Wafer-ZDR default header.
const WaferMessagesBaseURL = "https://pass.wafer.ai"

// AuthScheme selects which credential header an Anthropic-spec upstream expects.
type AuthScheme int

const (
	// AuthAPIKeyHeader sends the credential as x-api-key (api.anthropic.com).
	AuthAPIKeyHeader AuthScheme = iota
	// AuthBearer sends the credential as Authorization: Bearer. Subscription
	// OAuth tokens use Bearer under either scheme.
	AuthBearer
)

// Option configures a Client at construction.
type Option func(*Client)

// WithAuthScheme sets how the resolved credential is presented upstream.
func WithAuthScheme(scheme AuthScheme) Option {
	return func(c *Client) { c.authScheme = scheme }
}

// WithDefaultHeaders returns c with headers set on every upstream request.
// Prepared and inbound per-request headers can override these values.
func (c *Client) WithDefaultHeaders(h http.Header) *Client {
	c.defaultHeaders = h.Clone()
	return c
}

// WithProtectedHeaders returns c with headers that cannot be overridden by
// prepared or inbound per-request headers.
func (c *Client) WithProtectedHeaders(h http.Header) *Client {
	c.protectedHeaders = h.Clone()
	return c
}

// WithModelIDMap returns c with a rewrite map for the request body's
// top-level "model" field (see rewriteModelField). Pass nil to disable.
func (c *Client) WithModelIDMap(modelIDMap map[string]string) *Client {
	c.modelIDMap = modelIDMap
	return c
}

// rewriteModelField rewrites the body's top-level "model" via modelIDMap
// (mirrors openaicompat's rewriteModelField). Nil map or missing key = no-op.
func rewriteModelField(body []byte, modelIDMap map[string]string) []byte {
	if len(modelIDMap) == 0 || len(body) == 0 {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	upstream, ok := modelIDMap[model]
	if !ok {
		return body
	}
	out, err := sjson.SetBytes(body, "model", upstream)
	if err != nil {
		return body
	}
	return out
}

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	// authScheme is the credential header this upstream expects; zero value
	// (AuthAPIKeyHeader) preserves Anthropic's own behavior.
	authScheme AuthScheme
	// modelIDMap rewrites the body's "model" field when the router slug differs
	// from the upstream ID (e.g. "z-ai/glm-5.2" -> "GLM-5.2"). Nil = no rewrite.
	modelIDMap map[string]string
	// defaultHeaders are set on every upstream request (Proxy + Passthrough)
	// before prep.Headers / inbound headers apply.
	defaultHeaders http.Header
	// protectedHeaders are set after prep.Headers / inbound headers apply, so
	// provider-mandated values cannot be overridden.
	protectedHeaders http.Header
	// sseIdleTimeout overrides httputil.DefaultSSEIdleTimeout when > 0; tests set
	// it small so the output-stall watchdog fires before this one.
	sseIdleTimeout time.Duration
	// outputStall overrides httputil.DefaultOutputStallTimeout when > 0; used by
	// tests to trip output-stall without waiting out the real budget.
	outputStall time.Duration
	// throughput* override the minimum-throughput watchdog budgets when set.
	throughputWindow     time.Duration
	throughputMinElapsed time.Duration
	throughputMinDeltas  int
	throughputOverride   bool
	// versionMemo resolves gateway base URLs that already carry the "/v1"
	// segment this adapter appends.
	versionMemo providers.GatewayVersionMemo
}

func NewClient(apiKey, baseURL string, opts ...Option) *Client {
	c := &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    httputil.NewClient(httputil.NewTransport(10*time.Second, 10*time.Second)),
	}
	for _, opt := range opts {
		opt(c)
	}
	// Bearer gateways have no safe default; stay empty so a misconfigured client
	// fails rather than silently hitting api.anthropic.com.
	if c.baseURL == "" && c.authScheme == AuthAPIKeyHeader {
		c.baseURL = DefaultBaseURL
	}
	return c
}

// NewClientWithStallTimeouts is NewClient with watchdog budgets injected for testing.
func NewClientWithStallTimeouts(apiKey, baseURL string, sseIdleTimeout, outputStall time.Duration) *Client {
	c := NewClient(apiKey, baseURL)
	c.sseIdleTimeout = sseIdleTimeout
	c.outputStall = outputStall
	return c
}

// idleTimeout returns the byte-idle watchdog budget: the injected test override
// when set, else httputil.DefaultSSEIdleTimeout.
func (c *Client) idleTimeout() time.Duration {
	if c.sseIdleTimeout > 0 {
		return c.sseIdleTimeout
	}
	return httputil.DefaultSSEIdleTimeout
}

// outputStallTimeout returns the output-progress watchdog budget: the injected
// test override when set, else httputil.DefaultOutputStallTimeout.
func (c *Client) outputStallTimeout() time.Duration {
	if c.outputStall > 0 {
		return c.outputStall
	}
	return httputil.DefaultOutputStallTimeout
}

// throughputParams returns the minimum-throughput watchdog budgets: the injected
// test overrides when set, else the httputil defaults.
func (c *Client) throughputParams() (window, minElapsed time.Duration, minDeltas int) {
	if c.throughputOverride {
		return c.throughputWindow, c.throughputMinElapsed, c.throughputMinDeltas
	}
	return httputil.DefaultThroughputWindow, httputil.DefaultThroughputMinElapsed, httputil.DefaultMinThroughputDeltasPerWindow
}

// applyDefaultHeaders sets c.defaultHeaders on req before callers layer their
// own headers on top.
func (c *Client) applyDefaultHeaders(req *http.Request) {
	for k, vs := range c.defaultHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

// applyProtectedHeaders restores c.protectedHeaders after callers layer their
// own headers.
func (c *Client) applyProtectedHeaders(req *http.Request) {
	for k, vs := range c.protectedHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

// oauthBetaToken is the anthropic-beta flag Anthropic requires for Claude
// subscription (Claude.ai OAuth) tokens on /v1/messages.
const oauthBetaToken = "oauth-2025-04-20"

// subscriptionTokenPrefix marks a Claude subscription bearer (sk-ant-oat… /
// sk-ant-oat01…) used to gate the oauth beta header on the pure-passthrough path.
const subscriptionTokenPrefix = "sk-ant-oat"

// setAuth resolves credentials in precedence order: resolved per-request
// credential (subscription/BYOK/client), deployment key, then client-sent auth
// headers. A subscription OAuth credential authenticates via Authorization:
// Bearer and must NOT send x-api-key; everything else uses x-api-key.
//
// The passthrough tier scrubs router-issued Bearer tokens via
// httputil.SanitizeInboundAuthHeader before relaying inbound auth upstream.
func (c *Client) setAuth(ctx context.Context, upstream *http.Request, inbound *http.Request) {
	if creds := proxy.CredentialsFromContext(ctx); creds != nil {
		if creds.OAuth || c.authScheme == AuthBearer {
			upstream.Header.Set("authorization", "Bearer "+string(creds.APIKey))
			return
		}
		upstream.Header.Set("x-api-key", string(creds.APIKey))
		return
	}
	if c.apiKey != "" {
		if c.authScheme == AuthBearer {
			upstream.Header.Set("authorization", "Bearer "+c.apiKey)
			return
		}
		upstream.Header.Set("x-api-key", c.apiKey)
		return
	}
	// A Bearer gateway is a different tenant boundary; relaying the caller's
	// Anthropic credential there would leak it across that boundary.
	if c.authScheme == AuthBearer {
		return
	}
	if v := httputil.SanitizeInboundAuthHeader(inbound.Header.Get("authorization")); v != "" {
		upstream.Header.Set("authorization", v)
	}
	if v := inbound.Header.Get("x-api-key"); v != "" {
		upstream.Header.Set("x-api-key", v)
	}
}

// applyOAuthBeta merges the oauth beta flag into anthropic-beta when the
// request authenticates with a Claude subscription token — either a resolved
// OAuth credential, or (pure passthrough, no deployment key) a raw inbound
// sk-ant-oat Authorization bearer. Must run AFTER prep.Headers is copied onto
// the upstream request so it merges with, rather than is clobbered by, the
// model-capability-filtered anthropic-beta that translate produced.
func (c *Client) applyOAuthBeta(ctx context.Context, upstream, inbound *http.Request) {
	if !c.subscriptionAuth(ctx, inbound) {
		return
	}
	upstream.Header.Set("anthropic-beta", mergeBeta(upstream.Header.Get("anthropic-beta"), oauthBetaToken))
}

// subscriptionAuth reports whether this request will authenticate with a Claude
// subscription OAuth token. The inbound-bearer branch mirrors setAuth's own
// precedence: a configured deployment key outranks the inbound Authorization,
// so the call goes out as x-api-key and must not claim the oauth beta — a
// suppressed (exhausted) subscription failing over to the deployment key
// otherwise sends key auth plus an OAuth-only beta, which Anthropic rejects.
func (c *Client) subscriptionAuth(ctx context.Context, inbound *http.Request) bool {
	if creds := proxy.CredentialsFromContext(ctx); creds != nil {
		return creds.OAuth
	}
	if c.apiKey != "" {
		return false
	}
	if raw, found := strings.CutPrefix(inbound.Header.Get("authorization"), "Bearer "); found {
		return strings.HasPrefix(strings.TrimSpace(raw), subscriptionTokenPrefix)
	}
	return false
}

// claudeSubscriptionAuth reports whether this request authenticates with a
// Claude subscription bearer specifically, as opposed to any other OAuth-style
// credential a Bearer gateway may resolve.
func (c *Client) claudeSubscriptionAuth(ctx context.Context, inbound *http.Request) bool {
	if !c.subscriptionAuth(ctx, inbound) {
		return false
	}
	if creds := proxy.CredentialsFromContext(ctx); creds != nil {
		return strings.HasPrefix(string(creds.APIKey), subscriptionTokenPrefix)
	}
	return true
}

// mergeBeta appends token to a comma-separated anthropic-beta value if absent,
// preserving any existing (model-capability-filtered) tokens.
func mergeBeta(existing, token string) string {
	if existing == "" {
		return token
	}
	for _, p := range strings.Split(existing, ",") {
		if strings.TrimSpace(p) == token {
			return existing
		}
	}
	return existing + "," + token
}

func (c *Client) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	baseURL := proxy.EffectiveBaseURL(ctx, c.baseURL)
	body := rewriteModelField(prep.Body, c.modelIDMap)
	// Applied after the catalog map so a BYOK endpoint's own naming wins.
	body = proxy.ApplyModelAlias(ctx, body, decision.Model)
	if c.claudeSubscriptionAuth(ctx, r) {
		body = ensureClaudeCodeIdentity(body)
	}

	// 404s are buffered before reaching w, so a duplicate "/v1" can be re-tried.
	// A non-404 on the retried path is the real error and is memoized; only a
	// second 404 falls back to the probe so a genuine model-not-found is preserved.
	urls := c.versionMemo.URLs(baseURL, "/v1/messages")
	firstErr := c.proxyTo(ctx, cancel, urls[0], body, decision, prep, w, r)
	if len(urls) == 1 || !providers.IsUpstreamModelNotFound(firstErr) {
		return firstErr
	}
	err := c.proxyTo(ctx, cancel, urls[1], body, decision, prep, w, r)
	if err == nil || !providers.IsUpstreamModelNotFound(err) {
		c.versionMemo.Learn(baseURL)
		return err
	}
	return firstErr
}

func (c *Client) proxyTo(ctx context.Context, cancel context.CancelCauseFunc, url string, body []byte, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	upstream.Header.Set("content-type", "application/json")
	c.applyDefaultHeaders(upstream)
	c.setAuth(ctx, upstream, r)
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	c.applyProtectedHeaders(upstream)
	c.applyOAuthBeta(ctx, upstream, r)
	proxy.ApplyWIFTokenType(ctx, upstream)
	proxy.ApplyIdentityHeader(ctx, upstream)
	proxy.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if v := r.Header.Get("accept"); v != "" {
		upstream.Header.Set("accept", v)
	}

	t := timing.TimingFrom(ctx)
	t.StampUpstreamRequest()
	resp, err := c.http.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream call: %w", err)
	}
	defer resp.Body.Close()
	t.StampUpstreamHeaders()
	// Surface subscription rate-limit headroom (anthropic-ratelimit-unified-*) to
	// the proxy's usage observer. Done for every response, including 429s where
	// the headroom signal matters most.
	providers.ObserveUpstreamHeaders(ctx, resp.Header)

	if resp.StatusCode >= 400 {
		bufBody, totalRead, drainErr := httputil.ReadCapped(resp.Body, providers.MaxBufferedErrorBytes)
		if len(bufBody) > 0 {
			t.StampUpstreamFirstByte()
		}
		if drainErr == nil {
			t.StampUpstreamEOF()
		}
		httputil.LogUpstreamStatus(
			ctx,
			"Upstream Anthropic returned error status",
			resp.StatusCode,
			"routed_model", decision.Model,
			"body_preview", httputil.PreviewBytes(bufBody),
			"body_total_bytes", totalRead,
		)
		errHeaders := http.Header{}
		providers.CopyUpstreamHeaders(httputil.HeaderCapture{H: errHeaders}, resp)
		return &providers.UpstreamErrorResponse{
			Status:  resp.StatusCode,
			Headers: errHeaders,
			Body:    bufBody,
		}
	}

	streamBody := io.Reader(resp.Body)
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("content-type")), "text/event-stream") {
		streamBody, err = inspectSSEPrelude(ctx, cancel, c.idleTimeout(), resp.Body, t)
		if err != nil {
			var buffered *providers.UpstreamErrorResponse
			if errors.As(err, &buffered) {
				errHeaders := http.Header{}
				providers.CopyUpstreamHeaders(httputil.HeaderCapture{H: errHeaders}, resp)
				buffered.Headers = errHeaders
				httputil.LogUpstreamStatus(
					ctx,
					"Upstream Anthropic returned SSE error event",
					buffered.Status,
					"routed_model", decision.Model,
					"body_preview", httputil.PreviewBytes(buffered.Body),
				)
			}
			return err
		}
	}

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)

	// Anthropic ping keepalives reset the byte-idle watchdog, so also arm
	// output-progress + throughput watchdogs — a ping-alive/zero-output stream
	// (prod: sonnet-5 stuck at 0 output, no failover) otherwise never aborts.
	if arm, ok := w.(providers.OutputProgressArmer); ok {
		outMark, outStop := httputil.StartIdleWatchdogCause(ctx, cancel, c.outputStallTimeout(), httputil.ErrUpstreamOutputStall)
		tpWindow, tpMinElapsed, tpMinDeltas := c.throughputParams()
		tpMark, tpStop := httputil.StartThroughputWatchdog(ctx, cancel, tpWindow, tpMinElapsed, tpMinDeltas, httputil.ErrUpstreamSlowThroughput)
		combined := func() {
			outMark()
			tpMark()
		}
		if arm.ArmOutputProgress(timing.FirstOutputMark(ctx, combined)) {
			defer outStop()
			defer tpStop()
		} else {
			outStop()
			tpStop()
		}
	}

	return httputil.StreamBody(ctx, cancel, c.idleTimeout(), streamBody, resp.StatusCode, w, t)
}

func (c *Client) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	url := proxy.EffectiveBaseURL(ctx, c.baseURL) + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	upstream, err := http.NewRequestWithContext(ctx, r.Method, url, bytes.NewReader(prep.Body))
	if err != nil {
		return fmt.Errorf("build upstream passthrough request: %w", err)
	}
	c.applyDefaultHeaders(upstream)
	if ct := r.Header.Get("content-type"); ct != "" {
		upstream.Header.Set("content-type", ct)
	}
	c.setAuth(ctx, upstream, r)
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	c.applyProtectedHeaders(upstream)
	c.applyOAuthBeta(ctx, upstream, r)
	proxy.ApplyWIFTokenType(ctx, upstream)
	proxy.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if v := r.Header.Get("accept"); v != "" {
		upstream.Header.Set("accept", v)
	}

	resp, err := c.http.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream passthrough call: %w", err)
	}
	defer resp.Body.Close()

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		return httputil.WritePassthroughError(r.Context(), w, resp, nil, nil, "Upstream Anthropic returned error status (passthrough)", "path", r.URL.Path)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

var _ providers.Client = (*Client)(nil)
