package otel

import (
	"bytes"
	"net/http"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/sse"
)

// UsageSink receives extracted token usage. Translators call it directly when
// they've already parsed usage from an event, skipping a separate parse pass.
type UsageSink interface {
	RecordUsage(inputTokens, outputTokens int)
	RecordCacheUsage(cacheCreationTokens, cacheReadTokens int)
}

var (
	_ http.ResponseWriter = (*UsageExtractor)(nil)
	_ http.Flusher        = (*UsageExtractor)(nil)
	_ UsageSink           = (*UsageExtractor)(nil)
)

// Anthropic wire-format names the extractor recognizes. Duplicated here rather
// than imported so otel does not import translate (translate already imports
// observability); same precedent as openaiCacheTokens.
type anthropicSSEEvent string

const (
	anthropicEventMessageStart      anthropicSSEEvent = "message_start"
	anthropicEventMessageDelta      anthropicSSEEvent = "message_delta"
	anthropicEventContentBlockStart anthropicSSEEvent = "content_block_start"
)

type anthropicBlockType string

const anthropicBlockToolUse anthropicBlockType = "tool_use"

// UsageExtractor wraps an http.ResponseWriter and sniffs token usage (SSE or
// JSON) as bytes flow through. Only the unconsumed tail is retained between writes.
type UsageExtractor struct {
	inner    http.ResponseWriter
	provider string

	input         int
	output        int
	cacheCreation int
	cacheRead     int

	stopReason    string
	toolUseBlocks int

	// finishReason and toolCallIdxs hold the chat-shaped signals. A streaming
	// turn announces one tool call across many argument fragments, so calls are
	// counted by the index that identifies them rather than by fragment.
	finishReason string
	toolCallIdxs map[int]struct{}

	leftover []byte
}

// NewUsageExtractor creates a usage-extracting writer for the given provider's
// response format. If inner is nil, only RecordUsage/Tokens are valid — the
// ResponseWriter methods must not be called.
func NewUsageExtractor(inner http.ResponseWriter, provider string) *UsageExtractor {
	return &UsageExtractor{
		inner:    inner,
		provider: provider,
	}
}

func (u *UsageExtractor) Header() http.Header {
	if u.inner == nil {
		return nil
	}
	return u.inner.Header()
}

func (u *UsageExtractor) WriteHeader(statusCode int) {
	if u.inner == nil {
		return
	}
	u.inner.WriteHeader(statusCode)
}

// Write sniffs p for token usage data then delegates to the inner writer.
func (u *UsageExtractor) Write(p []byte) (int, error) {
	if u.inner == nil {
		return len(p), nil
	}
	u.leftover = append(u.leftover, p...)
	u.scanBuffer()
	return u.inner.Write(p)
}

func (u *UsageExtractor) Flush() {
	if u.inner == nil {
		return
	}
	if f, ok := u.inner.(http.Flusher); ok {
		f.Flush()
	}
}

// ArmOutputProgress forwards the watchdog arm call to inner if it supports it.
// UsageExtractor can't itself distinguish an output frame from a keepalive, so
// this must pass through or wrapping would hide the hook from the provider client.
func (u *UsageExtractor) ArmOutputProgress(mark func()) (armed bool) {
	if u.inner == nil {
		return false
	}
	arm, ok := u.inner.(interface{ ArmOutputProgress(func()) bool })
	if !ok {
		return false
	}
	return arm.ArmOutputProgress(mark)
}

// RecordUsage sets token counts directly, bypassing SSE parsing. Called by
// translators that have already parsed usage from the upstream event stream.
func (u *UsageExtractor) RecordUsage(inputTokens, outputTokens int) {
	if inputTokens > 0 {
		u.input = inputTokens
	}
	if outputTokens > 0 {
		u.output = outputTokens
	}
}

// RecordCacheUsage sets cache token counts directly. Pass 0 for a field the
// provider does not emit.
func (u *UsageExtractor) RecordCacheUsage(cacheCreationTokens, cacheReadTokens int) {
	if cacheCreationTokens > 0 {
		u.cacheCreation = cacheCreationTokens
	}
	if cacheReadTokens > 0 {
		u.cacheRead = cacheReadTokens
	}
}

// Tokens returns the extracted input and output token counts.
func (u *UsageExtractor) Tokens() (input, output int) {
	if u == nil {
		return 0, 0
	}
	return u.input, u.output
}

// CacheTokens returns the extracted cache creation/read counts. Zero means the
// provider doesn't emit cache tokens (Google) or there were no cache hits.
func (u *UsageExtractor) CacheTokens() (creation, read int) {
	if u == nil {
		return 0, 0
	}
	return u.cacheCreation, u.cacheRead
}

// AnthropicResponse returns the turn-ending signals sniffed from an
// Anthropic-format response: the upstream stop_reason and how many tool_use
// content blocks it carried. observed is false until a stop_reason is seen, so
// a stream that ends early is never reported as a measured zero-tool turn.
func (u *UsageExtractor) AnthropicResponse() (stopReason string, toolUseBlocks int, observed bool) {
	if u == nil {
		return "", 0, false
	}
	return u.stopReason, u.toolUseBlocks, u.stopReason != ""
}

// OpenAIChatResponse returns the turn-ending signals sniffed from a
// chat/completions-format response: the upstream finish_reason and how many
// tool calls it carried. observed is false until a finish_reason is seen, so a
// stream that ends early is never reported as a measured zero-tool turn.
func (u *UsageExtractor) OpenAIChatResponse() (finishReason string, toolCalls int, observed bool) {
	if u == nil || u.finishReason == "" {
		return "", 0, false
	}
	return u.finishReason, len(u.toolCallIdxs), true
}

// scanBuffer splits buffered data on SSE event boundaries and extracts token
// usage from each complete event using zero-alloc gjson probes.
func (u *UsageExtractor) scanBuffer() {
	data := u.leftover

	for {
		event, n := sse.SplitNext(data)
		if n == 0 {
			break
		}
		data = data[n:]

		eventType, payload := sse.ParseEvent(event)
		if len(payload) == 0 {
			continue
		}

		u.extractFromSSEEvent(eventType, payload)
	}

	n := copy(u.leftover, data)
	u.leftover = u.leftover[:n]

	u.tryExtractFromJSON()
}

// Dispatch is family-based so Anthropic-spec gateway providers
// (e.g. anthropic_gateway) are parsed correctly instead of recording zero usage.
func (u *UsageExtractor) extractFromSSEEvent(eventType []byte, data []byte) {
	switch providers.FamilyFor(u.provider) {
	case providers.FamilyAnthropic:
		u.extractAnthropicSSE(eventType, data)
	case providers.FamilyOpenAICompat, providers.FamilyGemini:
		u.extractOpenAISSE(data)
	}
}

// message_start carries input_tokens + cache tokens; message_delta carries
// output_tokens and the stop_reason; content_block_start announces each
// tool_use block.
func (u *UsageExtractor) extractAnthropicSSE(eventType []byte, data []byte) {
	if bytes.Equal(eventType, []byte(anthropicEventContentBlockStart)) {
		if gjson.GetBytes(data, "content_block.type").String() == string(anthropicBlockToolUse) {
			u.toolUseBlocks++
		}
		return
	}
	if !bytes.Equal(eventType, []byte(anthropicEventMessageStart)) && !bytes.Equal(eventType, []byte(anthropicEventMessageDelta)) {
		return
	}

	if bytes.Equal(eventType, []byte(anthropicEventMessageDelta)) {
		if stop := gjson.GetBytes(data, "delta.stop_reason").String(); stop != "" {
			u.stopReason = stop
		}
	}

	input, output, cacheCreation, cacheRead, found := extractUsageGJSON(data, providers.ProviderAnthropic)
	if !found {
		return
	}

	if bytes.Equal(eventType, []byte(anthropicEventMessageStart)) {
		if input > 0 {
			u.input = input
		}
		if cacheCreation > 0 {
			u.cacheCreation = cacheCreation
		}
		if cacheRead > 0 {
			u.cacheRead = cacheRead
		}
	}
	if bytes.Equal(eventType, []byte(anthropicEventMessageDelta)) && output > 0 {
		u.output = output
	}
}

// Final chunk with stream_options.include_usage=true carries the counts.
func (u *UsageExtractor) extractOpenAISSE(data []byte) {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return
	}

	u.extractOpenAIChatSSEResponse(trimmed)

	input, output, cacheCreation, cacheRead, found := extractUsageGJSON(trimmed, u.provider)
	if !found {
		return
	}

	if input > 0 {
		u.input = input
	}
	if output > 0 {
		u.output = output
	}
	if cacheCreation > 0 {
		u.cacheCreation = cacheCreation
	}
	if cacheRead > 0 {
		u.cacheRead = cacheRead
	}
}

func (u *UsageExtractor) tryExtractFromJSON() {
	if len(u.leftover) == 0 {
		return
	}

	input, output, cacheCreation, cacheRead, found := extractUsageGJSON(u.leftover, u.provider)
	if !found {
		return
	}

	// Keyed off usage being present: leftover holds a partial body on every
	// write before the last, and usage is the final top-level member, so a
	// found usage object is what makes content[] safe to count.
	switch providers.FamilyFor(u.provider) {
	case providers.FamilyAnthropic:
		u.extractAnthropicJSONResponse(u.leftover)
	case providers.FamilyOpenAICompat:
		u.extractOpenAIChatJSONResponse(u.leftover)
	}

	if input > 0 {
		u.input = input
	}
	if output > 0 {
		u.output = output
	}
	if cacheCreation > 0 {
		u.cacheCreation = cacheCreation
	}
	if cacheRead > 0 {
		u.cacheRead = cacheRead
	}
}

// A non-streaming Anthropic body carries stop_reason at the top level and one
// content entry per emitted block.
func (u *UsageExtractor) extractAnthropicJSONResponse(data []byte) {
	stop := gjson.GetBytes(data, "stop_reason").String()
	if stop == "" {
		return
	}
	u.stopReason = stop
	u.toolUseBlocks = 0
	for _, block := range gjson.GetBytes(data, "content").Array() {
		if block.Get("type").String() == string(anthropicBlockToolUse) {
			u.toolUseBlocks++
		}
	}
}

// A chat/completions chunk states the turn's end on the choice's finish_reason
// and announces each tool call under the choice's delta, keyed by an index that
// is stable across the call's argument fragments. A Responses or Gemini frame
// carries no choices, so nothing is observed from one.
func (u *UsageExtractor) extractOpenAIChatSSEResponse(data []byte) {
	if providers.FamilyFor(u.provider) != providers.FamilyOpenAICompat {
		return
	}
	gjson.GetBytes(data, "choices").ForEach(func(_, choice gjson.Result) bool {
		if reason := choice.Get("finish_reason").String(); reason != "" {
			u.finishReason = reason
		}
		choice.Get("delta.tool_calls").ForEach(func(_, call gjson.Result) bool {
			u.observeOpenAIToolCall(int(call.Get("index").Int()))
			return true
		})
		return true
	})
}

// A non-streaming chat/completions body carries the same signals on the
// message instead of on a delta.
func (u *UsageExtractor) extractOpenAIChatJSONResponse(data []byte) {
	choice := gjson.GetBytes(data, "choices.0")
	reason := choice.Get("finish_reason").String()
	if reason == "" {
		return
	}
	u.finishReason = reason
	for i := range choice.Get("message.tool_calls").Array() {
		u.observeOpenAIToolCall(i)
	}
}

func (u *UsageExtractor) observeOpenAIToolCall(index int) {
	if u.toolCallIdxs == nil {
		u.toolCallIdxs = make(map[int]struct{})
	}
	u.toolCallIdxs[index] = struct{}{}
}

// extractUsageGJSON probes usage fields via gjson (no json.Unmarshal/map allocs).
// OpenAI cache-read maps from cached_tokens; GPT-5.6+ cache-write maps from cache_write_tokens.
// Google's native :generateContent uses usageMetadata; its OpenAI-compat surface
// uses the OpenAI shape instead.
func extractUsageGJSON(data []byte, provider string) (input, output, cacheCreation, cacheRead int, found bool) {
	family := providers.FamilyFor(provider)

	if family == providers.FamilyGemini {
		if meta := gjson.GetBytes(data, "usageMetadata"); meta.Exists() {
			input = int(meta.Get("promptTokenCount").Int())
			// candidatesTokenCount excludes thinking; thoughtsTokenCount is billed as output.
			output = int(meta.Get("candidatesTokenCount").Int() + meta.Get("thoughtsTokenCount").Int())
			cacheRead = int(meta.Get("cachedContentTokenCount").Int())
			return input, output, 0, cacheRead, true
		}
	}

	usage := gjson.GetBytes(data, "usage")
	if !usage.Exists() && family == providers.FamilyAnthropic {
		usage = gjson.GetBytes(data, "message.usage")
	}
	// OpenAI Responses streaming nests usage under the terminal response event
	// (response.completed); the non-streaming body carries it at the top level.
	if !usage.Exists() && family == providers.FamilyOpenAICompat {
		usage = gjson.GetBytes(data, "response.usage")
	}
	if !usage.Exists() {
		return 0, 0, 0, 0, false
	}

	switch family {
	case providers.FamilyAnthropic:
		input = int(usage.Get("input_tokens").Int())
		output = int(usage.Get("output_tokens").Int())
		cacheCreation = int(usage.Get("cache_creation_input_tokens").Int())
		cacheRead = int(usage.Get("cache_read_input_tokens").Int())
	case providers.FamilyOpenAICompat, providers.FamilyGemini:
		// Chat Completions uses prompt_tokens/completion_tokens; Responses API
		// (Codex passthrough) uses input_tokens/output_tokens. Probe both.
		if pt := usage.Get("prompt_tokens"); pt.Exists() {
			input = int(pt.Int())
			output = int(usage.Get("completion_tokens").Int())
		} else {
			input = int(usage.Get("input_tokens").Int())
			output = int(usage.Get("output_tokens").Int())
		}
		cacheCreation, cacheRead = openaiCacheTokens(usage)
	default:
		return 0, 0, 0, 0, false
	}

	return input, output, cacheCreation, cacheRead, true
}

// openaiCacheTokens mirrors translate.OpenAICacheTokens. Duplicated here so
// otel does not import translate (translate already imports observability).
func openaiCacheTokens(usage gjson.Result) (cacheWrite, cacheRead int) {
	if !usage.Exists() {
		return 0, 0
	}
	for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details := usage.Get(prefix)
		if !details.Exists() {
			continue
		}
		if cacheRead == 0 {
			cacheRead = int(details.Get("cached_tokens").Int())
		}
		if cacheWrite == 0 {
			if w := details.Get("cache_write_tokens"); w.Exists() {
				cacheWrite = int(w.Int())
			} else {
				cacheWrite = int(details.Get("cache_creation_tokens").Int())
			}
		}
	}
	return cacheWrite, cacheRead
}
