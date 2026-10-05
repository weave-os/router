package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
)

// ContentCaptureMode controls how much request/response content the router
// emits over OTLP as high-fidelity `router.call` log records.
type ContentCaptureMode int

const (
	// CaptureOff emits no `router.call` log records. Permanent upstream 4xx
	// failures emit a separate diagnostic event with the bounded upstream error body.
	// Default for self-hosted / OSS.
	CaptureOff ContentCaptureMode = iota
	// CaptureHashed emits log records with metadata + SHA-256 content hashes
	// but no raw text — dedup/cache analysis without exposing prompts.
	CaptureHashed
	// CaptureFull emits log records with full raw request/response bodies
	// (after the redaction hook). Default for Weave-managed deploys.
	CaptureFull
)

// InstallationCaptureModeContextKey carries the installation capture ceiling
// (ContentCaptureMode); absent when no override is set.
type InstallationCaptureModeContextKey struct{}

// effectiveCaptureMode returns the stricter of the deployment-wide setting and
// the per-installation override (minimum wins, so a tenant can only tighten).
func (s *Service) effectiveCaptureMode(ctx context.Context) ContentCaptureMode {
	override, ok := ctx.Value(InstallationCaptureModeContextKey{}).(ContentCaptureMode)
	if !ok {
		return s.captureMode
	}
	return StricterCaptureMode(s.captureMode, override)
}

// StricterCaptureMode returns the less permissive of two capture modes.
func StricterCaptureMode(a, b ContentCaptureMode) ContentCaptureMode {
	if b < a {
		return b
	}
	return a
}

// CaptureMode reports the deployment-wide capture setting, before any
// per-installation override.
func (s *Service) CaptureMode() ContentCaptureMode { return s.captureMode }

// ContentKind tells the redaction hook whether it is scrubbing a request or a
// response body, so callers can apply asymmetric policies.
type ContentKind int

const (
	// ContentKindRequest marks an inbound request body.
	ContentKindRequest ContentKind = iota
	// ContentKindResponse marks an outbound response body.
	ContentKindResponse
)

// Redactor scrubs sensitive content before it enters the OTLP export queue.
// A nil redactor passes content through unchanged.
type Redactor func(content string, kind ContentKind) string

// ParseCaptureMode maps a config string to a ContentCaptureMode. Unknown or
// empty values fall back to CaptureOff (the safe default).
func ParseCaptureMode(raw string) ContentCaptureMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "full":
		return CaptureFull
	case "hashed":
		return CaptureHashed
	default:
		return CaptureOff
	}
}

func (m ContentCaptureMode) String() string {
	switch m {
	case CaptureFull:
		return "full"
	case CaptureHashed:
		return "hashed"
	default:
		return "off"
	}
}

// maybeCaptureResponse wraps w with a content-capturing writer when capture is
// enabled, mirroring the exact bytes sent to the client (nil when off).
//
// ResponsesWriter translates and emits an eager prelude to its inner writer, so
// wrapping it externally would miss the prelude and capture pre-translation
// bytes; instead we splice the capture writer at its true client boundary.
func (s *Service) maybeCaptureResponse(ctx context.Context, w http.ResponseWriter) (http.ResponseWriter, *captureWriter) {
	if s.effectiveCaptureMode(ctx) == CaptureOff {
		return w, nil
	}
	if rw, ok := w.(*translate.ResponsesWriter); ok {
		var cw *captureWriter
		rw.WrapInner(func(inner http.ResponseWriter) http.ResponseWriter {
			cw = newCaptureWriter(inner, s.captureMaxBytes)
			return cw
		})
		return w, cw
	}
	cw := newCaptureWriter(w, s.captureMaxBytes)
	return cw, cw
}

// capturedResponse extracts the buffered response body. truncated is true when
// the body exceeded the capture cap (the buffer is then dropped).
func capturedResponse(c *captureWriter) (body []byte, truncated bool) {
	if c == nil {
		return nil, false
	}
	b, _, ok := c.captured()
	if !ok {
		return nil, true
	}
	return b, false
}

// deferredCallLog lets a wrapping handler run call-log emission after the
// response body is fully written, since /v1/responses' ResponsesWriter only
// calls Finalize after ProxyOpenAIChatCompletion returns (reading the
// captured body earlier would yield empty/partial content).
type deferredCallLog struct {
	escalation func(error)
	fn         func()
	// requestBody overrides the captured request body: ProxyOpenAIResponses
	// sets it to the client's original Responses JSON so io.request_body
	// matches, instead of the translated Chat Completions payload.
	requestBody []byte
}

type deferredCallLogKey struct{}

func withDeferredCallLog(ctx context.Context) (context.Context, *deferredCallLog) {
	h := &deferredCallLog{}
	return context.WithValue(ctx, deferredCallLogKey{}, h), h
}

func deferredCallLogFrom(ctx context.Context) *deferredCallLog {
	h, _ := ctx.Value(deferredCallLogKey{}).(*deferredCallLog)
	return h
}

// run invokes the deferred emit if one was registered. Safe on nil receiver
// and when no emit was stored (e.g. the request errored before any call).
func (d *deferredCallLog) run() {
	if d != nil && d.fn != nil {
		d.fn()
	}
}

// zdrLogField blanks content-bearing log values when the effective capture
// mode is off, keeping stdout logs content-free for zero-retention installs.
func (s *Service) zdrLogField(ctx context.Context, v string) string {
	if s.effectiveCaptureMode(ctx) == CaptureOff {
		return ""
	}
	return v
}

func (s *Service) redact(content []byte, kind ContentKind) string {
	if s.redactor == nil {
		return string(content)
	}
	return s.redactor(string(content), kind)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recordCallLog emits a high-fidelity `router.call` OTLP log record for one
// upstream call, reusing the upstream span's attributes as the metadata base
// and appending content attributes per capture mode. Under CaptureOff, only
// permanent upstream 4xx failures emit a metadata-only alert event. base is cloned
// before appending so the span's attributes aren't mutated.
func (s *Service) recordCallLog(ctx context.Context, base []*commonv1.KeyValue, routeMs int64, proxyErr error, reqBody, respBody []byte, respTruncated bool) {
	mode := s.effectiveCaptureMode(ctx)
	if mode == CaptureOff {
		s.recordPermanentError(ctx, base, proxyErr, len(reqBody))
		return
	}

	content := otel.NewAttrBuilder(7).
		Int64("latency.route_ms", routeMs).
		Int64("io.request_bytes", int64(len(reqBody))).
		Int64("io.response_bytes", int64(len(respBody))).
		Bool("io.truncated", respTruncated)

	switch mode {
	case CaptureFull:
		content.String("io.request_body", s.redact(reqBody, ContentKindRequest)).
			String("io.response_body", s.redact(respBody, ContentKindResponse))
	case CaptureHashed:
		content.String("io.request_sha256", sha256Hex(reqBody)).
			String("io.response_sha256", sha256Hex(respBody))
	}

	attrs := append(slices.Clone(base), content.Build()...)
	sev := otel.SeverityInfo
	if proxyErr != nil {
		sev = otel.SeverityError
	}
	otel.RecordLog(ctx, otel.LogRecord{
		Name:     "router.call",
		Time:     time.Now(),
		Severity: sev,
		Attrs:    attrs,
	})
}

const permanentErrorEventName = "router.permanent_error"

const maxLoggedErrorResponseBytes = 4 << 10

type permanentErrorClass string

const (
	permanentErrorClassUnclassified         permanentErrorClass = "unclassified"
	permanentErrorClassInvalidRequest       permanentErrorClass = "invalid_request"
	permanentErrorClassAuthentication       permanentErrorClass = "authentication"
	permanentErrorClassPermission           permanentErrorClass = "permission"
	permanentErrorClassNotFound             permanentErrorClass = "not_found"
	permanentErrorClassRateLimit            permanentErrorClass = "rate_limit"
	permanentErrorClassProviderError        permanentErrorClass = "provider_error"
	permanentErrorClassProviderOverloaded   permanentErrorClass = "provider_overloaded"
	permanentErrorClassProviderBilling      permanentErrorClass = "provider_billing_blocked"
	permanentErrorClassCapabilityRejection  permanentErrorClass = "capability_rejection"
	permanentErrorClassSchemaRejection      permanentErrorClass = "schema_rejection"
	permanentErrorClassThoughtSignature     permanentErrorClass = "thought_signature_rejection"
	permanentErrorClassOutputConfigFormat   permanentErrorClass = "output_config_format_rejection"
	permanentErrorClassPromptCacheKey       permanentErrorClass = "prompt_cache_key_rejection"
	permanentErrorClassResponsesUnsupported permanentErrorClass = "responses_unsupported"
)

type requestAPISurface string

const requestAPISurfaceResponses requestAPISurface = "openai_responses"

type responsesSurfaceContextKey struct{}

type errorBodyFormat string

const (
	errorBodyFormatEmpty   errorBodyFormat = "empty"
	errorBodyFormatJSON    errorBodyFormat = "json"
	errorBodyFormatNonJSON errorBodyFormat = "non_json"
)

type requestSizeBucket string

const (
	requestSizeBucketEmpty        requestSizeBucket = "empty"
	requestSizeBucketUnder16KiB   requestSizeBucket = "under_16_kib"
	requestSizeBucket16To64KiB    requestSizeBucket = "16_to_64_kib"
	requestSizeBucket64To256KiB   requestSizeBucket = "64_to_256_kib"
	requestSizeBucket256KiBTo1MiB requestSizeBucket = "256_kib_to_1_mib"
	requestSizeBucketAtLeast1MiB  requestSizeBucket = "at_least_1_mib"
)

type upstreamCorrelationHeader string

const (
	upstreamCorrelationHeaderSnowflakeQueryID   upstreamCorrelationHeader = "X-Snowflake-Query-Id"
	upstreamCorrelationHeaderSnowflakeRequestID upstreamCorrelationHeader = "X-Snowflake-Request-Id"
	upstreamCorrelationHeaderRequestID          upstreamCorrelationHeader = "X-Request-Id"
	upstreamCorrelationHeaderRequest            upstreamCorrelationHeader = "Request-Id"
)

// recordPermanentError includes a bounded provider error response for diagnosis.
func (s *Service) recordPermanentError(ctx context.Context, base []*commonv1.KeyValue, proxyErr error, requestBytes int) {
	if proxyErr == nil {
		return
	}
	var status int64
	for _, attr := range base {
		if attr.Key == "upstream.status_code" {
			status = attr.Value.GetIntValue()
			break
		}
	}
	if status < 400 || status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests {
		return
	}

	attrs := make([]*commonv1.KeyValue, 0, 20)
	responsesSurface := false
	for _, attr := range base {
		switch attr.Key {
		case "request_id", "external_id", "client.session_id", "router_user_id", "decision.model", "decision.provider", "dispatch.primary_model", "dispatch.primary_provider", "dispatch.final_provider", "request.message_count", "request.has_tools", "request.api_surface", "routing.cross_format", "routing.turn_type", "upstream.status_code":
			attrs = append(attrs, attr)
			if attr.Key == "request.api_surface" && attr.Value.GetStringValue() == string(requestAPISurfaceResponses) {
				responsesSurface = true
			}
		}
	}
	attrs = append(attrs, permanentErrorDiagnosticAttrs(proxyErr, requestBytes, s.redactor, responsesSurface)...)
	otel.RecordLog(ctx, otel.LogRecord{
		Name: permanentErrorEventName, Time: time.Now(),
		Severity: otel.SeverityError, Attrs: attrs,
	})
}

func permanentErrorDiagnosticAttrs(proxyErr error, requestBytes int, redact Redactor, responsesSurface ...bool) []*commonv1.KeyValue {
	isResponsesSurface := len(responsesSurface) > 0 && responsesSurface[0]
	attrs := otel.NewAttrBuilder(8).
		String("upstream.error_class", string(classifyPermanentError(proxyErr, isResponsesSurface))).
		String("request.size_bucket", string(requestSizeBucketFor(requestBytes)))

	bufferedErr := errorResponseFor(proxyErr)
	if bufferedErr == nil {
		return attrs.String("upstream.error_body_format", string(errorBodyFormatEmpty)).Build()
	}

	bodyFormat := errorBodyFormatNonJSON
	if len(bufferedErr.Body) == 0 {
		bodyFormat = errorBodyFormatEmpty
	} else if trimmedBody := strings.TrimSpace(string(bufferedErr.Body)); json.Valid(bufferedErr.Body) || strings.HasPrefix(trimmedBody, "{") || strings.HasPrefix(trimmedBody, "[") {
		bodyFormat = errorBodyFormatJSON
	}
	attrs.String("upstream.error_body_format", string(bodyFormat)).
		Int64("upstream.error_body_bytes", bufferedErr.BodyBytes).
		Bool("upstream.error_body_capped", bufferedErr.Capped)
	errorResponse, errorResponseTruncated := boundedErrorResponse(bufferedErr.Body, redact)
	attrs.String("upstream.error_response", errorResponse).
		Bool("upstream.error_response_truncated", errorResponseTruncated)
	if upstreamRequestID := safeUpstreamRequestID(bufferedErr.Headers); upstreamRequestID != "" {
		attrs.String("upstream.request_id", upstreamRequestID)
	}
	return attrs.Build()
}

func boundedErrorResponse(body []byte, redact Redactor) (string, bool) {
	errorResponse := string(body)
	if redact != nil {
		errorResponse = redact(errorResponse, ContentKindResponse)
	}
	errorResponse = strings.ToValidUTF8(errorResponse, "�")
	if len(errorResponse) <= maxLoggedErrorResponseBytes {
		return errorResponse, false
	}
	truncatedAt := maxLoggedErrorResponseBytes
	for !utf8.ValidString(errorResponse[:truncatedAt]) {
		truncatedAt--
	}
	return errorResponse[:truncatedAt], true
}

type permanentErrorBody struct {
	Headers   http.Header
	Body      []byte
	BodyBytes int64
	Capped    bool
}

func errorResponseFor(proxyErr error) *permanentErrorBody {
	var buffered *providers.UpstreamErrorResponse
	if errors.As(proxyErr, &buffered) {
		bodyBytes := buffered.BodyBytes
		if bodyBytes == 0 {
			bodyBytes = int64(len(buffered.Body))
		}
		return &permanentErrorBody{
			Headers: buffered.Headers, Body: buffered.Body,
			BodyBytes: bodyBytes, Capped: buffered.BodyCapped,
		}
	}
	var passthrough *providers.UpstreamStatusError
	if errors.As(proxyErr, &passthrough) {
		return &permanentErrorBody{
			Headers: passthrough.Headers, Body: passthrough.Body,
			BodyBytes: passthrough.BodyBytes, Capped: passthrough.BodyBytes > int64(len(passthrough.Body)),
		}
	}
	return nil
}

func requestSizeBucketFor(requestBytes int) requestSizeBucket {
	switch {
	case requestBytes == 0:
		return requestSizeBucketEmpty
	case requestBytes < 16<<10:
		return requestSizeBucketUnder16KiB
	case requestBytes < 64<<10:
		return requestSizeBucket16To64KiB
	case requestBytes < 256<<10:
		return requestSizeBucket64To256KiB
	case requestBytes < 1<<20:
		return requestSizeBucket256KiBTo1MiB
	default:
		return requestSizeBucketAtLeast1MiB
	}
}

func classifyPermanentError(proxyErr error, responsesSurface bool) permanentErrorClass {
	switch {
	case providers.IsUpstreamOutputConfigFormatRejection(proxyErr):
		return permanentErrorClassOutputConfigFormat
	case providers.IsUpstreamThoughtSignatureRejection(proxyErr):
		return permanentErrorClassThoughtSignature
	case providers.IsUpstreamSchemaRejection(proxyErr):
		return permanentErrorClassSchemaRejection
	case providers.IsUpstreamCapabilityRejection(proxyErr):
		return permanentErrorClassCapabilityRejection
	case providers.IsUpstreamPromptCacheKeyRejection(proxyErr):
		return permanentErrorClassPromptCacheKey
	case responsesSurface && providers.IsUpstreamResponsesUnsupported(proxyErr):
		return permanentErrorClassResponsesUnsupported
	case providers.IsUpstreamModelNotFound(proxyErr):
		return permanentErrorClassNotFound
	case providers.IsUpstreamProviderBillingBlocked(proxyErr):
		return permanentErrorClassProviderBilling
	case providers.IsUpstreamRateLimited(proxyErr):
		return permanentErrorClassRateLimit
	}

	bufferedErr := errorResponseFor(proxyErr)
	if bufferedErr == nil {
		return permanentErrorClassUnclassified
	}
	for _, path := range []string{"error.type", "type"} {
		switch errorType, ok := providers.KnownProviderErrorType(gjson.GetBytes(bufferedErr.Body, path).String()); {
		case !ok:
			continue
		case errorType == providers.ProviderErrorTypeInvalidRequest:
			return permanentErrorClassInvalidRequest
		case errorType == providers.ProviderErrorTypeAuthentication:
			return permanentErrorClassAuthentication
		case errorType == providers.ProviderErrorTypePermission:
			return permanentErrorClassPermission
		case errorType == providers.ProviderErrorTypeNotFound:
			return permanentErrorClassNotFound
		case errorType == providers.ProviderErrorTypeRateLimit:
			return permanentErrorClassRateLimit
		case errorType == providers.ProviderErrorTypeAPI:
			return permanentErrorClassProviderError
		case errorType == providers.ProviderErrorTypeOverloaded, errorType == providers.ProviderErrorTypeServiceUnavailable:
			return permanentErrorClassProviderOverloaded
		}
	}
	if gjson.GetBytes(bufferedErr.Body, "error.status").String() == "INVALID_ARGUMENT" {
		return permanentErrorClassInvalidRequest
	}
	return permanentErrorClassUnclassified
}

func safeUpstreamRequestID(headers http.Header) string {
	for _, headerName := range []upstreamCorrelationHeader{
		upstreamCorrelationHeaderSnowflakeQueryID,
		upstreamCorrelationHeaderSnowflakeRequestID,
		upstreamCorrelationHeaderRequestID,
		upstreamCorrelationHeaderRequest,
	} {
		requestID := strings.TrimSpace(headers.Get(string(headerName)))
		if requestID == "" || len(requestID) > 128 {
			continue
		}
		for _, char := range requestID {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("-_.:/", char)) {
				requestID = ""
				break
			}
		}
		if requestID != "" {
			return requestID
		}
	}
	return ""
}
