package recordedlaws_test

func replayLossAllowlist() []lossRule {
	return []lossRule{
		{hop: "replay:responses_*>chat_*+*", kind: dropped, key: "thinking.*.*", why: "docs/RESPONSES_TRANSLATION.md: encrypted reasoning has no Chat field, so a Chat client has nothing replayable"},
		{hop: "replay:responses_*>anthropic_*+*", kind: dropped, key: "thinking.*.text", why: "docs/RESPONSES_TRANSLATION.md: the summary is a display digest of the encrypted reasoning; replay carries the encrypted item"},
		{hop: "replay:anthropic_*>*+*", kind: dropped, key: "thinking.*.*", why: "follows from the anthropic>chat signature drop: without a signature Anthropic cannot accept the block (docs/REQUEST_ENVELOPE.md)"},
		{hop: "replay:chat_*>anthropic_*+*", kind: changed, key: "tool.*.id", holds: nonceSuffixed, why: "the client echoes the nonce-suffixed tool_use id; call and result stay paired (TestAnthropicSSETranslator_StreamedToolUseIDUniqueAcrossResponses)"},
		{hop: "replay:chat_*>anthropic_*+*", kind: dropped, key: "thinking.*.text", why: "unsigned cross-format thinking is not replayed upstream (docs/REQUEST_ENVELOPE.md, Anthropic same-format overrides)"},
		{hop: "replay:gemini_*>chat_*+*", kind: added, key: "tool.*.signature", why: "docs/TRANSLATION_COMPATIBILITY.md: the first call's signature is replayed on later calls (TestGeminiStream_FirstFunctionCallSignatureLatchesToLaterCalls)"},
		{hop: "replay:chat_sse>responses_sse+*", kind: dropped, key: "thinking.*.text", candidate: true, why: "follows from the chat>responses reasoning drop"},
		{hop: "replay:gemini_*>responses_*@chat+*", kind: added, key: "error", candidate: true, why: "candidate bug: the next Codex turn has no thoughtSignature, so PrepareGemini rejects it with ErrGeminiUnsignedToolHistory"},
		{hop: "replay:gemini_*>responses_*@chat+*", kind: dropped, key: "text", candidate: true, why: "same root cause as the ErrGeminiUnsignedToolHistory rejection"},
		{hop: "replay:gemini_*>responses_*@chat+*", kind: dropped, key: "tool.*.*", candidate: true, why: "same root cause as the ErrGeminiUnsignedToolHistory rejection"},
	}
}
