package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/websearch"

	"github.com/tidwall/gjson"
)

// nativeSearchLoggerContextKey carries the pre-bind logger so nested Responses
// dispatch can bind session fields without duplicating log keys.
type nativeSearchLoggerContextKey struct{}

// codexWebSearchContextKey keeps isolated helpers out of conversation pin state.
type codexWebSearchContextKey struct{}

const (
	nativeSearchResponseLimit = 2 * 1024 * 1024
	nativeSearchTimeout       = 90 * time.Second
)

func (s *Service) serveCodexWebSearch(ctx context.Context, body []byte, model string, stream bool, enabled map[string]struct{}, r *http.Request, w http.ResponseWriter) (bool, error) {
	if !websearch.IsClaudeCodeWebSearchHelper(body) || subscriptionAPIOnly(ctx) || subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return false, nil
	}
	if anyNativeServerToolProvider(enabled) && !requestcontext.CodexSubscriptionCanAttemptModel(model) {
		return false, nil
	}
	if _, ok := enabled[providers.ProviderOpenAI]; !ok {
		return false, nil
	}
	creds := ExtractClientCredentials(providers.ProviderOpenAI, r.Header)
	if !managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) && codexSubscriptionFromContext(ctx) == nil && (creds == nil || !creds.OAuth) {
		return false, nil
	}
	selected := model
	if !requestcontext.CodexSubscriptionCanAttemptModel(selected) {
		selected = CodexAutomaticModel
	}
	converted, err := websearch.ResponsesRequest(body, selected)
	if err != nil {
		return true, nativeSearchInvalidRequest(err)
	}
	callCtx, cancel := context.WithTimeout(ctx, nativeSearchTimeout)
	defer cancel()
	// The search bridge never authorizes API-key fallback, even for funded callers.
	callCtx = context.WithValue(callCtx, subscriptionOnlyAttemptKey{}, true)
	callCtx = context.WithValue(callCtx, codexWebSearchContextKey{}, true)
	if base, ok := ctx.Value(nativeSearchLoggerContextKey{}).(*slog.Logger); ok {
		callCtx = observability.WithLogger(callCtx, base)
	}
	req := r.Clone(callCtx)
	req.URL.Path = "/v1/responses"
	req.Header.Set(routingMarkerHeader, "off")
	req.Header.Del(ForceModelHeader)
	req.Body = http.NoBody
	capture := &nativeSearchBuffer{header: make(http.Header), maxCalls: websearch.CallLimit(body), cancel: cancel, calls: make(map[string]struct{})}
	err = s.ProxyOpenAIResponses(callCtx, converted, capture, req)
	copyNativeSearchHeaders(w, capture.header)
	if capture.err != nil {
		if errors.Is(capture.err, websearch.ErrCallLimitExceeded) {
			return true, nativeSearchInvalidRequest(capture.err)
		}
		return true, capture.err
	}
	if err != nil {
		var upstream *providers.UpstreamErrorResponse
		if errors.As(err, &upstream) {
			copyNativeSearchHeaders(w, upstream.Headers)
			return true, nativeSearchErrorResponse(upstream.Status, upstream.Body, err)
		}
		var status *providers.UpstreamStatusError
		if errors.As(err, &status) {
			copyNativeSearchHeaders(w, status.Headers)
			return true, nativeSearchErrorResponse(status.Status, status.Body, err)
		}
		return true, err
	}
	if capture.status >= 400 {
		return true, nativeSearchErrorResponse(capture.status, capture.body.Bytes(), nil)
	}
	result, err := websearch.ParseResponses(capture.body.Bytes(), body)
	if err != nil {
		if errors.Is(err, websearch.ErrCallLimitExceeded) {
			return true, nativeSearchInvalidRequest(err)
		}
		return true, err
	}
	servedModel := capture.header.Get(HeaderRouterModel)
	if servedModel == "" {
		servedModel = result.Model
	}
	if servedModel == "" {
		return true, errors.New("web search response is missing its served model")
	}
	query, _ := websearch.DetectSearchTurn(body)
	tool, _ := websearch.FindServerTool(body)
	message := websearch.SynthesizeMessage("msg_router_search_"+fmt.Sprintf("%x", time.Now().UnixNano()), servedModel, tool.Name, query, result, 0)
	observability.FromContext(ctx).Info("Served native web search through Codex subscription", "results", len(result.Results))
	return true, writeWebSearchMessage(w, message, stream)
}

type nativeSearchBuffer struct {
	header     http.Header
	body       bytes.Buffer
	err        error
	status     int
	maxCalls   int
	calls      map[string]struct{}
	scanOffset int
	scanner    sse.Scanner
	cancel     context.CancelFunc
}

func (b *nativeSearchBuffer) Header() http.Header { return b.header }
func (b *nativeSearchBuffer) WriteHeader(code int) {
	if b.status == 0 && code >= 200 {
		b.status = code
	}
}
func (b *nativeSearchBuffer) Flush() {}
func (b *nativeSearchBuffer) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if len(p) > nativeSearchResponseLimit-b.body.Len() {
		b.err = errors.New("web search response exceeds size limit")
		if b.cancel != nil {
			b.cancel()
		}
		return 0, b.err
	}
	if b.status == 0 {
		b.WriteHeader(http.StatusOK)
	}
	n, _ := b.body.Write(p)
	if b.maxCalls > 0 {
		for {
			event, consumed := b.scanner.Next(b.body.Bytes()[b.scanOffset:])
			if consumed == 0 {
				break
			}
			b.scanOffset += consumed
			_, data := sse.ParseEvent(event)
			value := gjson.ParseBytes(data)
			kind := value.Get("type").String()
			if kind != "response.web_search_call.in_progress" && !(kind == "response.output_item.added" && value.Get("item.type").String() == "web_search_call") {
				continue
			}
			id := value.Get("item_id").String()
			if id == "" {
				id = value.Get("item.id").String()
			}
			if id == "" {
				id = fmt.Sprintf("index:%d", value.Get("output_index").Int())
			}
			b.calls[id] = struct{}{}
			if len(b.calls) > b.maxCalls {
				b.err = websearch.ErrCallLimitExceeded
				if b.cancel != nil {
					b.cancel()
				}
				return n, b.err
			}
		}
	}
	return n, nil
}

func copyNativeSearchHeaders(w http.ResponseWriter, headers http.Header) {
	for _, name := range []string{HeaderRouterDecision, HeaderRouterProvider, HeaderRouterModel, HeaderRouterContextWindow, HeaderRouterContextEstimate, HeaderRouterContextReserve, HeaderRouterContextEstimateKind, HeaderRouterContextVersion, HeaderRouterCache, HeaderRouterFallbackFrom, HeaderRouterFallbackAttempt, HeaderRouterCostUSD, HeaderRouterCostInputUSD, HeaderRouterCostOutputUSD, HeaderRouterCacheReadTokens, HeaderRouterCacheCreationTokens, "X-Request-Id", "Retry-After"} {
		if values := headers.Values(name); len(values) > 0 {
			w.Header()[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
}

func nativeSearchInvalidRequest(err error) error {
	encoded, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": err.Error()}})
	return &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: encoded}
}

// nativeSearchErrorResponse normalizes buffered Responses failures for Messages
// clients, retaining status and cause while diagnostics are copied separately.
func nativeSearchErrorResponse(status int, body []byte, cause error) *providers.UpstreamErrorResponse {
	kind := providers.ProviderErrorTypeAPI
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = providers.ProviderErrorTypeInvalidRequest
	case http.StatusUnauthorized:
		kind = providers.ProviderErrorTypeAuthentication
	case http.StatusForbidden:
		kind = providers.ProviderErrorTypePermission
	case http.StatusNotFound:
		kind = providers.ProviderErrorTypeNotFound
	case http.StatusTooManyRequests:
		kind = providers.ProviderErrorTypeRateLimit
	case http.StatusRequestEntityTooLarge:
		kind = "request_too_large"
	case 529:
		kind = providers.ProviderErrorTypeOverloaded
	}
	if original, known := providers.KnownProviderErrorType(gjson.GetBytes(body, "error.type").String()); known && original != providers.ProviderErrorTypeServiceUnavailable {
		kind = original
	}
	message := "Upstream web search failed."
	for _, path := range []string{"error.message", "message", "detail"} {
		if value := gjson.GetBytes(body, path); value.Type == gjson.String && value.String() != "" {
			message = value.String()
			break
		}
	}
	encoded, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
	return &providers.UpstreamErrorResponse{Status: status, Body: encoded, Cause: cause}
}
