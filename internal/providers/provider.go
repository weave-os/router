package providers

import (
	"context"
	"errors"
	"fmt"
	"github.com/tidwall/gjson"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"weave-os/router/internal/router"
)

type SubscriptionTransport interface {
	SupportsSubscriptions() bool
}

type UpstreamHeaderObserver func(context.Context, http.Header)

type upstreamHeaderObserverKey struct{}

func WithUpstreamHeaderObserver(ctx context.Context, obs UpstreamHeaderObserver) context.Context {
	if obs == nil {
		return ctx
	}
	return context.WithValue(ctx, upstreamHeaderObserverKey{}, obs)
}

func ObserveUpstreamHeaders(ctx context.Context, h http.Header) {
	if obs, ok := ctx.Value(upstreamHeaderObserverKey{}).(UpstreamHeaderObserver); ok && obs != nil {
		obs(ctx, h)
	}
}

const (
	ProviderAnthropic        = "anthropic"
	ProviderOpenAI           = "openai"
	ProviderGoogle           = "google"
	ProviderOpenRouter       = "openrouter"
	ProviderTrustedRouter    = "trustedrouter"
	ProviderFireworks        = "fireworks"
	ProviderDeepInfra        = "deepinfra"
	ProviderBedrock          = "bedrock"
	ProviderMakora           = "makora"
	ProviderMiniMax          = "minimax"
	ProviderTogether         = "together"
	ProviderXAI              = "xai"
	ProviderMeta             = "meta"
	ProviderWafer            = "wafer"
	ProviderWaferAnthropic   = "wafer_anthropic"
	ProviderAnthropicGateway = "anthropic_gateway"
	ProviderOpenAIGateway    = "openai_gateway"
)

type TranslationFamily int

const (
	FamilyUnknown TranslationFamily = iota
	FamilyAnthropic
	FamilyOpenAICompat
	FamilyGemini
)

var ProviderFamilies = map[string]TranslationFamily{
	ProviderAnthropic:     FamilyAnthropic,
	ProviderOpenAI:        FamilyOpenAICompat,
	ProviderGoogle:        FamilyGemini,
	ProviderOpenRouter:    FamilyOpenAICompat,
	ProviderTrustedRouter: FamilyOpenAICompat,
	ProviderFireworks:     FamilyOpenAICompat,
	ProviderDeepInfra:     FamilyOpenAICompat,
	ProviderBedrock:       FamilyOpenAICompat,
	ProviderMakora:        FamilyOpenAICompat,
	ProviderMiniMax:       FamilyOpenAICompat,
	ProviderTogether:      FamilyOpenAICompat,
	ProviderXAI:           FamilyOpenAICompat,
	ProviderMeta:          FamilyOpenAICompat,
	ProviderWafer:         FamilyOpenAICompat,

	ProviderWaferAnthropic:   FamilyAnthropic,
	ProviderAnthropicGateway: FamilyAnthropic,
	ProviderOpenAIGateway:    FamilyOpenAICompat,
}

func FamilyFor(provider string) TranslationFamily {
	return ProviderFamilies[provider]
}

func IsOpenAICompat(provider string) bool {
	return FamilyFor(provider) == FamilyOpenAICompat
}

func IsGateway(provider string) bool {
	switch provider {
	case ProviderAnthropicGateway, ProviderOpenAIGateway:
		return true
	default:
		return false
	}
}

func SupportsAnthropicServerTools(provider string) bool {
	return FamilyFor(provider) == FamilyAnthropic && !IsGateway(provider)
}

func AllProviders() []string {
	out := make([]string, 0, len(ProviderFamilies))
	for p := range ProviderFamilies {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func ValidateDispatchable(registered []string) error {
	missing := make([]string, 0)
	for _, p := range registered {
		if FamilyFor(p) == FamilyUnknown {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("providers missing a ProviderFamilies entry (add them to internal/providers/provider.go): %s", strings.Join(missing, ", "))
}

var APIKeyEnvVars = map[string]string{
	ProviderAnthropic:        "ANTHROPIC_API_KEY",
	ProviderOpenAI:           "OPENAI_API_KEY",
	ProviderGoogle:           "GOOGLE_API_KEY",
	ProviderOpenRouter:       "OPENROUTER_API_KEY",
	ProviderTrustedRouter:    "TRUSTEDROUTER_API_KEY",
	ProviderFireworks:        "FIREWORKS_API_KEY",
	ProviderDeepInfra:        "DEEPINFRA_API_KEY",
	ProviderBedrock:          "AWS_BEARER_TOKEN_BEDROCK",
	ProviderMakora:           "MAKORA_API_KEY",
	ProviderMiniMax:          "MINIMAX_API_KEY",
	ProviderTogether:         "TOGETHER_API_KEY",
	ProviderXAI:              "XAI_API_KEY",
	ProviderMeta:             "META_API_KEY",
	ProviderWafer:            "WAFER_API_KEY",
	ProviderWaferAnthropic:   "WAFER_API_KEY",
	ProviderAnthropicGateway: "ANTHROPIC_GATEWAY_TOKEN",
	ProviderOpenAIGateway:    "OPENAI_GATEWAY_TOKEN",
}

func APIKeyEnvVar(provider string) string {
	return APIKeyEnvVars[provider]
}

var baseURLRequiredProviders = map[string]struct{}{
	ProviderAnthropicGateway: {},
	ProviderOpenAIGateway:    {},
}

func RequiresBaseURL(provider string) bool {
	_, ok := baseURLRequiredProviders[provider]
	return ok
}

func OutputExcludesReasoning(provider string) bool {
	return provider == ProviderXAI
}

var CacheTTL = map[string]time.Duration{
	ProviderAnthropic:        time.Hour,
	ProviderOpenAI:           5 * time.Minute,
	ProviderGoogle:           5 * time.Minute,
	ProviderOpenRouter:       5 * time.Minute,
	ProviderFireworks:        5 * time.Minute,
	ProviderDeepInfra:        5 * time.Minute,
	ProviderBedrock:          5 * time.Minute,
	ProviderXAI:              5 * time.Minute,
	ProviderMeta:             5 * time.Minute,
	ProviderWafer:            5 * time.Minute,
	ProviderWaferAnthropic:   5 * time.Minute,
	ProviderAnthropicGateway: 5 * time.Minute,
	ProviderOpenAIGateway:    5 * time.Minute,
}

const DefaultCacheTTL = 5 * time.Minute

func CacheTTLFor(provider string) time.Duration {
	if ttl, ok := CacheTTL[provider]; ok {
		return ttl
	}
	return DefaultCacheTTL
}

var HopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

func CopyUpstreamHeaders(w http.ResponseWriter, resp *http.Response) {
	dynamicHop := make(map[string]struct{})
	for _, v := range resp.Header.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				dynamicHop[http.CanonicalHeaderKey(name)] = struct{}{}
			}
		}
	}
	for k, vs := range resp.Header {
		canon := http.CanonicalHeaderKey(k)
		if _, hop := HopByHopHeaders[canon]; hop {
			continue
		}
		if _, hop := dynamicHop[canon]; hop {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

var ErrNotImplemented = errors.New("provider: not implemented")

type UpstreamStatusError struct {
	Status    int
	Headers   http.Header
	Body      []byte
	BodyBytes int64
	Cause     error
}

func (e *UpstreamStatusError) Error() string {
	return fmt.Sprintf("upstream returned status %d", e.Status)
}

func (e *UpstreamStatusError) Unwrap() error { return e.Cause }

type UpstreamErrorResponse struct {
	Status     int
	Headers    http.Header
	Body       []byte
	BodyBytes  int64
	BodyCapped bool
	Cause      error
	Rejection  UpstreamRejection
}

type ProviderErrorType string

const (
	ProviderErrorTypeInvalidRequest     ProviderErrorType = "invalid_request_error"
	ProviderErrorTypeAuthentication     ProviderErrorType = "authentication_error"
	ProviderErrorTypePermission         ProviderErrorType = "permission_error"
	ProviderErrorTypeNotFound           ProviderErrorType = "not_found_error"
	ProviderErrorTypeRateLimit          ProviderErrorType = "rate_limit_error"
	ProviderErrorTypeAPI                ProviderErrorType = "api_error"
	ProviderErrorTypeOverloaded         ProviderErrorType = "overloaded_error"
	ProviderErrorTypeServiceUnavailable ProviderErrorType = "service_unavailable"
)

func KnownProviderErrorType(value string) (ProviderErrorType, bool) {
	errorType := ProviderErrorType(value)
	switch errorType {
	case ProviderErrorTypeInvalidRequest,
		ProviderErrorTypeAuthentication,
		ProviderErrorTypePermission,
		ProviderErrorTypeNotFound,
		ProviderErrorTypeRateLimit,
		ProviderErrorTypeAPI,
		ProviderErrorTypeOverloaded,
		ProviderErrorTypeServiceUnavailable:
		return errorType, true
	default:
		return "", false
	}
}

func (e *UpstreamErrorResponse) Error() string {
	return fmt.Sprintf("upstream returned status %d (buffered)", e.Status)
}

func (e *UpstreamErrorResponse) Unwrap() error { return e.Cause }

type QuotaVerificationFailure string

const (
	QuotaVerificationFailureRequest         QuotaVerificationFailure = "request"
	QuotaVerificationFailureTransport       QuotaVerificationFailure = "transport"
	QuotaVerificationFailureTimeout         QuotaVerificationFailure = "timeout"
	QuotaVerificationFailureEndpoint        QuotaVerificationFailure = "endpoint"
	QuotaVerificationFailureInvalidResponse QuotaVerificationFailure = "invalid_response"
)

type QuotaVerificationUnavailableError struct {
	Failure        QuotaVerificationFailure
	UpstreamStatus int
}

func (e *QuotaVerificationUnavailableError) Error() string {
	return fmt.Sprintf("subscription quota verification unavailable: %s (upstream status %d)", e.Failure, e.UpstreamStatus)
}

const MaxBufferedErrorBytes = 64 * 1024

var ErrUpstreamIdleTimeout = errors.New("upstream sse idle timeout")

var ErrUpstreamOutputStall = errors.New("upstream sse output stall")

var ErrUpstreamSlowThroughput = errors.New("upstream sse slow throughput")

var ErrUpstreamEmptyCompletion = errors.New("upstream returned an empty completion")

func IsRetryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests:
		return true
	}
	return status >= 500 && status <= 599
}

func IsUpstreamRateLimited(err error) bool {
	var buffered *UpstreamErrorResponse
	return errors.As(err, &buffered) && buffered.Status == http.StatusTooManyRequests
}

func RetryAfter(err error, now time.Time) (delay time.Duration, ok bool) {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Headers == nil {
		return 0, false
	}
	raw := strings.TrimSpace(buffered.Headers.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	if seconds, parseErr := strconv.Atoi(raw); parseErr == nil {
		if seconds < 0 {
			return 0, false
		}
		if seconds > int(math.MaxInt64/int64(time.Second)) {
			return math.MaxInt64, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	at, parseErr := http.ParseTime(raw)
	if parseErr != nil {
		return 0, false
	}
	if !at.After(now) {
		return 0, true
	}
	return at.Sub(now), true
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if IsResponseHeaderTimeout(err) {
		return true
	}
	if errors.Is(err, ErrUpstreamIdleTimeout) || errors.Is(err, ErrUpstreamOutputStall) || errors.Is(err, ErrUpstreamSlowThroughput) || errors.Is(err, ErrUpstreamEmptyCompletion) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var buffered *UpstreamErrorResponse
	if errors.As(err, &buffered) {
		return IsRetryableStatus(buffered.Status)
	}
	var flushed *UpstreamStatusError
	if errors.As(err, &flushed) {
		return false
	}
	return true
}

func IsResponseHeaderTimeout(err error) bool {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr.Err == nil {
		return false
	}
	return strings.Contains(urlErr.Err.Error(), "timeout awaiting response headers")
}

func IsHTTP2ConnectionLoss(err error) bool {
	var streamErr http2.StreamError
	var connErr http2.ConnectionError
	if errors.As(err, &streamErr) || errors.As(err, &connErr) {
		return true
	}
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "http2: client connection lost") ||
		strings.Contains(msg, "http2: server sent GOAWAY and closed the connection") {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.HasPrefix(e.Error(), "connection error: ") {
			return true
		}
	}
	return false
}

func IsUpstreamModelNotFound(err error) bool {
	var buffered *UpstreamErrorResponse
	if errors.As(err, &buffered) {
		return buffered.Status == http.StatusNotFound
	}
	return false
}

func IsUpstreamProviderBillingBlocked(err error) bool {
	var buffered *UpstreamErrorResponse
	if errors.As(err, &buffered) {
		return buffered.Status == http.StatusPaymentRequired
	}
	return false
}

var schemaRejectionPhrases = []string{
	"conflict in schema definitions",
	"failed to compile grammar",
	"could not compile grammar",
	"invalid tool schema",
	"invalid function schema",
	"invalid schema for function",
	"schema is not representable",
	"invalid input schema",
}

func IsUpstreamSchemaRejection(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(string(buffered.Body))
	for _, phrase := range schemaRejectionPhrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}

var thoughtSignatureRejectionPhrases = []string{
	"thought signature",
	"thought_signature",
	"thoughtsignature",
}

func IsUpstreamThoughtSignatureRejection(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(string(buffered.Body))
	for _, phrase := range thoughtSignatureRejectionPhrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}

var unknownFieldPhrases = []string{
	"extra inputs are not permitted",
	"extra inputs not permitted",
	"extra fields not permitted",
	"unknown field",
	"unrecognized field",
	"unexpected field",
	"additional properties are not allowed",
}

func IsUpstreamOutputConfigFormatRejection(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(string(buffered.Body))
	if !strings.Contains(body, "output_config") || !strings.Contains(body, "format") {
		return false
	}
	for _, phrase := range unknownFieldPhrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}

func IsAnthropicFastModeQuotaRejection(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status != http.StatusTooManyRequests {
		return false
	}
	return strings.Contains(strings.ToLower(string(buffered.Body)), "fast mode")
}

func IsUpstreamPromptCacheKeyRejection(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(string(buffered.Body))
	if !strings.Contains(body, "prompt_cache_key") {
		return false
	}
	for _, phrase := range unknownFieldPhrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}

var responsesUnsupportedPhrases = []string{
	"responses rest api not enabled",
	"responses api not enabled",
	"not allowed to access this endpoint",
	"unknown path",
}

func IsUpstreamResponsesUnsupported(err error) bool {
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) {
		return false
	}
	if buffered.Status == http.StatusNotFound {
		return true
	}
	if buffered.Status != http.StatusBadRequest && buffered.Status != http.StatusForbidden {
		return false
	}
	body := strings.ToLower(string(buffered.Body))
	for _, phrase := range responsesUnsupportedPhrases {
		if strings.Contains(body, phrase) {
			return true
		}
	}
	return false
}

func UpstreamErrorBodyMessage(err error) string {
	const maxBodyLogBytes = 1024
	var buffered *UpstreamErrorResponse
	if !errors.As(err, &buffered) || len(buffered.Body) == 0 {
		return ""
	}
	body := buffered.Body
	if len(body) > maxBodyLogBytes {
		body = body[:maxBodyLogBytes]
	}
	if msg := gjson.GetBytes(body, "error.message"); msg.Exists() {
		return msg.String()
	}
	if msg := gjson.GetBytes(body, "message"); msg.Exists() {
		return msg.String()
	}
	return strings.TrimSpace(string(body))
}

type Endpoint int

const (
	EndpointChatCompletions Endpoint = iota
	EndpointResponses
)

type PreparedRequest struct {
	ResponseToolNames map[string]string
	Body              []byte
	Headers           http.Header
	Endpoint          Endpoint
	Stats             RequestMutationStats
}

type RequestMutationStats struct {
	CCOnlyToolsStripped     int
	CCTaskRemindersStripped int
	ServerToolsStripped     int
	GeminiReminderInjected  bool
	GeminiValidatedToolMode bool
	Transformations         []RequestTransformation
}

type TransformationAction string

const (
	TransformationPreserved TransformationAction = "preserved"
	TransformationRewritten TransformationAction = "rewritten"
	TransformationRejected  TransformationAction = "rejected"
	TransformationDropped   TransformationAction = "dropped"
)

type TransformationSeverity string

const (
	TransformationInfo  TransformationSeverity = "info"
	TransformationWarn  TransformationSeverity = "warning"
	TransformationError TransformationSeverity = "error"
)

type RequestTransformation struct {
	Code     string
	Action   TransformationAction
	Severity TransformationSeverity
	Path     string
	Reason   string
}

type OutputProgressArmer interface {
	ArmOutputProgress(mark func()) (armed bool)
}

type ReasoningProgressArmer interface {
	ArmReasoningProgress(mark func()) (armed bool)
}

type Client interface {
	Proxy(ctx context.Context, decision router.Decision, prep PreparedRequest, w http.ResponseWriter, r *http.Request) error

	Passthrough(ctx context.Context, prep PreparedRequest, w http.ResponseWriter, r *http.Request) error
}
