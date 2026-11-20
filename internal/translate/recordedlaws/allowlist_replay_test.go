package recordedlaws_test

func replayLossAllowlist() []lossRule {
	return []lossRule{
		{hop: "replay:responses_*>chat_*+*", kind: dropped, key: "thinking.*.*", why: "docs/RESPONSES_TRANSLATION.md: encrypted reasoning has no Chat field, so a Chat client has nothing replayable"},
		{hop: "replay:chat_sse>responses_sse+*", kind: dropped, key: "thinking.*.text", why: "docs/RESPONSES_TRANSLATION.md: Chat-derived reasoning has no encrypted_content, and Responses reasoning is dropped when projected to Chat"},
		{hop: "replay:responses_*>anthropic_*+*", kind: dropped, key: "thinking.*.text", why: "docs/RESPONSES_TRANSLATION.md: the summary is a display digest of the encrypted reasoning; replay carries the encrypted item"},
		{hop: "replay:anthropic_*>*+*", kind: dropped, key: "thinking.*.*", why: "follows from the anthropic>chat signature drop: without a signature Anthropic cannot accept the block (docs/REQUEST_ENVELOPE.md)"},
		{hop: "replay:chat_*>anthropic_*+*", kind: changed, key: "tool.*.id", holds: nonceSuffixed, why: "the client echoes the nonce-suffixed tool_use id; call and result stay paired (TestAnthropicSSETranslator_StreamedToolUseIDUniqueAcrossResponses)"},
		{hop: "replay:chat_*>anthropic_*+*", kind: dropped, key: "thinking.*.text", why: "unsigned cross-format thinking is not replayed upstream (docs/REQUEST_ENVELOPE.md, Anthropic same-format overrides)"},
		{hop: "replay:gemini_*>chat_*+*", kind: added, key: "tool.*.signature", why: "docs/TRANSLATION_COMPATIBILITY.md: the first call's signature is replayed on later calls (TestGeminiStream_FirstFunctionCallSignatureLatchesToLaterCalls)"},
		{hop: "replay:gemini_*>responses_*@chat+*", kind: added, key: "tool.*.signature", why: "docs/TRANSLATION_COMPATIBILITY.md: Codex echoes the call_id carrier, so the first call's signature is replayed on later calls (TestCandidate_ResponsesClientKeepsGeminiThoughtSignatureForTheNextTurn)"},
	}
}
