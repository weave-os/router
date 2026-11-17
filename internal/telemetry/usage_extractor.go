package telemetry

import (
	"bytes"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/usage"

	"github.com/tidwall/gjson"
)

var (
	_ usage.Sink = (*UsageExtractor)(nil)
)

type anthropicSSEEvent string

const (
	anthropicEventMessageStart      anthropicSSEEvent = "message_start"
	anthropicEventMessageDelta      anthropicSSEEvent = "message_delta"
	anthropicEventContentBlockStart anthropicSSEEvent = "content_block_start"
)

type anthropicBlockType string

const anthropicBlockToolUse anthropicBlockType = "tool_use"

const (
	anthropicStopMaxTokens = "max_tokens"
	openAIFinishLength     = "length"
	geminiFinishMaxTokens  = "MAX_TOKENS"
)

type UsageExtractor struct {
	provider string

	facts              usage.Facts
	outputLimitReached bool
	requestCacheTTL1h  func() bool

	stopReason    string
	toolUseBlocks int

	finishReason string
	toolCallIdxs map[int]struct{}

	leftover []byte
	framing  sse.Scanner
}

func NewUsageExtractor(provider string) *UsageExtractor {
	return &UsageExtractor{
		provider: provider,
	}
}

func (u *UsageExtractor) Consume(p []byte) {
	if u == nil {
		return
	}
	u.leftover = append(u.leftover, p...)
	u.scanBuffer()
}

func (u *UsageExtractor) RecordProviderUsage(facts usage.Facts) {
	if u == nil {
		return
	}
	if providers.OutputExcludesReasoning(u.provider) {
		facts.OutputBasis = usage.OutputExcludesReasoning
	}
	u.facts.Merge(facts)
}

func (u *UsageExtractor) UsageFacts() usage.Facts {
	if u == nil {
		return usage.Facts{}
	}
	return u.facts
}

func (u *UsageExtractor) SetRequestCacheTTL1h(oneHour func() bool) {
	if u != nil {
		u.requestCacheTTL1h = oneHour
	}
}

func (u *UsageExtractor) RecordOutputLimitReached() {
	if u != nil {
		u.outputLimitReached = true
	}
}

func (u *UsageExtractor) OutputLimitReached() bool {
	return u != nil && u.outputLimitReached
}

func (u *UsageExtractor) Tokens() (input, output int) {
	if u == nil {
		return 0, 0
	}
	return u.facts.Input.Value, u.facts.BilledOutputTokens()
}

func (u *UsageExtractor) CacheTokens() (creation, read int) {
	if u == nil {
		return 0, 0
	}
	return u.facts.CacheWrite.Value, u.facts.CacheRead.Value
}

func (u *UsageExtractor) UsageModifiers() catalog.UsageModifiers {
	if u == nil {
		return catalog.UsageModifiers{}
	}
	oneHour := u.facts.CacheWrite1h.Value
	if !u.facts.CacheSplitReported() && u.facts.CacheWrite.Value > 0 && u.requestCacheTTL1h != nil && u.requestCacheTTL1h() {
		oneHour = u.facts.CacheWrite.Value
	}
	return catalog.UsageModifiers{CacheCreation1h: oneHour, InferenceGeo: u.facts.InferenceGeo}
}

func (u *UsageExtractor) Speed() catalog.Speed {
	if u == nil {
		return ""
	}
	return u.facts.Speed
}

func (u *UsageExtractor) InferenceGeo() catalog.InferenceGeo {
	if u == nil {
		return ""
	}
	return u.facts.InferenceGeo
}

func (u *UsageExtractor) ReasoningTokens() int {
	if u == nil {
		return 0
	}
	return u.facts.Reasoning.Value
}

func (u *UsageExtractor) AnthropicResponse() (stopReason string, toolUseBlocks int, observed bool) {
	if u == nil {
		return "", 0, false
	}
	return u.stopReason, u.toolUseBlocks, u.stopReason != ""
}

func (u *UsageExtractor) OpenAIChatResponse() (finishReason string, toolCalls int, observed bool) {
	if u == nil || u.finishReason == "" {
		return "", 0, false
	}
	return u.finishReason, len(u.toolCallIdxs), true
}

func (u *UsageExtractor) scanBuffer() {
	data := u.leftover

	for {
		event, n := u.framing.Next(data)
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

func (u *UsageExtractor) extractFromSSEEvent(eventType []byte, data []byte) {
	switch providers.FamilyFor(u.provider) {
	case providers.FamilyAnthropic:
		u.extractAnthropicSSE(eventType, data)
	case providers.FamilyOpenAICompat, providers.FamilyGemini:
		u.extractOpenAISSE(data)
	case providers.FamilyUnknown:
	}
}

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
			if stop == anthropicStopMaxTokens {
				u.RecordOutputLimitReached()
			}
		}
	}

	u.RecordProviderUsage(usage.ParseAnthropic(anthropicUsage(data)))
}

func (u *UsageExtractor) extractOpenAISSE(data []byte) {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return
	}

	u.extractOpenAIChatSSEResponse(trimmed)
	if providers.FamilyFor(u.provider) == providers.FamilyGemini && gjson.GetBytes(trimmed, "candidates.0.finishReason").String() == geminiFinishMaxTokens {
		u.RecordOutputLimitReached()
	}

	u.RecordProviderUsage(u.parseUsage(trimmed))
}

func (u *UsageExtractor) tryExtractFromJSON() {
	if len(u.leftover) == 0 {
		return
	}

	facts := u.parseUsage(u.leftover)
	if !facts.Present {
		return
	}

	switch providers.FamilyFor(u.provider) {
	case providers.FamilyAnthropic:
		u.extractAnthropicJSONResponse(u.leftover)
	case providers.FamilyOpenAICompat:
		u.extractOpenAIChatJSONResponse(u.leftover)
	case providers.FamilyGemini:
		if gjson.GetBytes(u.leftover, "candidates.0.finishReason").String() == geminiFinishMaxTokens {
			u.RecordOutputLimitReached()
		}
	case providers.FamilyUnknown:
	}

	u.RecordProviderUsage(facts)
}

func (u *UsageExtractor) extractAnthropicJSONResponse(data []byte) {
	stop := gjson.GetBytes(data, "stop_reason").String()
	if stop == "" {
		return
	}
	u.stopReason = stop
	if stop == anthropicStopMaxTokens {
		u.RecordOutputLimitReached()
	}
	u.toolUseBlocks = 0
	for _, block := range gjson.GetBytes(data, "content").Array() {
		if block.Get("type").String() == string(anthropicBlockToolUse) {
			u.toolUseBlocks++
		}
	}
}

func (u *UsageExtractor) extractOpenAIChatSSEResponse(data []byte) {
	if providers.FamilyFor(u.provider) != providers.FamilyOpenAICompat {
		return
	}
	gjson.GetBytes(data, "choices").ForEach(func(_, choice gjson.Result) bool {
		if reason := choice.Get("finish_reason").String(); reason != "" {
			u.finishReason = reason
			if reason == openAIFinishLength {
				u.RecordOutputLimitReached()
			}
		}
		choice.Get("delta.tool_calls").ForEach(func(_, call gjson.Result) bool {
			u.observeOpenAIToolCall(int(call.Get("index").Int()))
			return true
		})
		return true
	})
}

func (u *UsageExtractor) extractOpenAIChatJSONResponse(data []byte) {
	choice := gjson.GetBytes(data, "choices.0")
	reason := choice.Get("finish_reason").String()
	if reason == "" {
		return
	}
	u.finishReason = reason
	if reason == openAIFinishLength {
		u.RecordOutputLimitReached()
	}
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

func (u *UsageExtractor) parseUsage(body []byte) usage.Facts {
	family := providers.FamilyFor(u.provider)
	if family == providers.FamilyAnthropic {
		return usage.ParseAnthropic(anthropicUsage(body))
	}
	if family == providers.FamilyGemini {
		if meta := gjson.GetBytes(body, "usageMetadata"); meta.IsObject() {
			return usage.ParseGemini(meta)
		}
	}
	if family == providers.FamilyOpenAICompat || family == providers.FamilyGemini {
		object := gjson.GetBytes(body, "usage")
		if !object.IsObject() {
			object = gjson.GetBytes(body, "response.usage")
		}
		return usage.ParseOpenAI(object)
	}
	return usage.Facts{}
}

func anthropicUsage(body []byte) gjson.Result {
	if object := gjson.GetBytes(body, "usage"); object.IsObject() {
		return object
	}
	return gjson.GetBytes(body, "message.usage")
}
