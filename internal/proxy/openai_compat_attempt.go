package proxy

// openAICompatAttempt is one OpenAI-compatible dispatch's wire shape. The
// proxy flips a field and re-dispatches once when a gateway 400s the shape it
// was sent, memoizing the answer so later turns start from the working one.
type openAICompatAttempt struct {
	// useResponses POSTs /v1/responses instead of /v1/chat/completions.
	useResponses bool
	// stripPromptCacheKey omits the prompt_cache_key affinity hint.
	stripPromptCacheKey bool
	// omitReasoningSummary omits reasoning.summary from a Responses body.
	omitReasoningSummary bool
}
