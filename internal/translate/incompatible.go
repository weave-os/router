package translate

import "errors"

// ErrModelTranslationRequirementsIncompatible marks a routed target that
// cannot preserve capability-backed request semantics.
var ErrModelTranslationRequirementsIncompatible = errors.New("model cannot preserve translation requirements")

// IsIntrinsicallyIncompatible reports whether err marks a request the routed
// model provably cannot serve (unrepresentable tool schema, reasoning intent,
// or missing Gemini thought signatures on tool history) — as opposed to a
// transient upstream fault.
func IsIntrinsicallyIncompatible(err error) bool {
	return errors.Is(err, ErrGeminiSchemaIncompatible) ||
		errors.Is(err, ErrReasoningIncompatible) ||
		errors.Is(err, ErrGeminiUnsignedToolHistory) ||
		errors.Is(err, ErrModelTranslationRequirementsIncompatible)
}
