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
	"unicode/utf8"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

type ContentCaptureMode int

const (
	CaptureOff ContentCaptureMode = iota
	CaptureHashed
	CaptureFull
)

type InstallationCaptureModeContextKey struct{}

func (s *Service) effectiveCaptureMode(ctx context.Context) ContentCaptureMode {
	override, ok := ctx.Value(InstallationCaptureModeContextKey{}).(ContentCaptureMode)
	if !ok {
		return s.captureMode
	}
	return StricterCaptureMode(s.captureMode, override)
}

func StricterCaptureMode(a, b ContentCaptureMode) ContentCaptureMode {
	if b < a {
		return b
	}
	return a
}

func (s *Service) CaptureMode() ContentCaptureMode { return s.captureMode }

type ContentKind int

const (
	ContentKindRequest ContentKind = iota
	ContentKindResponse
)

type Redactor func(content string, kind ContentKind) string

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

type deferredCallLog struct {
	escalation  func(error)
	fn          func()
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

func (d *deferredCallLog) run() {
	if d != nil && d.fn != nil {
		d.fn()
	}
}

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

func (s *Service) recordCallLog(ctx context.Context, base []telemetry.Attr, routeMs int64, proxyErr error, reqBody, respBody []byte, respTruncated bool) {
	mode := s.effectiveCaptureMode(ctx)
	if mode == CaptureOff {
		s.recordPermanentError(ctx, base, proxyErr, len(reqBody))
		return
	}

	content := telemetry.NewAttrBuilder(7).
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
	case CaptureOff:
	}

	attrs := append(slices.Clone(base), content.Build()...)
	sev := telemetry.SeverityInfo
	if proxyErr != nil {
		sev = telemetry.SeverityError
	}
	telemetry.RecordLog(ctx, telemetry.LogRecord{
		Name:     "router.call",
		Time:     s.clock.Now(),
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

func (s *Service) recordPermanentError(ctx context.Context, base []telemetry.Attr, proxyErr error, requestBytes int) {
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

	attrs := make([]telemetry.Attr, 0, 20)
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
	telemetry.RecordLog(ctx, telemetry.LogRecord{
		Name: permanentErrorEventName, Time: s.clock.Now(),
		Severity: telemetry.SeverityError, Attrs: attrs,
	})
}

func permanentErrorDiagnosticAttrs(proxyErr error, requestBytes int, redact Redactor, responsesSurface ...bool) []telemetry.Attr {
	isResponsesSurface := len(responsesSurface) > 0 && responsesSurface[0]
	attrs := telemetry.NewAttrBuilder(8).
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
			allowed := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("-_.:/", char)
			if !allowed {
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
