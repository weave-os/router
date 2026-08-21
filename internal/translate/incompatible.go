package translate

import "errors"

// IsIntrinsicallyIncompatible reports whether err marks a request the routed
// model provably cannot serve (unrepresentable tool schema, reasoning intent,
// or missing Gemini thought signatures on tool history) — as opposed to a
// transient upstream fault. The proxy uses it to fail over to the baseline
// Anthropic model instead of surfacing a generic 502.
func IsIntrinsicallyIncompatible(err error) bool {
	return errors.Is(err, ErrGeminiSchemaIncompatible) ||
		errors.Is(err, ErrReasoningIncompatible) ||
		errors.Is(err, ErrGeminiUnsignedToolHistory)
}
