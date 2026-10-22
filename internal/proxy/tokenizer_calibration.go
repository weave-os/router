package proxy

import (
	"context"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

// Cross-tokenizer usage reporting.
//
// A client budgets its own context management against the model it asked
// for: Claude Code auto-compacts when input_tokens + cache_read_input_tokens +
// cache_creation_input_tokens in a response approaches the requested model's
// window. When the router serves that turn on a model with a different
// tokenizer, the provider reports usage in its own tokens, and the same
// history is a different number of tokens in each. GPT bills a Claude Code
// history at roughly 0.55-0.65x of Claude's count (tool-call JSON compresses
// most, prose least), so a Luna-served thread reported ~600K while it was
// ~1M on Opus. Claude Code never compacted; Luna then hit its input cap, and
// the eventual compaction (or any return to Opus) was too long for Opus.
//
// The router therefore reports a cross-tokenizer turn's input usage in the
// requested model's tokens, using a ratio measured on the same thread: after
// every served turn it records the full prompt the serving tokenizer billed,
// and when the other tokenizer billed the thread within a couple of minutes
// the two counts give that thread's conversion (sessionpin.TokenizerCalibration).
// Production thread switches show a thread's previous ratio predicts its next
// one to within 0.5% (median), against 4.3% for a fleet-wide average, because
// the ratio depends on the thread's own mix of code, JSON and prose.
//
// Until a thread has been measured, usage is reported unscaled: the router
// never invents a conversion. Billing, telemetry, and the pin's own usage
// columns always keep the provider's counts; only the client projection is
// converted.

// tokenizerFamilyFor maps a serving binding (or, for the requested model, its
// catalog primary binding) to the tokenizer that bills its input. Bindings
// outside the two measured families report "" and are never calibrated.
func tokenizerFamilyFor(provider string) sessionpin.TokenizerFamily {
	switch provider {
	case providers.ProviderAnthropic, providers.ProviderAnthropicGateway:
		return sessionpin.TokenizerFamilyAnthropic
	case providers.ProviderOpenAI, providers.ProviderOpenAIGateway:
		return sessionpin.TokenizerFamilyOpenAI
	default:
		return ""
	}
}

// clientInputTokenScale returns the factor that converts input tokens billed
// by servedProvider into requestedModel's tokens for this thread, or 1 when
// they share a tokenizer or the thread has no measured conversion yet.
func clientInputTokenScale(res turnLoopResult, servedProvider, requestedModel string) float64 {
	// Ingress accepts provider-qualified ids ("anthropic/claude-opus-5-5");
	// catalog ids never carry their own provider prefix.
	requested, _ := catalog.ByID(stripRequestedProviderQualifier(requestedModel))
	from, to := tokenizerFamilyFor(servedProvider), tokenizerFamilyFor(requested.PrimaryProvider())
	if from == "" || to == "" || from == to {
		return 1
	}
	ratio, ok := res.TokenizerCalibration.Ratio(from, to)
	if !ok {
		return 1
	}
	return ratio
}

// fullPromptTokens is the whole history the provider billed for this turn.
// Anthropic reports fresh input with cache categories separate; OpenAI's
// input already includes cached tokens.
func fullPromptTokens(provider string, in, cacheCreation, cacheRead int) int {
	if providers.FamilyFor(provider) == providers.FamilyAnthropic {
		return in + cacheCreation + cacheRead
	}
	return in
}

// recordTokenizerObservation adds this turn's billed prompt to the thread's
// tokenizer calibration and persists it on the row recordTurnUsage writes.
// Only the thread's own conversation turns (main loop and tool results) are
// observed: utility, compaction and sub-agent prompts differ from the history
// the next turn will resend, so comparing them would measure two different
// prompts rather than two tokenizers.
func (s *Service) recordTokenizerObservation(ctx context.Context, res turnLoopResult, role, servedProvider string, in, cacheCreation, cacheRead int) {
	if res.TurnType != turntype.MainLoop && res.TurnType != turntype.ToolResult {
		return
	}
	store, ok := s.pinStore.(sessionpin.TokenizerCalibrationStore)
	if !ok {
		return
	}
	family := tokenizerFamilyFor(servedProvider)
	prompt := fullPromptTokens(servedProvider, in, cacheCreation, cacheRead)
	if family == "" || prompt <= 0 {
		return
	}
	cal := res.TokenizerCalibration.Observe(family, prompt, time.Now())
	// Detached from the request's cancellation like the usage writeback, but
	// logged through the request-scoped logger so the line carries its IDs.
	if err := store.UpdateTokenizerCalibration(context.WithoutCancel(ctx), res.SessionKey, role, strategyForTurnLoopResult(res), cal); err != nil {
		observability.FromContext(ctx).Error("tokenizer calibration writeback failed", "err", err)
	}
}

// stripRequestedProviderQualifier drops a leading "<provider>/" from a
// requested model id when the remainder is a catalog model, leaving catalog
// ids that legitimately contain a slash (e.g. "z-ai/glm-5.3") untouched.
func stripRequestedProviderQualifier(model string) string {
	if _, known := catalog.ByID(model); known {
		return model
	}
	provider, rest, qualified := strings.Cut(model, "/")
	if !qualified {
		return model
	}
	if stripped, hadPrefix := translate.StripProviderPrefix(model, provider); hadPrefix && rest != "" {
		if _, known := catalog.ByID(stripped); known {
			return stripped
		}
	}
	return model
}
