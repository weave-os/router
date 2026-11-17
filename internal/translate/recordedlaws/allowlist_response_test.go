package recordedlaws_test

import (
	"encoding/base64"
	"regexp"

	"github.com/tidwall/gjson"
)

const (
	openAIReasoningCarrier = "__openai_reasoning__"
	reasoningScope         = "recordedlaws-scope"
)

func nonceSuffixed(before, after string) bool {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(before) + `_[0-9a-f]{12}$`).MatchString(after)
}

func carriesOpenAIReasoning(before, after string) bool {
	return hasPrefixOf(before+openAIReasoningCarrier, after)
}

func routerReasoningEnvelope(before, after string) bool {
	decoded, err := base64.StdEncoding.DecodeString(after)
	if err != nil {
		return false
	}
	envelope := gjson.ParseBytes(decoded)
	return envelope.Get("provider").String() == "openai" && envelope.Get("enc").String() == before
}

func responseLossAllowlist() []lossRule {
	return []lossRule{
		{hop: "gemini_*>*", kind: added, key: "tool.*.id", why: "Chat tool_calls need an id and Gemini functionCall has none; gemini_response.go and gemini_stream.go mint call_ ids"},
		{hop: "gemini_*>chat_*", kind: added, key: "tool.*.signature", why: "docs/TRANSLATION_COMPATIBILITY.md: Gemini 3.x signs only the first call and the router replays it on later calls (TestGeminiStream_FirstFunctionCallSignatureLatchesToLaterCalls)"},
		{hop: "chat_*>anthropic_*", kind: changed, key: "tool.*.id", holds: nonceSuffixed, why: "per-response nonce on translated tool_use ids (TestAnthropicSSETranslator_StreamedToolUseIDUniqueAcrossResponses)"},
		{hop: "responses_*>anthropic_*", kind: changed, key: "thinking.*.signature", holds: routerReasoningEnvelope, why: "encrypted reasoning is wrapped in the router reasoning envelope (TestOpenAIReasoningSignatureRoundTrip)"},
		{hop: "responses_*>anthropic_*", kind: changed, key: "tool.*.id", holds: carriesOpenAIReasoning, why: "tool_use ids carry the OpenAI reasoning envelope for replay (TestPrepareOpenAIResponses_ReplaysReasoningCarriedOnToolUseID)"},
		{hop: "responses_*>chat_*", kind: dropped, key: "thinking.*.signature", why: "docs/RESPONSES_TRANSLATION.md: encrypted reasoning has no Chat field"},
		{hop: "anthropic_*>*", kind: dropped, key: "thinking.*.signature", why: "Chat has no thinking-signature field and unsigned thinking is stripped on replay (docs/REQUEST_ENVELOPE.md, Anthropic same-format overrides)"},
		{hop: "gemini_*>responses_*@chat", kind: added, key: "tool.*.signature", why: "docs/TRANSLATION_COMPATIBILITY.md: the first call's signature is replayed on later calls, carried in the Responses call_id (TestResponsesWriter_CallIDKeepsGeminiThoughtCarrier)"},
	}
}
