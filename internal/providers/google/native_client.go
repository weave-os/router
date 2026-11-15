package google

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/httputil"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/timing"
	"weave-os/router/internal/translate"
)

const NativeBaseURL = "https://generativelanguage.googleapis.com"

type NativeClient struct {
	apiKey         string
	baseURL        string
	http           *http.Client
	sseIdleTimeout time.Duration
	outputStall    time.Duration
}

func NewNativeClient(apiKey, baseURL string) *NativeClient {
	if baseURL == "" {
		baseURL = NativeBaseURL
	}
	return &NativeClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    httputil.NewClient(httputil.NewTransport(5*time.Second, 5*time.Second)),
	}
}

func NewNativeClientWithStallTimeouts(apiKey, baseURL string, sseIdleTimeout, outputStall time.Duration) *NativeClient {
	c := NewNativeClient(apiKey, baseURL)
	c.sseIdleTimeout = sseIdleTimeout
	c.outputStall = outputStall
	return c
}

func (c *NativeClient) idleTimeout() time.Duration {
	if c.sseIdleTimeout > 0 {
		return c.sseIdleTimeout
	}
	return httputil.DefaultSSEIdleTimeout
}

func (c *NativeClient) outputStallTimeout() time.Duration {
	if c.outputStall > 0 {
		return c.outputStall
	}
	return httputil.DefaultOutputStallTimeout
}

func (c *NativeClient) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	stream := prep.Headers.Get(translate.GeminiStreamHintHeader) == "true"
	prep.Headers.Del(translate.GeminiStreamHintHeader)

	method := ":generateContent"
	query := ""
	if stream {
		method = ":streamGenerateContent"
		query = "?alt=sse"
	}
	url := requestcontext.EffectiveBaseURL(ctx, c.baseURL) + "/v1beta/models/" + requestcontext.EffectiveUpstreamModel(ctx, decision.Model) + method + query

	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prep.Body))
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	upstream.Header.Set("Content-Type", "application/json")
	c.applyAPIKey(ctx, upstream)
	for k, vs := range prep.Headers {
		upstream.Header[http.CanonicalHeaderKey(k)] = vs
	}
	requestcontext.ApplyIdentityHeader(ctx, upstream)
	requestcontext.ApplyForwardedClientHeaders(ctx, upstream, r.Header)
	if stream {
		upstream.Header.Set("Accept", "text/event-stream")
	}

	t := timing.TimingFrom(ctx)
	t.StampUpstreamRequest()
	resp, err := c.http.Do(upstream)
	if err != nil {
		return fmt.Errorf("upstream call: %w", err)
	}
	defer resp.Body.Close()
	t.StampUpstreamHeaders()
	providers.ObserveUpstreamHeaders(ctx, resp.Header)

	if resp.StatusCode >= 400 {
		body, totalRead, readErr := httputil.ReadCapped(resp.Body, providers.MaxBufferedErrorBytes)
		if len(body) > 0 {
			t.StampUpstreamFirstByte()
		}
		if readErr == nil {
			t.StampUpstreamEOF()
		}
		headers := http.Header{}
		providers.CopyUpstreamHeaders(httputil.HeaderCapture{H: headers}, resp)
		return &providers.UpstreamErrorResponse{
			Status:     resp.StatusCode,
			Headers:    headers,
			Body:       body,
			BodyBytes:  totalRead,
			BodyCapped: totalRead > int64(len(body)),
		}
	}

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)

	if arm, ok := w.(providers.OutputProgressArmer); ok {
		outMark, outStop := httputil.StartIdleWatchdogCause(ctx, cancel, c.outputStallTimeout(), httputil.ErrUpstreamOutputStall)
		if arm.ArmOutputProgress(timing.FirstOutputMark(ctx, outMark)) {
			defer outStop()
		} else {
			outStop()
		}
	}

	return httputil.StreamBody(ctx, cancel, c.idleTimeout(), resp.Body, resp.StatusCode, w, t)
}

func (c *NativeClient) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	suffix := r.URL.Path
	if rest, ok := strings.CutPrefix(suffix, "/v1/"); ok {
		suffix = "/v1beta/" + rest
	} else if !strings.HasPrefix(suffix, "/v1beta") {
		suffix = "/v1beta" + suffix
	}
	url := requestcontext.EffectiveBaseURL(ctx, c.baseURL) + suffix
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
	c.applyAPIKey(ctx, upstream)
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
	defer resp.Body.Close()

	providers.CopyUpstreamHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode >= 400 {
		return httputil.WritePassthroughError(r.Context(), w, resp, nil, nil, "Upstream Google returned error status (passthrough)", "path", r.URL.Path)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (c *NativeClient) applyAPIKey(ctx context.Context, req *http.Request) {
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil && len(creds.APIKey) > 0 {
		req.Header.Set("x-goog-api-key", string(creds.APIKey))
		return
	}
	if c.apiKey != "" {
		req.Header.Set("x-goog-api-key", c.apiKey)
	}
}

var _ providers.Client = (*NativeClient)(nil)
