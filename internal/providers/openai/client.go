package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/timing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/http/httpguts"
)

const DefaultBaseURL = "https://api.openai.com"

const responsesContextOverflowCode = "context_length_exceeded"

const (
	chatGPTCodexBaseURL   = "https://chatgpt.com/backend-api/codex"
	codexResponsesPath    = "/responses"
	codexAccountIDHeader  = "ChatGPT-Account-ID"
	codexOpenAIBetaHeader = "OpenAI-Beta"
	codexOpenAIBetaValue  = "responses=experimental"
	codexOriginatorHeader = "originator"
	codexOriginatorValue  = "codex_cli_rs"
	codexSessionIDHeader  = "Session-Id"
	codexUserAgentHeader  = "User-Agent"
	codexUserAgentValue   = "codex_cli_rs"
)

func maxEffortToXhigh(body []byte, model string) []byte {
	if gjson.GetBytes(body, "reasoning.effort").String() != "max" {
		return body
	}
	if slices.Contains(router.Lookup(model).Reasoning().Levels, "max") {
		return body
	}
	out, err := sjson.SetBytes(body, "reasoning.effort", "xhigh")
	if err != nil {
		return body
	}
	return out
}

var codexUnsupportedParams = []string{
	"max_output_tokens", "temperature", "top_p", "metadata", "service_tier", "truncation",
}

func stripCodexUnsupportedParams(body []byte) []byte {
	for _, key := range codexUnsupportedParams {
		if !gjson.GetBytes(body, key).Exists() {
			continue
		}
		out, err := sjson.DeleteBytes(body, key)
		if err != nil {
			continue
		}
		body = out
	}
	return body
}

func codexSubscriptionCreds(ctx context.Context) *requestcontext.Credentials {
	creds := requestcontext.CredentialsFromContext(ctx)
	if creds != nil && creds.OAuth && len(creds.AccountID) > 0 {
		return creds
	}
	return nil
}

const responseHeaderTimeout = 120 * time.Second

type Client struct {
	allowCodexExtraCredits bool
	apiKey                 string
	baseURL                string
	http                   *http.Client
	modelIDMap             map[string]string
	sseIdleTimeout         time.Duration
	outputStall            time.Duration
	codexBaseURL           string
}

func NewClient(apiKey, baseURL string) *Client {
	return NewClientWithModelIDMap(apiKey, baseURL, nil)
}

func NewClientWithModelIDMap(apiKey, baseURL string, modelIDMap map[string]string) *Client {
	return newClientWithModelIDMap(apiKey, baseURL, responseHeaderTimeout, modelIDMap)
}

func (c *Client) SetCodexExtraCreditsAllowed(allowed bool) { c.allowCodexExtraCredits = allowed }

func (c *Client) SetCodexBaseURL(baseURL string) {
	if baseURL != "" {
		c.codexBaseURL = baseURL
	}
}

func NewClientWithResponseHeaderTimeout(apiKey, baseURL string, headerTimeout time.Duration) *Client {
	return newClientWithModelIDMap(apiKey, baseURL, headerTimeout, nil)
}

func newClientWithModelIDMap(apiKey, baseURL string, headerTimeout time.Duration, modelIDMap map[string]string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		apiKey:       apiKey,
		baseURL:      baseURL,
		modelIDMap:   modelIDMap,
		codexBaseURL: chatGPTCodexBaseURL,
		http:         httputil.NewClient(httputil.NewTransportWithResponseHeaderTimeout(5*time.Second, 5*time.Second, headerTimeout)),
	}
}

func NewClientWithTimeouts(apiKey, baseURL string, headerTimeout, sseIdleTimeout time.Duration) *Client {
	c := NewClientWithResponseHeaderTimeout(apiKey, baseURL, headerTimeout)
	c.sseIdleTimeout = sseIdleTimeout
	return c
}

func NewClientWithStallTimeouts(apiKey, baseURL string, headerTimeout, sseIdleTimeout, outputStall time.Duration) *Client {
	c := NewClientWithTimeouts(apiKey, baseURL, headerTimeout, sseIdleTimeout)
	c.outputStall = outputStall
	return c
}

func (c *Client) SupportsSubscriptions() bool { return true }

func (c *Client) DeploymentPrincipal() string {
	return providers.CredentialPrincipal(c.baseURL, c.apiKey)
}

func (c *Client) idleTimeoutFor(endpoint providers.Endpoint) time.Duration {
	if c.sseIdleTimeout > 0 {
		return c.sseIdleTimeout
	}
	if endpoint == providers.EndpointResponses {
		return httputil.DefaultResponsesSSEIdleTimeout
	}
	return httputil.DefaultSSEIdleTimeout
}

func (c *Client) outputStallTimeout() time.Duration {
	if c.outputStall > 0 {
		return c.outputStall
	}
	return httputil.DefaultResponsesOutputStallTimeout
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

func (c *Client) stallBudgetFor(endpoint providers.Endpoint, cause error) time.Duration {
	if errors.Is(cause, httputil.ErrUpstreamOutputStall) {
		return c.outputStallTimeout()
	}
	return c.idleTimeoutFor(endpoint)
}

func (c *Client) setAuth(ctx context.Context, upstream *http.Request, inbound *http.Request) {
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		upstream.Header.Set("Authorization", "Bearer "+string(creds.APIKey))
		return
	}
	if c.apiKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+c.apiKey)
		return
	}
	if creds := requestcontext.ExtractClientCredentials(providers.ProviderOpenAI, inbound.Header); creds != nil && creds.OAuth {
		return
	}
	v := inbound.Header.Get("authorization")
	if v == "" {
		return
	}
	if v = httputil.SanitizeInboundAuthHeader(v); v == "" {
		return
	}
	upstream.Header.Set("Authorization", v)
}

func (c *Client) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	codexCreds := codexSubscriptionCreds(ctx)
	if codexCreds != nil && prep.Endpoint != providers.EndpointResponses {
		return fmt.Errorf("codex subscription credentials require a Responses endpoint")
	}
	if codexCreds != nil && !requestcontext.CodexSubscriptionCanAttemptModel(decision.Model) {
		return fmt.Errorf("refusing Codex subscription credential for model without approved Codex funding eligibility %q", decision.Model)
	}
	useCodex := codexCreds != nil && prep.Endpoint == providers.EndpointResponses
	baseURL := requestcontext.EffectiveBaseURL(ctx, c.baseURL)
	path := "/v1/chat/completions"
	if prep.Endpoint == providers.EndpointResponses {
		path = "/v1/responses"
	}
	reqBody := rewriteModelField(prep.Body, c.modelIDMap)
	collectCodexResponse := false
	if useCodex {
		baseURL = c.codexBaseURL
		path = codexResponsesPath
		reqBody = stripCodexUnsupportedParams(reqBody)
		if !gjson.GetBytes(reqBody, "stream").Bool() {
			streamed, err := sjson.SetBytes(reqBody, "stream", true)
			if err != nil {
				return fmt.Errorf("force Codex stream: %w", err)
			}
			reqBody = streamed
			collectCodexResponse = true
		}
	} else if prep.Endpoint == providers.EndpointResponses {
		reqBody = maxEffortToXhigh(reqBody, decision.Model)
	}
	reqBody = requestcontext.ApplyModelAlias(ctx, reqBody, decision.Model)
	if useCodex {
		if err := c.checkCodexIncludedQuota(ctx, gjson.GetBytes(reqBody, "model").String()); err != nil {
			return err
		}
	}
	upstream, err := newInferenceRequest(ctx, baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	c.setAuth(ctx, upstream, r)
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	requestcontext.ApplyIdentityHeader(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if v := r.Header.Get("Accept"); v != "" {
		upstream.Header.Set("Accept", v)
	}
	if collectCodexResponse {
		upstream.Header.Set("Accept", "text/event-stream")
	}
	if useCodex {
		upstream.Header.Set(codexAccountIDHeader, string(codexCreds.AccountID))
		upstream.Header.Set(codexOpenAIBetaHeader, codexOpenAIBetaValue)
		upstream.Header.Set(codexOriginatorHeader, codexOriginatorValue)
		upstream.Header.Set(codexUserAgentHeader, codexUserAgentValue)
		affinity := requestcontext.NormalizeClientIdentifier(gjson.GetBytes(reqBody, "prompt_cache_key").String())
		if affinity != "" && httpguts.ValidHeaderFieldValue(affinity) {
			upstream.Header.Set(codexSessionIDHeader, affinity)
		}
	}

	t := timing.TimingFrom(ctx)
	t.StampUpstreamRequest()
	resp, err := c.http.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	t.StampUpstreamHeaders()
	providers.ObserveUpstreamHeaders(ctx, resp.Header)

	idleTimeout := c.idleTimeoutFor(prep.Endpoint)
	log := observability.FromContext(ctx)
	log.Debug("OpenAI upstream response",
		"status", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"),
		"transfer_encoding", resp.Header.Get("Transfer-Encoding"),
		"content_encoding", resp.Header.Get("Content-Encoding"),
		"upstream_request_id", resp.Header.Get("X-Request-Id"),
	)

	if resp.StatusCode >= 400 {
		mark, stop := httputil.StartIdleWatchdog(ctx, cancel, idleTimeout)
		body := &progressReader{r: resp.Body, mark: mark}
		bufBody, totalRead, drainErr := httputil.ReadCapped(body, providers.MaxBufferedErrorBytes)
		stop()
		if errors.Is(context.Cause(ctx), httputil.ErrUpstreamIdleTimeout) {
			logStreamStall(ctx, decision.Model, path, idleTimeout, totalRead, httputil.ErrUpstreamIdleTimeout)
		}
		if len(bufBody) > 0 {
			t.StampUpstreamFirstByte()
		}
		if drainErr == nil {
			t.StampUpstreamEOF()
		}
		httputil.LogUpstreamStatus(
			ctx,
			"Upstream OpenAI returned error status",
			resp.StatusCode,
			"base_url", c.baseURL,
			"upstream_host", upstream.URL.Host,
			"upstream_path", path,
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

	var collector *codexResponseCollector
	if collectCodexResponse {
		collector = newCodexResponseCollector(w)
		w = collector
	}
	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	status := resp.StatusCode

	if prep.Endpoint == providers.EndpointResponses {
		if arm, ok := w.(providers.OutputProgressArmer); ok {
			outMark, outStop := httputil.StartIdleWatchdogCause(ctx, cancel, c.outputStallTimeout(), httputil.ErrUpstreamOutputStall)
			if arm.ArmOutputProgress(timing.FirstOutputMark(ctx, outMark)) {
				defer outStop()
				if reasoningArmer, ok := w.(providers.ReasoningProgressArmer); ok {
					reasoningArmer.ArmReasoningProgress(outMark)
				}
			} else {
				outStop()
			}
		}
	}

	body := &progressReader{r: resp.Body}
	debug := log.Enabled(ctx, slog.LevelDebug)
	var opts []httputil.StreamOption
	var responseFrames []byte
	var terminalResponseError error
	if prep.Endpoint == providers.EndpointResponses {
		opts = append(opts, httputil.WithOnChunk(func(chunk []byte, first bool) {
			if debug && first {
				log.Debug("OpenAI upstream first chunk", "bytes", len(chunk))
			}
			if terminalResponseError != nil {
				return
			}
			responseFrames = append(responseFrames, chunk...)
			for {
				event, eventByteCount := sse.SplitNext(responseFrames)
				if eventByteCount == 0 {
					break
				}
				responseFrames = responseFrames[eventByteCount:]
				_, payload := sse.ParseEvent(event)
				if eventType := gjson.GetBytes(payload, "type").String(); eventType == "response.failed" || eventType == "error" {
					terminalResponseError = &providers.UpstreamErrorResponse{Status: responsesFailureStatus(payload), Body: bytes.Clone(payload)}
				}
			}
			if len(responseFrames) > 1024*1024 {
				responseFrames = nil
			}
		}))
	}

	if debug && prep.Endpoint != providers.EndpointResponses {
		opts = append(opts, httputil.WithOnChunk(func(chunk []byte, first bool) {
			if first {
				log.Debug("OpenAI upstream first chunk",
					"bytes", len(chunk),
				)
			}
		}))
	}
	streamErr := httputil.StreamBody(ctx, cancel, idleTimeout, body, status, w, t, opts...)
	if trailing := bytes.TrimSpace(responseFrames); terminalResponseError == nil && len(trailing) > 0 && trailing[0] == '{' &&
		gjson.GetBytes(trailing, "status").String() == "failed" {
		terminalResponseError = &providers.UpstreamErrorResponse{Status: responsesFailureStatus(trailing), Body: append([]byte(nil), trailing...)}
	}
	if streamErr == nil && terminalResponseError != nil {
		streamErr = terminalResponseError
	}
	if streamErr == nil && collector != nil {
		streamErr = collector.finish()
	}
	switch {
	case errors.Is(streamErr, httputil.ErrUpstreamIdleTimeout), errors.Is(streamErr, httputil.ErrUpstreamOutputStall):
		logStreamStall(ctx, decision.Model, path, c.stallBudgetFor(prep.Endpoint, streamErr), body.n, streamErr)
	case streamErr != nil:
		if debug {
			log.Debug("OpenAI upstream stream ended with error", "err", streamErr, "bytes_read", body.n)
		}
	case debug:
		log.Debug("OpenAI upstream stream complete", "bytes_total", body.n)
	}
	return streamErr
}

func responsesFailureStatus(failureBody []byte) int {
	for _, path := range []string{"error.code", "response.error.code", "code"} {
		if gjson.GetBytes(failureBody, path).String() == responsesContextOverflowCode {
			return http.StatusBadRequest
		}
	}
	return http.StatusBadGateway
}

type progressReader struct {
	r    io.Reader
	mark func()
	n    int64
}

func (p *progressReader) Read(buf []byte) (n int, err error) {
	n, err = p.r.Read(buf)
	if n > 0 {
		p.n += int64(n)
		if p.mark != nil {
			p.mark()
		}
	}
	return n, err
}

func logStreamStall(ctx context.Context, model, path string, budget time.Duration, bytesReceived int64, cause error) {
	stallKind := "byte_idle"
	if errors.Is(cause, httputil.ErrUpstreamOutputStall) {
		stallKind = "output_idle"
	}
	observability.FromContext(ctx).Error("OpenAI upstream stream stalled mid-response; aborting stream",
		"model", model,
		"provider", providers.ProviderOpenAI,
		"upstream_path", path,
		"stall_kind", stallKind,
		"budget_ms", budget.Milliseconds(),
		"bytes_received", bytesReceived,
	)
}

func (c *Client) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	url := requestcontext.EffectiveBaseURL(ctx, c.baseURL) + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	upstream, err := http.NewRequestWithContext(ctx, r.Method, url, bytes.NewReader(prep.Body))
	if err != nil {
		return fmt.Errorf("build upstream passthrough request: %w", err)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upstream.Header.Set("Content-Type", ct)
	}
	c.setAuth(ctx, upstream, r)
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
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
		return httputil.WritePassthroughError(ctx, w, resp, nil, nil, "Upstream OpenAI returned error status (passthrough)", "base_url", c.baseURL, "path", r.URL.Path)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

var _ providers.Client = (*Client)(nil)
