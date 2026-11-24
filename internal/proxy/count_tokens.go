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
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

const countTokensUpstreamBudget = 5 * time.Second

func (s *Service) countTokens(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
	if _, gateway := requestcontext.UpstreamGatewayFrom(ctx); gateway || router.IsRoutingModel(gjson.GetBytes(body, "model").String()) {
		return writeLocalCountTokens(w, body)
	}
	if !s.credentials.AnthropicCredentialReachable(ctx, r.Header, s.engine.Clients().Has) {
		if err := writeLocalCountTokens(w, body); err == nil {
			return nil
		}
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

func countTokensShouldFallBack(err error, buf *bufferedResponse) bool {
	if buf.status == 0 {
		if err == nil || errors.Is(err, providers.ErrNotImplemented) || errors.Is(err, ErrProviderNotConfigured) {
			return false
		}
		return providers.IsRetryable(err) || errors.Is(err, context.DeadlineExceeded)
	}
	if buf.status < 400 {
		return err != nil
	}
	return providers.IsRetryableStatus(buf.status)
}

func isCountTokensRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/count_tokens")
}

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
