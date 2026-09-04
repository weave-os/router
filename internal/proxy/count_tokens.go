package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// countTokensUpstreamBudget bounds the upstream count_tokens attempt. It must
// leave headroom inside the route's overall timeout so a slow upstream still
// gets answered with the local estimate instead of the handler's deadline
// error: the SDK issues count_tokens as a pre-flight, so a hard failure here
// blocks the /v1/messages call behind it.
const countTokensUpstreamBudget = 5 * time.Second

// countTokens answers the Anthropic count_tokens pre-flight. The upstream is
// authoritative when it answers; it is skipped when no Anthropic credential is
// reachable, and its answer is replaced by the local estimate when the attempt
// fails for a reason a retry could fix (transport error, timeout, 408/429/5xx
// including 529 overload). Client-fault statuses (400, 401, 404, ...) are
// replayed verbatim so a bad request or key is still reported as such.
func (s *Service) countTokens(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
	if !s.anthropicCredentialReachable(ctx, r.Header) {
		if err := writeLocalCountTokens(w, body); err == nil {
			return nil
		}
		// Unparseable body: fall through so the client sees the same error it
		// would get from a credential-less passthrough today.
		return s.PassthroughToNamedProvider(ctx, providers.ProviderAnthropic, body, w, r)
	}

	upstreamCtx, cancel := context.WithTimeout(ctx, countTokensUpstreamBudget)
	defer cancel()
	buf := &bufferedResponse{header: make(http.Header)}
	upstreamErr := s.PassthroughToNamedProvider(upstreamCtx, providers.ProviderAnthropic, body, buf, r)

	if !countTokensShouldFallBack(upstreamErr, buf) {
		return buf.replay(w, upstreamErr)
	}
	if err := writeLocalCountTokens(w, body); err != nil {
		return buf.replay(w, upstreamErr)
	}
	observability.FromContext(ctx).Warn("count_tokens upstream failed; answered with local estimate",
		"upstream_status", buf.status,
		"upstream_err", upstreamErr,
	)
	return nil
}

// countTokensShouldFallBack reports whether the upstream attempt failed in a
// way the local estimate should paper over. A committed non-retryable status
// is the upstream's actual answer about the request and must reach the client,
// and a deployment fault (ErrNotImplemented, ErrProviderNotConfigured) must
// stay loud rather than be masked by an estimate.
func countTokensShouldFallBack(err error, buf *bufferedResponse) bool {
	if buf.status == 0 {
		if err == nil || errors.Is(err, providers.ErrNotImplemented) || errors.Is(err, ErrProviderNotConfigured) {
			return false
		}
		// Transport failure or the attempt's own deadline: the local estimate
		// is the retry. IsRetryable alone excludes deadlines because on the
		// routed path they mean the client's budget is gone; here the budget
		// is ours and deliberately shorter than the route's.
		return providers.IsRetryable(err) || errors.Is(err, context.DeadlineExceeded)
	}
	if buf.status < 400 {
		return err != nil
	}
	return providers.IsRetryableStatus(buf.status)
}

// isCountTokensRequest reports whether r is the Anthropic count_tokens
// pre-flight (POST /v1/messages/count_tokens).
func isCountTokensRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/count_tokens")
}

// writeLocalCountTokens answers a count_tokens request from the request body's
// byte-length token estimate, mirroring the Anthropic response shape.
func writeLocalCountTokens(w http.ResponseWriter, body []byte) error {
	env, err := translate.ParseAnthropic(body)
	if err != nil {
		return err
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = fmt.Fprintf(w, `{"input_tokens":%d}`, env.ContextOverflowTokenEstimate())
	return err
}

// bufferedResponse captures a provider's response so the caller can decide
// whether to forward it or substitute an answer before any byte reaches the
// client.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// replay forwards the captured response to w. When nothing was captured the
// upstream error is returned unchanged so the handler renders it.
func (b *bufferedResponse) replay(w http.ResponseWriter, err error) error {
	if b.status == 0 {
		return err
	}
	for k, vs := range b.header {
		w.Header()[k] = vs
	}
	w.WriteHeader(b.status)
	if _, werr := w.Write(b.body.Bytes()); werr != nil {
		return werr
	}
	return err
}
