package otel

import (
	"net/http"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/usage"
)

var (
	_ http.ResponseWriter = (*UsageExtractor)(nil)
	_ http.Flusher        = (*UsageExtractor)(nil)
	_ usage.Sink          = (*UsageExtractor)(nil)
)

// UsageExtractor is the OTel adapter that forwards response bytes while
// feeding the vendor-neutral telemetry usage parser.
type UsageExtractor struct {
	inner     http.ResponseWriter
	extractor *telemetry.UsageExtractor
}

func NewUsageExtractor(inner http.ResponseWriter, provider string) *UsageExtractor {
	return &UsageExtractor{
		inner:     inner,
		extractor: telemetry.NewUsageExtractor(provider),
	}
}

func (u *UsageExtractor) Header() http.Header {
	if u == nil || u.inner == nil {
		return nil
	}
	return u.inner.Header()
}

func (u *UsageExtractor) WriteHeader(statusCode int) {
	if u == nil || u.inner == nil {
		return
	}
	sse.PrepareJSONResponse(u.inner)
	u.inner.WriteHeader(statusCode)
}

func (u *UsageExtractor) Write(p []byte) (int, error) {
	if u == nil || u.inner == nil {
		return len(p), nil
	}
	u.extractor.Consume(p)
	return sse.WriteJSONChunk(u.inner, p)
}

func (u *UsageExtractor) Flush() {
	if u == nil || u.inner == nil {
		return
	}
	if f, ok := u.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the watchdog arm call to inner if it supports it.
func (u *UsageExtractor) ArmOutputProgress(mark func()) bool {
	if u == nil || u.inner == nil {
		return false
	}
	arm, ok := u.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

func (u *UsageExtractor) RecordProviderUsage(facts usage.Facts) {
	if u != nil {
		u.extractor.RecordProviderUsage(facts)
	}
}

func (u *UsageExtractor) UsageFacts() usage.Facts {
	if u == nil {
		return usage.Facts{}
	}
	return u.extractor.UsageFacts()
}

func (u *UsageExtractor) SetRequestCacheTTL1h(oneHour func() bool) {
	if u != nil {
		u.extractor.SetRequestCacheTTL1h(oneHour)
	}
}

func (u *UsageExtractor) RecordOutputLimitReached() {
	if u != nil {
		u.extractor.RecordOutputLimitReached()
	}
}

func (u *UsageExtractor) OutputLimitReached() bool {
	return u != nil && u.extractor.OutputLimitReached()
}

func (u *UsageExtractor) Tokens() (int, int) {
	if u == nil {
		return 0, 0
	}
	return u.extractor.Tokens()
}

func (u *UsageExtractor) CacheTokens() (int, int) {
	if u == nil {
		return 0, 0
	}
	return u.extractor.CacheTokens()
}

func (u *UsageExtractor) UsageModifiers() catalog.UsageModifiers {
	if u == nil {
		return catalog.UsageModifiers{}
	}
	return u.extractor.UsageModifiers()
}

func (u *UsageExtractor) Speed() catalog.Speed {
	if u == nil {
		return ""
	}
	return u.extractor.Speed()
}

func (u *UsageExtractor) InferenceGeo() catalog.InferenceGeo {
	if u == nil {
		return ""
	}
	return u.extractor.InferenceGeo()
}

func (u *UsageExtractor) ReasoningTokens() int {
	if u == nil {
		return 0
	}
	return u.extractor.ReasoningTokens()
}

func (u *UsageExtractor) AnthropicResponse() (string, int, bool) {
	if u == nil {
		return "", 0, false
	}
	return u.extractor.AnthropicResponse()
}

func (u *UsageExtractor) OpenAIChatResponse() (string, int, bool) {
	if u == nil {
		return "", 0, false
	}
	return u.extractor.OpenAIChatResponse()
}

func (u *UsageExtractor) Extractor() *telemetry.UsageExtractor {
	if u == nil {
		return nil
	}
	return u.extractor
}
