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
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/timing"
)

const DefaultBaseURL = "https://api.anthropic.com"

const largePromptBodyBytes = 1 << 20

var largePromptResponseHeaderTimeout = httputil.TimeoutFromEnv("ROUTER_ANTHROPIC_LARGE_PROMPT_HEADER_TIMEOUT_SECONDS", 120*time.Second)

const WaferMessagesBaseURL = "https://pass.wafer.ai"

type AuthScheme int

const (
	AuthAPIKeyHeader AuthScheme = iota
	AuthBearer
)

type Option func(*Client)

func WithAuthScheme(scheme AuthScheme) Option {
	return func(c *Client) { c.authScheme = scheme }
}

func WithModelListHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client == nil {
			return
		}
		c.modelHTTP = client
	}
}

func (c *Client) WithDefaultHeaders(h http.Header) *Client {
	c.defaultHeaders = h.Clone()
	return c
}

func (c *Client) WithProtectedHeaders(h http.Header) *Client {
	c.protectedHeaders = h.Clone()
	return c
}

func (c *Client) WithModelIDMap(modelIDMap map[string]string) *Client {
	c.modelIDMap = modelIDMap
	return c
}

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
	apiKey               string
	baseURL              string
	http                 *http.Client
	modelHTTP            *http.Client
	largePromptHTTP      *http.Client
	authScheme           AuthScheme
	modelIDMap           map[string]string
	defaultHeaders       http.Header
	protectedHeaders     http.Header
	sseIdleTimeout       time.Duration
	outputStall          time.Duration
	throughputWindow     time.Duration
	throughputMinElapsed time.Duration
	throughputMinDeltas  int
	throughputOverride   bool
	versionMemo          providers.GatewayVersionMemo
}

func (c *Client) SupportsSubscriptions() bool { return c.authScheme == AuthAPIKeyHeader }

func NewClient(apiKey, baseURL string, opts ...Option) *Client {
	c := &Client{
		apiKey:          apiKey,
		baseURL:         baseURL,
		http:            httputil.NewClient(httputil.NewTransport(10*time.Second, 10*time.Second)),
		largePromptHTTP: httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(10*time.Second, 10*time.Second, largePromptResponseHeaderTimeout)),
		modelHTTP:       httputil.NewDefaultModelDiscoveryClient(),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.baseURL == "" && c.authScheme == AuthAPIKeyHeader {
		c.baseURL = DefaultBaseURL
	}
	return c
}

func NewClientWithHeaderTimeouts(apiKey, baseURL string, defaultTimeout, largePromptTimeout time.Duration) *Client {
	c := NewClient(apiKey, baseURL)
	c.http = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(10*time.Second, 10*time.Second, defaultTimeout))
	c.largePromptHTTP = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(10*time.Second, 10*time.Second, largePromptTimeout))
	return c
}

func (c *Client) httpFor(body []byte) *http.Client {
	if len(body) >= largePromptBodyBytes && c.largePromptHTTP != nil {
		return c.largePromptHTTP
	}
	return c.http
}

func NewClientWithStallTimeouts(apiKey, baseURL string, sseIdleTimeout, outputStall time.Duration) *Client {
	c := NewClient(apiKey, baseURL)
	c.sseIdleTimeout = sseIdleTimeout
	c.outputStall = outputStall
	return c
}

func (c *Client) idleTimeout() time.Duration {
	if c.sseIdleTimeout > 0 {
		return c.sseIdleTimeout
	}
	return httputil.DefaultSSEIdleTimeout
}

func (c *Client) outputStallTimeout() time.Duration {
	if c.outputStall > 0 {
		return c.outputStall
	}
	return httputil.DefaultOutputStallTimeout
}

func (c *Client) throughputParams() (window, minElapsed time.Duration, minDeltas int) {
	if c.throughputOverride {
		return c.throughputWindow, c.throughputMinElapsed, c.throughputMinDeltas
	}
	return httputil.DefaultThroughputWindow, httputil.DefaultThroughputMinElapsed, httputil.DefaultMinThroughputDeltasPerWindow
}

func (c *Client) applyDefaultHeaders(req *http.Request) {
	for k, vs := range c.defaultHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

func forwardClientUserAgent(upstream, inbound *http.Request) {
	if upstream.Header.Get("User-Agent") != "" {
		return
	}
	if v := inbound.UserAgent(); v != "" {
		upstream.Header.Set("User-Agent", v)
	}
}

func (c *Client) applyProtectedHeaders(req *http.Request) {
	for k, vs := range c.protectedHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

const oauthBetaToken = string(auth.AnthropicBetaOAuth)

const subscriptionTokenPrefix = "sk-ant-oat"

var errInboundSubscriptionRelay = errors.New("refusing to relay inbound Claude subscription bearer without resolved credentials")

func (c *Client) setAuth(ctx context.Context, upstream *http.Request, inbound *http.Request) {
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		if creds.OAuth || creds.AuthType == auth.AuthTypeAnthropicWIF || c.authScheme == AuthBearer {
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

func (c *Client) applyOAuthBeta(ctx context.Context, upstream, inbound *http.Request) {
	creds := requestcontext.CredentialsFromContext(ctx)
	federated := creds != nil && creds.AuthType == auth.AuthTypeAnthropicWIF
	if !federated && !c.subscriptionAuth(ctx, inbound) {
		return
	}
	upstream.Header.Set("anthropic-beta", mergeBeta(upstream.Header.Get("anthropic-beta"), oauthBetaToken))
}

func (c *Client) subscriptionAuth(ctx context.Context, inbound *http.Request) bool {
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		return creds.OAuth
	}
	if c.apiKey != "" {
		return false
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(inbound.Header.Get("authorization")), " ")
	return found && strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(strings.TrimSpace(token), subscriptionTokenPrefix)
}

func (c *Client) claudeSubscriptionAuth(ctx context.Context, inbound *http.Request) bool {
	if !c.subscriptionAuth(ctx, inbound) {
		return false
	}
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		return strings.HasPrefix(string(creds.APIKey), subscriptionTokenPrefix)
	}
	return true
}

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
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil && creds.CredentialError != nil {
		return creds.CredentialError
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	if requestcontext.CredentialsFromContext(ctx) == nil && c.authScheme != AuthBearer && c.subscriptionAuth(ctx, r) {
		return errInboundSubscriptionRelay
	}

	baseURL := requestcontext.EffectiveBaseURL(ctx, c.baseURL)
	body := rewriteModelField(prep.Body, c.modelIDMap)
	body = requestcontext.ApplyModelAlias(ctx, body, decision.Model)
	if c.claudeSubscriptionAuth(ctx, r) {
		body = ensureClaudeCodeIdentity(body)
	}

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
	requestcontext.ApplyWIFTokenType(ctx, upstream)
	requestcontext.ApplyIdentityHeader(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	forwardClientUserAgent(upstream, r)
	if v := r.Header.Get("accept"); v != "" {
		upstream.Header.Set("accept", v)
	}

	t := timing.TimingFrom(ctx)
	t.StampUpstreamRequest()
	resp, err := c.httpFor(body).Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	t.StampUpstreamHeaders()
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
			upstreamErrorLogMessage(requestcontext.EffectiveBaseURL(ctx, c.baseURL) != DefaultBaseURL, "Upstream Anthropic returned error status"),
			resp.StatusCode,
			"routed_model", decision.Model,
			"upstream_host", upstream.URL.Host,
			"body_preview", httputil.PreviewBytes(bufBody),
			"body_total_bytes", totalRead,
		)
		errHeaders := http.Header{}
		providers.CopyUpstreamHeaders(httputil.HeaderCapture{H: errHeaders}, resp)
		return &providers.UpstreamErrorResponse{
			Status:     resp.StatusCode,
			Headers:    errHeaders,
			Body:       bufBody,
			BodyBytes:  totalRead,
			BodyCapped: totalRead > int64(len(bufBody)),
			Rejection:  classifyRejection(resp.StatusCode, bufBody, prep.Body, body),
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
				buffered.Rejection = classifyRejection(buffered.Status, buffered.Body, prep.Body, body)
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
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil && creds.CredentialError != nil {
		return creds.CredentialError
	}
	if requestcontext.CredentialsFromContext(ctx) == nil && c.authScheme != AuthBearer && c.subscriptionAuth(ctx, r) {
		return errInboundSubscriptionRelay
	}
	passthroughBase := requestcontext.EffectiveBaseURL(ctx, c.baseURL)
	url := passthroughBase + r.URL.Path
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
	requestcontext.ApplyWIFTokenType(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	forwardClientUserAgent(upstream, r)
	if v := r.Header.Get("accept"); v != "" {
		upstream.Header.Set("accept", v)
	}

	resp, err := c.httpFor(prep.Body).Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream passthrough call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		return httputil.WritePassthroughError(ctx, w, resp, nil, nil, upstreamErrorLogMessage(passthroughBase != DefaultBaseURL, "Upstream Anthropic returned error status")+" (passthrough)", "upstream_host", upstream.URL.Host, "path", r.URL.Path)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

var _ providers.Client = (*Client)(nil)

func (c *Client) BaseURL() string { return c.baseURL }

func upstreamErrorLogMessage(viaGateway bool, direct string) string {
	if viaGateway {
		return "Upstream gateway returned error status"
	}
	return direct
}
