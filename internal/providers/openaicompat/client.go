package openaicompat

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
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/timing"
)

const (
	DefaultBaseURL       = "https://openrouter.ai/api/v1"
	FireworksBaseURL     = "https://api.fireworks.ai/inference/v1"
	DeepInfraBaseURL     = "https://api.deepinfra.com/v1/openai"
	MakoraBaseURL        = "https://inference.makora.com/v1"
	MiniMaxGlobalBaseURL = "https://api.minimax.io/v1"
	MiniMaxCNBaseURL     = "https://api.minimaxi.com/v1"
	TogetherBaseURL      = "https://api.together.xyz/v1"
	XAIBaseURL           = "https://api.x.ai/v1"
	MetaBaseURL          = "https://api.meta.ai/v1"
	WaferBaseURL         = "https://pass.wafer.ai/v1"
)

func MiniMaxBaseURL(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "cn", "china":
		return MiniMaxCNBaseURL
	default:
		return MiniMaxGlobalBaseURL
	}
}

const DefaultGrokResponseHeaderTimeout = 90 * time.Second

var grokResponseHeaderTimeout = DefaultGrokResponseHeaderTimeout

type Config struct {
	GrokResponseHeaderTimeout time.Duration
}

func Configure(cfg Config) {
	grokResponseHeaderTimeout = cfg.GrokResponseHeaderTimeout
}

const BedrockMantleBaseURLTemplate = "https://bedrock-mantle.%s.api.aws/v1"

func BedrockMantleBaseURL(region string) string {
	if region == "" {
		region = "us-east-1"
	}
	return fmt.Sprintf(BedrockMantleBaseURLTemplate, region)
}

type Client struct {
	apiKey               string
	baseURL              string
	http                 *http.Client
	modelHTTP            *http.Client
	grokHTTP             *http.Client
	modelIDMap           map[string]string
	sseIdleTimeout       time.Duration
	outputStall          time.Duration
	throughputWindow     time.Duration
	throughputMinElapsed time.Duration
	throughputMinDeltas  int
	throughputOverride   bool
	defaultHeaders       http.Header
	protectedHeaders     http.Header
	versionMemo          providers.GatewayVersionMemo
}

type Option func(*Client)

func WithModelListHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client == nil {
			return
		}
		c.modelHTTP = client
	}
}

func NewClient(apiKey, baseURL string, opts ...Option) *Client {
	return NewClientWithModelIDMap(apiKey, baseURL, nil, opts...)
}

func NewClientWithModelIDMap(apiKey, baseURL string, modelIDMap map[string]string, opts ...Option) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return newClient(apiKey, baseURL, modelIDMap, opts...)
}

func NewGatewayClient(apiKey, baseURL string, opts ...Option) *Client {
	return newClient(apiKey, baseURL, nil, opts...)
}

func newClient(apiKey, baseURL string, modelIDMap map[string]string, opts ...Option) *Client {
	client := &Client{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		http:       httputil.NewClient(httputil.NewTransport(5*time.Second, 5*time.Second)),
		grokHTTP:   httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(5*time.Second, 5*time.Second, grokResponseHeaderTimeout)),
		modelHTTP:  httputil.NewDefaultModelDiscoveryClient(),
		modelIDMap: modelIDMap,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) DeploymentPrincipal() string {
	return providers.CredentialPrincipal(c.baseURL, c.apiKey)
}

func (c *Client) httpFor(model string) *http.Client {
	if strings.HasPrefix(model, "grok") && c.grokHTTP != nil {
		return c.grokHTTP
	}
	return c.http
}

func (c *Client) WithDefaultHeaders(h http.Header) *Client {
	c.defaultHeaders = h.Clone()
	return c
}

func (c *Client) WithProtectedHeaders(h http.Header) *Client {
	c.protectedHeaders = h.Clone()
	return c
}

func (c *Client) applyDefaultHeaders(req *http.Request) {
	for k, vs := range c.defaultHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

func (c *Client) applyProtectedHeaders(req *http.Request) {
	for k, vs := range c.protectedHeaders {
		req.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
}

func NewClientWithHeaderTimeouts(apiKey, baseURL string, defaultTimeout, grokTimeout time.Duration) *Client {
	c := NewClient(apiKey, baseURL)
	c.http = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(5*time.Second, 5*time.Second, defaultTimeout))
	c.grokHTTP = httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(5*time.Second, 5*time.Second, grokTimeout))
	return c
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

func NewClientWithThroughputGuard(apiKey, baseURL string, window, minElapsed time.Duration, minDeltas int) *Client {
	c := NewClient(apiKey, baseURL)
	c.throughputOverride = true
	c.throughputWindow = window
	c.throughputMinElapsed = minElapsed
	c.throughputMinDeltas = minDeltas
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

func stripSnowflakeCortexUnsupportedResponsesFields(body []byte, baseURL string, endpoint providers.Endpoint) []byte {
	if endpoint != providers.EndpointResponses || !providers.IsCortexBaseURL(baseURL) {
		return body
	}
	out, err := sjson.DeleteBytes(body, "reasoning.summary")
	if err != nil {
		return body
	}
	return out
}

func (c *Client) effectiveBaseURL(ctx context.Context) string {
	base := providers.NormalizeAzureOpenAIBaseURL(requestcontext.EffectiveBaseURL(ctx, c.baseURL))
	return providers.NormalizeCortexOpenAIBaseURL(base)
}

func (c *Client) setAuth(ctx context.Context, upstream *http.Request) {
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		upstream.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
		return
	}
	if c.apiKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func (c *Client) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	body := rewriteModelField(prep.Body, c.modelIDMap)
	body = requestcontext.ApplyModelAlias(ctx, body, decision.Model)
	baseURL := c.effectiveBaseURL(ctx)
	body = stripSnowflakeCortexUnsupportedResponsesFields(body, baseURL, prep.Endpoint)

	suffix := "/chat/completions"
	if prep.Endpoint == providers.EndpointResponses {
		suffix = "/responses"
	}

	urls := c.versionMemo.URLs(baseURL, suffix)
	firstErr := c.proxyTo(ctx, cancel, urls[0], baseURL, body, decision, prep, w, r)
	if len(urls) == 1 || !providers.IsUpstreamModelNotFound(firstErr) {
		return firstErr
	}
	err := c.proxyTo(ctx, cancel, urls[1], baseURL, body, decision, prep, w, r)
	if err == nil || !providers.IsUpstreamModelNotFound(err) {
		c.versionMemo.Learn(baseURL)
		return err
	}
	return firstErr
}

func (c *Client) proxyTo(ctx context.Context, cancel context.CancelCauseFunc, url, baseURL string, body []byte, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	c.applyDefaultHeaders(upstream)
	upstream.Header.Set("Content-Type", "application/json")
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	c.applyProtectedHeaders(upstream)
	c.setAuth(ctx, upstream)
	requestcontext.ApplyWIFTokenType(ctx, upstream)
	requestcontext.ApplyIdentityHeader(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if v := r.Header.Get("Accept"); v != "" {
		upstream.Header.Set("Accept", v)
	}

	t := timing.TimingFrom(ctx)
	t.StampUpstreamRequest()
	resp, err := c.httpFor(decision.Model).Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	t.StampUpstreamHeaders()

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
			"Upstream OpenAI-compatible provider returned error status",
			resp.StatusCode,
			"base_url", baseURL,
			"routed_model", decision.Model,
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
			if reasoningArmer, ok := w.(providers.ReasoningProgressArmer); ok {
				reasoningArmer.ArmReasoningProgress(outMark)
			}
		} else {
			outStop()
			tpStop()
		}
	}

	streamErr := httputil.StreamBody(ctx, cancel, c.idleTimeout(), resp.Body, resp.StatusCode, w, t)
	if errors.Is(streamErr, httputil.ErrUpstreamIdleTimeout) || errors.Is(streamErr, httputil.ErrUpstreamOutputStall) || errors.Is(streamErr, httputil.ErrUpstreamSlowThroughput) {
		logStreamStall(ctx, decision.Model, baseURL, streamErr)
	}
	return streamErr
}

func logStreamStall(ctx context.Context, model, baseURL string, cause error) {
	stallKind := "byte_idle"
	switch {
	case errors.Is(cause, httputil.ErrUpstreamOutputStall):
		stallKind = "output_idle"
	case errors.Is(cause, httputil.ErrUpstreamSlowThroughput):
		stallKind = "slow_throughput"
	}
	observability.FromContext(ctx).Error("Upstream OpenAI-compatible stream stalled mid-response; aborting stream",
		"model", model,
		"base_url", baseURL,
		"stall_kind", stallKind,
	)
}

func (c *Client) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	suffix := strings.TrimPrefix(r.URL.Path, "/v1")
	baseURL := c.effectiveBaseURL(ctx)
	url := baseURL + suffix
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	upstream, err := http.NewRequestWithContext(ctx, r.Method, url, bytes.NewReader(prep.Body))
	if err != nil {
		return fmt.Errorf("build upstream passthrough request: %w", err)
	}
	c.applyDefaultHeaders(upstream)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upstream.Header.Set("Content-Type", ct)
	}
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	c.applyProtectedHeaders(upstream)
	c.setAuth(ctx, upstream)
	requestcontext.ApplyWIFTokenType(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if v := r.Header.Get("Accept"); v != "" {
		upstream.Header.Set("Accept", v)
	}

	resp, err := c.http.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream passthrough call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		return httputil.WritePassthroughError(ctx, w, resp, nil, nil, "Upstream OpenAI-compatible provider returned error status (passthrough)", "base_url", baseURL, "path", r.URL.Path)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

var _ providers.Client = (*Client)(nil)
