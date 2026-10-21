package catalog

import "weave-os/router/internal/providers"

// Tokenizer names the token vocabulary a model family bills input in. The
// same conversation produces different token counts under different
// tokenizers, so a count measured on one model is not a size on another.
type Tokenizer string

const (
	// TokenizerUnknown means no measured conversion; counts pass through.
	TokenizerUnknown Tokenizer = ""
	// TokenizerAnthropic is the Claude family's tokenizer.
	TokenizerAnthropic Tokenizer = "anthropic"
	// TokenizerOpenAI is the GPT family's tokenizer.
	TokenizerOpenAI Tokenizer = "openai"
)

// anthropicPerOpenAIInputTokens is how many Anthropic-tokenizer tokens one
// OpenAI-tokenizer token of agent-harness history becomes. Measured from
// production Claude Code threads that switched between gpt-6-luna and
// claude-opus-5-5 within two minutes (Oct 2026): Luna→Opus median 1.72
// (IQR 1.63–1.81, n≈6.4K), Opus→Luna median 0.57 (n≈8.5K), stable across
// history sizes from 30K to 900K tokens. The value sits at the upper
// quartile: over-reporting makes a client compact slightly early, while
// under-reporting can leave the compaction request itself too long for Claude.
const anthropicPerOpenAIInputTokens = 1.8

// TokenizerFor reports the tokenizer a catalog model bills input in, from its
// primary binding's wire family. Unknown models report TokenizerUnknown.
func TokenizerFor(id string) Tokenizer {
	m, ok := ByID(id)
	if !ok {
		return TokenizerUnknown
	}
	switch m.PrimaryProvider() {
	case providers.ProviderAnthropic:
		return TokenizerAnthropic
	case providers.ProviderOpenAI:
		return TokenizerOpenAI
	default:
		return TokenizerUnknown
	}
}

// InputTokenScale returns the factor converting input tokens billed by served
// into the requested model's tokenizer. It is 1 for the same model, the same
// tokenizer, or any pair without a measured conversion.
func InputTokenScale(served, requested string) float64 {
	from, to := TokenizerFor(served), TokenizerFor(requested)
	switch {
	case from == TokenizerOpenAI && to == TokenizerAnthropic:
		return anthropicPerOpenAIInputTokens
	case from == TokenizerAnthropic && to == TokenizerOpenAI:
		return 1 / anthropicPerOpenAIInputTokens
	default:
		return 1
	}
}
