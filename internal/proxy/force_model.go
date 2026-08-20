package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"workweave/router/internal/observability"
	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
)

// ForceModelHeader pins the session to a specific model, mirroring the
// /force-model chat command. Needed for headless clients (eval harness, CI
// smoke runs): Claude Code eats "/force-model …" as a client-side slash
// command before it reaches the router. The header rides on every request,
// so the pin is (re)written and served on the same turn. Values that name no
// catalog model fail the request; so do excluded ones.
const ForceModelHeader = "x-weave-force-model"

// ErrForcedModelUnknown is returned when a caller forces a model name that
// resolves to no catalog entry. Failing is the point: routing on regardless
// serves a model the caller never asked for while looking like the force took.
var ErrForcedModelUnknown = errors.New("forced model is not a known model")

// ForcedModelUnknownError carries the unresolvable value so the dispatch
// classifier can quote it back.
type ForcedModelUnknownError struct {
	Model string
}

// Error implements error.
func (e *ForcedModelUnknownError) Error() string {
	return fmt.Sprintf("%q is not a known model", e.Model)
}

// Unwrap ties the typed error to ErrForcedModelUnknown for errors.Is.
func (e *ForcedModelUnknownError) Unwrap() error { return ErrForcedModelUnknown }

var forceModelAliases = map[string]string{
	"anthropic":   "claude-opus-5",
	"claude":      "claude-opus-5",
	"opus":        "claude-opus-5",
	"claude-opus": "claude-opus-5",
	"opus-5":      "claude-opus-5",
	"opus-5.0":    "claude-opus-5",
	"opus5":       "claude-opus-5",
	"claude-5":    "claude-opus-5",
	// Opus 4.8 is retired from routing but still servable as passthrough;
	// keep its own-name aliases so direct pins resolve.
	"opus-4-8":      "claude-opus-4-8",
	"opus-4.8":      "claude-opus-4-8",
	"claude-4-8":    "claude-opus-4-8",
	"claude-4.8":    "claude-opus-4-8",
	"fable":         "claude-fable-5",
	"fable-5":       "claude-fable-5",
	"fable5":        "claude-fable-5",
	"claude-fable":  "claude-fable-5",
	"sonnet":        "claude-sonnet-5",
	"claude-sonnet": "claude-sonnet-5",
	"sonnet-5":      "claude-sonnet-5",
	"sonnet-4-6":    "claude-sonnet-4-6",
	"sonnet-4.6":    "claude-sonnet-4-6",
	"haiku":         "claude-haiku-4-5",
	"claude-haiku":  "claude-haiku-4-5",
	"haiku-4-5":     "claude-haiku-4-5",
	"haiku-4.5":     "claude-haiku-4-5",
	// Generic GPT aliases follow the current flagship, not a pinned version;
	// they pointed at gpt-5.5 until it was retired. Version-specific aliases
	// (gpt-5-5*) deliberately still resolve to their exact model, which stays
	// available as priced passthrough.
	"gpt":    "gpt-5.6-sol",
	"openai": "gpt-5.6-sol",
	// The bare gpt-5.6 alias routes to Sol, matching OpenAI's own alias.
	"gpt-5.6":       "gpt-5.6-sol",
	"gpt-5-6":       "gpt-5.6-sol",
	"sol":           "gpt-5.6-sol",
	"gpt-5-6-sol":   "gpt-5.6-sol",
	"terra":         "gpt-5.6-terra",
	"gpt-5-6-terra": "gpt-5.6-terra",
	"luna":          "gpt-5.6-luna",
	"gpt-5-6-luna":  "gpt-5.6-luna",
	"gpt-5-5":       "gpt-5.5",
	"gpt-5-5-pro":   "gpt-5.5-pro",
	"gpt-5-5-mini":  "gpt-5.5-mini",
	"gpt-5-5-nano":  "gpt-5.5-nano",
	"grok":          "grok-4.5",
	"grok-4.5":      "grok-4.5",
	"grok4.5":       "grok-4.5",
	"xai":           "grok-4.5",
	// "grok" stays at 4.5 for backward compat; "grok-max" surfaces 4.6.
	"grok-4.6":              "grok-4.6",
	"grok4.6":               "grok-4.6",
	"grok-max":              "grok-4.6",
	"gpt-5-4":               "gpt-5.4",
	"gpt-5-4-pro":           "gpt-5.4-pro",
	"gpt-5-4-mini":          "gpt-5.4-mini",
	"gpt-5-4-nano":          "gpt-5.4-nano",
	"google":                "gemini-3-pro-preview",
	"gemini":                "gemini-3-pro-preview",
	"gemini-pro":            "gemini-3-pro-preview",
	"gemini-flash":          "gemini-3-flash-preview",
	"gemini-3-6-flash":      "gemini-3.6-flash",
	"gemini-3-5-flash-lite": "gemini-3.5-flash-lite",
	"gemini-3-7-flash":      "gemini-3.7-flash",
	// The family alias follows Makora's V4-Pro EOL onto Flash; deepseek-pro
	// still names V4-Pro explicitly, which is passthrough-only now.
	"deepseek":       "deepseek/deepseek-v4-flash",
	"deepseek-pro":   "deepseek/deepseek-v4-pro",
	"deepseek-flash": "deepseek/deepseek-v4-flash",
	"qwen":           "qwen/qwen3-coder",
	"qwen-coder":     "qwen/qwen3-coder",
	// qwen3.7-plus is retired from routing but still servable as passthrough;
	// keep its own-name alias so direct pins resolve.
	"qwen3.7-plus": "qwen/qwen3.7-plus",
	"qwen-max":     "qwen/qwen3.8-max",
	"qwen3.8-max":  "qwen/qwen3.8-max",
	"qwen3.8":      "qwen/qwen3.8-max",
	// Generic kimi alias stays on 2.7; k3 is ~3x the price, so it needs an
	// explicit pin rather than silently repricing everyone on the family alias.
	"kimi":      "moonshotai/kimi-k2.7",
	"kimi-k3":   "moonshotai/kimi-k3",
	"kimi-k2.7": "moonshotai/kimi-k2.7",
	"kimi-k2.6": "moonshotai/kimi-k2.6",
	// Generic glm/zai aliases stay on 5.1 (Together/Fireworks/OpenRouter);
	// 5.2 is Fireworks-only day-0, so it requires an explicit pin.
	"glm":          "z-ai/glm-5.1",
	"zai":          "z-ai/glm-5.1",
	"z-ai":         "z-ai/glm-5.1",
	"glm-5.2":      "z-ai/glm-5.2",
	"glm-5.1":      "z-ai/glm-5.1",
	"glm-5":        "z-ai/glm-5",
	"minimax":      "minimax/minimax-m3",
	"minimax-m3":   "minimax/minimax-m3",
	"minimax-m2.7": "minimax/minimax-m2.7",
	"mistral":      "mistralai/mistral-small-2603",
}

// resolveForceModel is the legacy two-return surface. New pin-and-effort
// callers use resolveForceModelWithEffort.
func resolveForceModel(model string) (canonicalID, provider string, known bool) {
	canon, prov, kn, _ := resolveForceModelWithEffort(model)
	return canon, prov, kn
}

// forceModelNameKnown is the translate.ForceModelKnown the command parser uses
// to decide how many words of "/fm qwen 3.8 fix the bug" are the model name.
// It answers with the same resolver that later performs the pin, so the parser
// and the pin can never disagree about where the model name ends.
func forceModelNameKnown(candidate string) bool {
	_, _, known := resolveForceModel(candidate)
	return known
}

// resolveForceModelWithEffort is like resolveForceModel but also strips a
// `:level` suffix. `known` is true only for catalog matches; known=false +
// effort!="" lets callers surface "model not found" without losing the effort.
func resolveForceModelWithEffort(model string) (canonicalID, provider string, known bool, effort string) {
	effortLevel, stripped := stripEffortSuffix(model)
	model = stripped
	model = strings.ToLower(strings.TrimSpace(model))
	effort = effortLevel
	unknownID := model
	requiredProvider := ""
	if nativeID, ok := strings.CutPrefix(model, "openai/"); ok {
		model = nativeID
		unknownID = nativeID
		requiredProvider = providers.ProviderOpenAI
	}
	if alias, ok := forceModelAliases[model]; ok {
		model = alias
	}
	if m, ok := catalog.ByID(model); ok && len(m.Providers) > 0 && (requiredProvider == "" || m.Providers[0].Provider == requiredProvider) {
		return m.ID, m.Providers[0].Provider, true, effort
	}
	if !strings.Contains(model, "/") {
		suffix := "/" + model
		var matched catalog.Model
		var matches int
		for _, m := range catalog.Models {
			if strings.HasSuffix(m.ID, suffix) && len(m.Providers) > 0 && (requiredProvider == "" || m.Providers[0].Provider == requiredProvider) {
				matched = m
				matches++
			}
		}
		if matches == 1 && len(matched.Providers) > 0 {
			return matched.ID, matched.Providers[0].Provider, true, effort
		}
	}
	// Separator-insensitive retry: users type the model the way they say it
	// ("qwen 3.8", "gpt 5.6 sol"), not the way the catalog spells it. Compare
	// on a form with every separator removed so spaces, hyphens, dots, and
	// underscores are all interchangeable. Runs only after the exact lookups
	// above, so it can never override a precise match.
	if id, prov, ok := resolveForceModelLoose(model, requiredProvider); ok {
		return id, prov, true, effort
	}
	if requiredProvider != "" {
		return unknownID, requiredProvider, false, effort
	}
	switch {
	case strings.HasPrefix(model, "claude-"):
		return model, providers.ProviderAnthropic, false, effort
	case strings.HasPrefix(model, "gpt-"),
		model == "o1", model == "o3", model == "o1-pro", model == "o3-pro",
		strings.HasPrefix(model, "o1-"), strings.HasPrefix(model, "o3-"), strings.HasPrefix(model, "o4-"):
		return model, providers.ProviderOpenAI, false, effort
	case strings.HasPrefix(model, "gemini-"):
		return model, providers.ProviderGoogle, false, effort
	case strings.Contains(model, "/"):
		return model, providers.ProviderOpenRouter, false, effort
	default:
		return model, providers.ProviderAnthropic, false, effort
	}
}

// resolveForceModelLoose matches model against aliases and catalog IDs with
// all separators removed. Trailing path segments also match ("qwen3.8max" →
// "qwen/qwen3.8-max"). Ambiguous matches are refused. Leading/trailing
// separators ("gpt-") are refused: folding them would pin the wrong model.
func resolveForceModelLoose(model, requiredProvider string) (canonicalID, provider string, known bool) {
	if model == "" || isModelSeparator(rune(model[0])) || isModelSeparator(rune(model[len(model)-1])) {
		return "", "", false
	}
	key := foldModelSeparators(model)
	if key == "" {
		return "", "", false
	}
	if alias, ok := foldedForceModelAliases[key]; ok {
		if m, found := catalog.ByID(alias); found && len(m.Providers) > 0 &&
			(requiredProvider == "" || m.Providers[0].Provider == requiredProvider) {
			return m.ID, m.Providers[0].Provider, true
		}
	}
	var matched catalog.Model
	matches := 0
	for _, m := range catalog.Models {
		if len(m.Providers) == 0 || (requiredProvider != "" && m.Providers[0].Provider != requiredProvider) {
			continue
		}
		folded := foldModelSeparators(m.ID)
		_, tail, hasSlash := strings.Cut(m.ID, "/")
		if folded != key && !(hasSlash && foldModelSeparators(tail) == key) {
			continue
		}
		matched = m
		matches++
	}
	if matches != 1 {
		return "", "", false
	}
	return matched.ID, matched.Providers[0].Provider, true
}

// foldModelSeparators lowercases and drops every character that only ever
// separates words in a model name, so "Qwen 3.8 Max", "qwen-3.8-max", and
// "qwen3.8max" all fold together.
func foldModelSeparators(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if isModelSeparator(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isModelSeparator reports whether r only ever separates words in a model
// name, making it insignificant for matching.
func isModelSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '-', '_', '.', '/':
		return true
	default:
		return false
	}
}

// foldedForceModelAliases indexes forceModelAliases by folded key so the loose
// pass reaches aliases too ("qwen max" → "qwen-max" → qwen/qwen3.8-max).
// Collisions after folding are dropped: two aliases that fold alike can point
// at different models, and guessing between them is what breaks trust here.
var foldedForceModelAliases = func() map[string]string {
	folded := make(map[string]string, len(forceModelAliases))
	ambiguous := make(map[string]struct{})
	for alias, target := range forceModelAliases {
		key := foldModelSeparators(alias)
		if existing, seen := folded[key]; seen && existing != target {
			ambiguous[key] = struct{}{}
			continue
		}
		folded[key] = target
	}
	for key := range ambiguous {
		delete(folded, key)
	}
	return folded
}()

// stripEffortSuffix splits a `:level` suffix off model, canonicalizes it via
// CanonicalizeEffort, and returns ("", model) when no recognized suffix found.
func stripEffortSuffix(model string) (effort string, modelOut string) {
	const sep = ":"
	idx := strings.LastIndex(model, sep)
	if idx < 0 || idx == len(model)-1 {
		return "", model
	}
	tail := strings.TrimSpace(model[idx+1:])
	if !looksLikeEffortAlias(tail) {
		return "", model
	}
	return translate.CanonicalizeEffort(tail), model[:idx]
}

// looksLikeEffortAlias guards against future catalog IDs that contain `:`,
// ensuring the colon is only treated as a suffix separator for known levels.
func looksLikeEffortAlias(tail string) bool {
	switch strings.ToLower(strings.TrimSpace(tail)) {
	case "fast", "low", "medium", "med", "high", "max", "xhigh",
		"ultra", "minimal", "min":
		return true
	default:
		return false
	}
}

// setForceModelPin upserts an immutable user-forced session pin. It preserves
// the prior pin's LastServedModel so the next turn can detect a mid-session
// model switch and strip stale Anthropic thinking-block signatures. No-op if
// the pin store is unconfigured or installationID is nil.
func (s *Service) setForceModelPin(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	installationID uuid.UUID,
	canonicalModel, provider string,
) error {
	if s.pinStore == nil || installationID == uuid.Nil {
		return nil
	}
	log := observability.FromContext(ctx)
	var lastServedModel string
	existing, found, err := s.pinStore.Get(ctx, sessionKey, role)
	if err != nil {
		log.Error("force-model: prior pin lookup failed", "err", err)
	} else if found {
		lastServedModel = existing.LastServedModel
	}
	forced := sessionpin.Pin{
		SessionKey:      sessionKey,
		Role:            role,
		InstallationID:  installationID,
		Provider:        provider,
		Model:           canonicalModel,
		Reason:          translate.ReasonUserForceModel,
		TurnCount:       1,
		PinnedUntil:     pinNeverExpires,
		LastServedModel: lastServedModel,
	}
	// context.Background(): ctx may already be canceled here (response written,
	// client disconnected); a canceled ctx would leave the prior pin stuck.
	return s.pinStore.Upsert(context.Background(), forced)
}

// applyForceModelHeader honors the x-weave-force-model request header,
// writing the same session pin the /force-model command writes. It's
// (re)written on every request carrying the header. A model that names no
// catalog entry, or one the exclusion policy forbids, fails the request —
// silently routing elsewhere would serve a model the caller never asked for.
//
// A `:level` suffix is stashed on context as router.Overrides.ForceEffort
// so pin + effort land in one header.
func (s *Service) applyForceModelHeader(
	ctx context.Context,
	r *http.Request,
	env *translate.RequestEnvelope,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
) (string, error) {
	raw := strings.TrimSpace(r.Header.Get(ForceModelHeader))
	if raw == "" {
		return "", nil
	}
	log := observability.FromContext(ctx)
	canonicalModel, provider, known, effortLevel := resolveForceModelWithEffort(raw)
	if effortLevel != "" {
		// Merge with any existing knobs so ForceEffort doesn't drop Alpha/QualityBias.
		merged := router.Overrides{ForceEffort: effortLevel}
		if existing := router.RoutingKnobsFromContext(r.Context()); existing != nil {
			merged.Alpha = existing.Alpha
			merged.QualityBias = existing.QualityBias
			merged.SpeedWeight = existing.SpeedWeight
			merged.OutputCostRatio = existing.OutputCostRatio
			merged.ExpectedOutputTokens = existing.ExpectedOutputTokens
			merged.PerModelVerbosity = existing.PerModelVerbosity
		}
		// Mutate *r so the caller's downstream routingKnobsForRequest
		// (which reads ctx from r.Context()) discovers the knob.
		*r = *r.WithContext(router.WithRoutingKnobs(r.Context(), &merged))
	}
	if !known {
		log.Warn("x-weave-force-model: rejected unrecognized model",
			"input_model", raw,
			"session_key_hex", fmt.Sprintf("%x", sessionKey),
		)
		return "", &ForcedModelUnknownError{Model: raw}
	}
	binding, reason := s.forcedModelBinding(ctx, canonicalModel, provider)
	if reason != "" {
		log.Warn("x-weave-force-model: rejected excluded model",
			"input_model", raw,
			"canonical_model", canonicalModel,
			"provider", provider,
			"reason", reason,
		)
		return "", &ForcedModelExcludedError{Model: canonicalModel, Reason: reason}
	}
	provider = binding
	if s.pinStore == nil {
		return canonicalModel, nil
	}
	role := roleForTier(catalog.TierFor(env.Model()))
	if err := s.setForceModelPin(ctx, sessionKey, role, installationID, canonicalModel, provider); err != nil {
		log.Error("x-weave-force-model: pin store upsert failed", "err", err)
		return canonicalModel, nil
	}
	log.Info("x-weave-force-model applied",
		"input_model", raw,
		"canonical_model", canonicalModel,
		"provider", provider,
		"effort", effortLevel,
		"session_key_hex", fmt.Sprintf("%x", sessionKey),
		"role", role,
	)
	return canonicalModel, nil
}

// handleForceModelCommand processes a /force-model or /unforce-model directive:
// writes (or expires) the session pin and returns a synthetic acknowledgment
// response without dispatching upstream. inputTokens should be the request's
// RoutingFeatures.Tokens so the token counter reflects actual turn input, not
// just the synthetic response text.
func (s *Service) handleForceModelCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	cmd translate.ForceModelResult,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	inputTokens int,
) error {
	log := observability.FromContext(ctx)
	role := roleForTier(catalog.TierFor(env.Model()))

	// Formatted as a routing marker (✦ **Weave Router** → …\n\n) so
	// StripRoutingMarkerFromMessages strips it from later inbound requests;
	// otherwise it'd persist in history and leak router internals upstream.
	var msg string
	if cmd.List {
		// A bare /force-model is a request to see the options, not a failed
		// pin: answer with the listing and leave any existing pin alone.
		msg = renderForceModelListing(s.pinnableModels(ctx), env.SourceFormat())
		log.Debug("/force-model: listed pinnable models",
			"session_key_hex", fmt.Sprintf("%x", sessionKey),
			"role", role,
		)
	} else if cmd.Clear {
		if s.pinStore != nil && installationID != uuid.Nil {
			if err := s.expireSessionPin(ctx, installationID, sessionKey, role, "user_unforced"); err != nil {
				log.Error("/unforce-model: pin store upsert failed", "err", err)
				return err
			}
		}
		msg = "✦ **Weave Router** → force-model cleared · resuming automatic model selection\n\n"
		if env.SourceFormat() == translate.FormatOpenAI {
			msg = "Weave Router: force-model cleared; resuming automatic model selection"
		}
		// Debug not Info: fires on every command use, not a major business event.
		log.Debug("/unforce-model: session pin cleared",
			"session_key_hex", fmt.Sprintf("%x", sessionKey),
			"role", role,
		)
	} else if canonicalModel, provider, known := resolveForceModel(cmd.Model); !known {
		// Not in the catalog (e.g. truncated "/force-model gpt-") — reject
		// rather than pin something we can't honor; prior pin left untouched.
		// The suggestions come from the same gate that admits a pin, so a
		// user who guessed wrong is shown ids that will actually work.
		log.Info("/force-model: rejected unknown model",
			"input_model", cmd.Model,
			"session_key_hex", fmt.Sprintf("%x", sessionKey),
			"role", role,
		)
		msg = renderForceModelRejection(cmd.Model, s.pinnableModels(ctx), env.SourceFormat())
	} else if binding, reason := s.forcedModelBinding(ctx, canonicalModel, provider); reason != "" {
		// Exclusions outrank the force. Pinning anyway would look accepted and
		// then serve something else every turn; the prior pin is left untouched.
		log.Warn("/force-model: rejected excluded model",
			"input_model", cmd.Model,
			"canonical_model", canonicalModel,
			"provider", provider,
			"reason", reason,
			"session_key_hex", fmt.Sprintf("%x", sessionKey),
			"role", role,
		)
		msg = fmt.Sprintf("✦ **Weave Router** → force-model rejected: %s · keeping automatic routing. Ask an admin to allow the provider, or force a model from one that is permitted.\n\n", reason)
		if env.SourceFormat() == translate.FormatOpenAI {
			msg = fmt.Sprintf("Weave Router: force-model rejected: %s; keeping automatic routing. Ask an admin to allow the provider, or force a model from one that is permitted.", reason)
		}
	} else {
		if err := s.setForceModelPin(ctx, sessionKey, role, installationID, canonicalModel, binding); err != nil {
			log.Error("/force-model: pin store upsert failed", "err", err)
			return err
		}
		msg = fmt.Sprintf("✦ **Weave Router** → force-model applied: %s (%s) · Use /unforce-model to clear\n\n", canonicalModel, binding)
		if env.SourceFormat() == translate.FormatOpenAI {
			msg = fmt.Sprintf("Weave Router: force-model applied: %s (%s). Use /unforce-model to clear.", canonicalModel, binding)
		}
		log.Debug("/force-model: session pin set",
			"input_model", cmd.Model,
			"canonical_model", canonicalModel,
			"provider", binding,
			"session_key_hex", fmt.Sprintf("%x", sessionKey),
			"role", role,
		)
	}

	switch env.SourceFormat() {
	case translate.FormatOpenAI:
		return writeSyntheticOpenAIResponse(w, env, msg, inputTokens)
	default:
		return writeSyntheticAnthropicResponse(w, env, msg, inputTokens)
	}
}

// writeSyntheticAnthropicResponse writes a minimal Anthropic Messages API
// response without hitting an upstream, handling both streaming and
// non-streaming shapes.
func writeSyntheticAnthropicResponse(w http.ResponseWriter, env *translate.RequestEnvelope, text string, inputTokens int) error {
	msgID := fmt.Sprintf("msg_router_cmd_%x", time.Now().UnixNano())
	if env.Stream() {
		return writeSyntheticAnthropicSSE(w, msgID, text, inputTokens)
	}
	return writeSyntheticAnthropicJSON(w, msgID, text, inputTokens)
}

func writeSyntheticAnthropicJSON(w http.ResponseWriter, msgID, text string, inputTokens int) error {
	resp := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         "weave-router",
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": len(text) / 4,
		},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal synthetic response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, writeErr := w.Write(body)
	return writeErr
}

func writeSyntheticAnthropicSSE(w http.ResponseWriter, msgID, text string, inputTokens int) error {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 4096)

	outTokens := len(text) / 4

	events := []string{
		sseEvent("message_start", mustMarshalJSON(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": msgID, "type": "message", "role": "assistant",
				"content": []any{}, "model": "weave-router",
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
			},
		})),
		sseEvent("content_block_start", mustMarshalJSON(map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})),
		sseEvent("ping", `{"type":"ping"}`),
		sseEvent("content_block_delta", mustMarshalJSON(map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text},
		})),
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sseEvent("message_delta", mustMarshalJSON(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": outTokens},
		})),
		sseEvent("message_stop", `{"type":"message_stop"}`),
	}

	for _, ev := range events {
		bw.WriteString(ev)
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// writeSyntheticOpenAIResponse writes a minimal OpenAI Chat Completions
// response without hitting an upstream, handling both streaming and
// non-streaming shapes.
func writeSyntheticOpenAIResponse(w http.ResponseWriter, env *translate.RequestEnvelope, text string, inputTokens int) error {
	respID := fmt.Sprintf("chatcmpl_router_cmd_%x", time.Now().UnixNano())
	if env.Stream() {
		return writeSyntheticOpenAISSE(w, respID, text, inputTokens)
	}
	return writeSyntheticOpenAIJSON(w, respID, text, inputTokens)
}

func writeSyntheticOpenAIJSON(w http.ResponseWriter, respID, text string, inputTokens int) error {
	outTokens := len(text) / 4
	resp := map[string]any{
		"id":      respID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "weave-router",
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outTokens,
			"total_tokens":      inputTokens + outTokens,
		},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal synthetic openai response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, writeErr := w.Write(body)
	return writeErr
}

func writeSyntheticOpenAISSE(w http.ResponseWriter, respID, text string, inputTokens int) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 4096)
	created := time.Now().Unix()
	outTokens := len(text) / 4
	chunkStart := mustMarshalJSON(map[string]any{
		"id":      respID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   "weave-router",
		"choices": []any{
			map[string]any{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": nil,
			},
		},
	})
	chunkStop := mustMarshalJSON(map[string]any{
		"id":      respID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   "weave-router",
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outTokens,
			"total_tokens":      inputTokens + outTokens,
		},
	})
	events := []string{
		openAISSEData(chunkStart),
		openAISSEData(chunkStop),
		openAISSEData("[DONE]"),
	}
	for _, ev := range events {
		bw.WriteString(ev)
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func sseEvent(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

func openAISSEData(data string) string {
	return "data: " + data + "\n\n"
}

func mustMarshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
