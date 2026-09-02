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
	// grok-4.5 is retired from routing (no AA Agentic Index score, never rostered).
	// Family aliases follow flagship 4.6; own-name alias keeps grok-4.5 as passthrough.
	"grok":                  "grok-4.6",
	"grok-4.5":              "grok-4.5",
	"grok4.5":               "grok-4.5",
	"xai":                   "grok-4.6",
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
	"gemini-3-8-flash":      "gemini-3.8-flash",
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
	// Dash-form spellings people type from memory; the catalog ID uses dots.
	"qwen/qwen-3.8-max": "qwen/qwen3.8-max",
	"qwen-3.8-max":      "qwen/qwen3.8-max",
	"qwen-3.8":          "qwen/qwen3.8-max",
	// Generic kimi alias stays on 2.7; k3 is ~3x the price, so it needs an
	// explicit pin rather than silently repricing everyone on the family alias.
	"kimi":      "moonshotai/kimi-k2.7",
	"kimi-k3":   "moonshotai/kimi-k3",
	"kimi-k2.7": "moonshotai/kimi-k2.7",
	"kimi-k2.6": "moonshotai/kimi-k2.6",
	// Generic glm/zai aliases stay on 5.1 (Together/Fireworks/OpenRouter);
	// 5.2 is Fireworks-only day-0, so it requires an explicit pin.
	"glm":           "z-ai/glm-5.1",
	"zai":           "z-ai/glm-5.1",
	"z-ai":          "z-ai/glm-5.1",
	"glm-5.3-flash": "z-ai/glm-5.3-flash",
	"glm-5.3":       "z-ai/glm-5.3",
	"glm-5.2":       "z-ai/glm-5.2",
	"glm-5.1":       "z-ai/glm-5.1",
	"glm-5":         "z-ai/glm-5",
	"minimax":       "minimax/minimax-m3",
	"minimax-m3":    "minimax/minimax-m3",
	"minimax-m2.7":  "minimax/minimax-m2.7",
	"mistral":       "mistralai/mistral-small-2603",
}

// resolveForceModel is the legacy two-return surface. New pin-and-effort
// callers use resolveForceModelWithEffort.
func resolveForceModel(model string) (canonicalID, provider string, known bool) {
	canon, prov, kn, _ := resolveForceModelWithEffort(model)
	return canon, prov, kn
}

// resolveForceModelWithEffort is like resolveForceModel but also strips a
// `:level` suffix. `known` is true only for catalog matches; known=false +
// effort!="" lets callers surface "model not found" without losing the effort.
//
// Matching is exact: no prefix, substring, or nearest-match fallback.
// Approximate matching silently served the wrong model; an unrecognized
// name must fail loudly instead.
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
	} else if canonical, ok := bareCatalogNames[model]; ok {
		model = canonical
	}
	if m, ok := catalog.ByID(model); ok && len(m.Providers) > 0 && (requiredProvider == "" || m.Providers[0].Provider == requiredProvider) {
		return m.ID, m.Providers[0].Provider, true, effort
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

// bareCatalogNames maps a slash-form model's bare tail to its canonical
// ID ("qwen3-coder" -> "qwen/qwen3-coder") for vendor-prefix-optional lookup.
// Tails that are ambiguous, match a full catalog ID, or duplicate an alias
// are excluded; TestBareCatalogNames_Unambiguous asserts the invariant.
var bareCatalogNames = func() map[string]string {
	owners := make(map[string][]string)
	for _, m := range catalog.Models {
		if _, tail, ok := strings.Cut(m.ID, "/"); ok && len(m.Providers) > 0 {
			owners[tail] = append(owners[tail], m.ID)
		}
	}
	out := make(map[string]string, len(owners))
	for tail, ids := range owners {
		if len(ids) > 1 {
			continue
		}
		if _, isFullID := catalog.ByID(tail); isFullID {
			continue
		}
		if _, aliased := forceModelAliases[tail]; aliased {
			continue
		}
		out[tail] = ids[0]
	}
	return out
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

const (
	forceModelSessionRole       = "force_model"
	forceModelHistoryRoleSuffix = "_force_hist"
	forceModelHistoryReason     = "force_model_history"
	userUnforcedReason          = "user_unforced"
)

func forceModelHistoryRole(role string) string {
	if role == "" {
		role = sessionpin.DefaultRole
	}
	return role + forceModelHistoryRoleSuffix
}

func (s *Service) preserveForceModelControlHistory(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	nextModel string,
) error {
	existing, found, err := s.pinStore.Get(ctx, sessionKey, forceModelSessionRole)
	if err != nil {
		return err
	}
	if !found || !isUserForcedReason(existing.Reason) || existing.Model == "" || existing.Model == nextModel {
		return nil
	}
	return s.pinStore.UpdateUsage(context.Background(), sessionKey, forceModelSessionRole, sessionpin.Usage{
		Strategy:            existing.Strategy,
		EndedAt:             time.Now(),
		ServedModel:         existing.Model,
		ServedProvider:      existing.Provider,
		PriorServedModel:    existing.LastServedModel,
		SessionEverSwitched: existing.HasEverSwitched,
	})
}

// setForceModelSessionPin writes the session-wide control row. Ordinary turns
// never refresh this row; only another force or an explicit clear changes it.
func (s *Service) setForceModelSessionPin(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	installationID uuid.UUID,
	canonicalModel, provider string,
) error {
	if s.pinStore == nil || installationID == uuid.Nil {
		return nil
	}
	if err := s.preserveForceModelControlHistory(ctx, sessionKey, canonicalModel); err != nil {
		return fmt.Errorf("preserve force-model control history: %w", err)
	}
	forced := sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           forceModelSessionRole,
		InstallationID: installationID,
		Provider:       provider,
		Model:          canonicalModel,
		Reason:         translate.ReasonUserForceModel,
		TurnCount:      1,
		PinnedUntil:    pinNeverExpires,
	}
	return s.pinStore.Upsert(context.Background(), forced)
}

func (s *Service) loadForceModelSessionPin(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
) (sessionpin.Pin, bool, bool) {
	if s.pinStore == nil {
		return sessionpin.Pin{}, false, false
	}
	pin, found, err := s.pinStore.Get(ctx, sessionKey, forceModelSessionRole)
	if err != nil {
		observability.FromContext(ctx).Error("force-model session pin lookup failed", "err", err)
		return sessionpin.Pin{}, false, false
	}
	if found && pin.Reason == userUnforcedReason {
		return pin, false, true
	}
	if !found || !pin.PinnedUntil.After(time.Now()) || !isUserForcedReason(pin.Reason) || pin.Model == "" || pin.Provider == "" {
		return sessionpin.Pin{}, false, false
	}
	return pin, true, false
}

func (s *Service) loadForceModelHistory(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) sessionpin.Pin {
	if s.pinStore == nil {
		return sessionpin.Pin{}
	}
	pin, found, err := s.pinStore.Get(ctx, sessionKey, forceModelHistoryRole(role))
	if err != nil {
		observability.FromContext(ctx).Error("force-model history lookup failed", "err", err)
		return sessionpin.Pin{}
	}
	if !found || !pin.PinnedUntil.After(time.Now()) {
		return sessionpin.Pin{}
	}
	return pin
}

func (s *Service) anchorForceModelHistory(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	forcedPin sessionpin.Pin,
) {
	if s.pinStore == nil || installationID == uuid.Nil {
		return
	}
	s.upsertPin(ctx, sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           forceModelHistoryRole(role),
		InstallationID: installationID,
		Provider:       forcedPin.Provider,
		Model:          forcedPin.Model,
		Reason:         forceModelHistoryReason,
		Strategy:       router.StrategyFromContext(ctx),
		TurnCount:      1,
		PinnedUntil:    pinNeverExpires,
	})
}

func forceModelClearRoles() []string {
	return []string{
		roleForTier(catalog.TierUnknown),
		roleForTier(catalog.TierLow),
		roleForTier(catalog.TierMid),
		roleForTier(catalog.TierHigh),
	}
}

func (s *Service) clearLegacyForceModelPins(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
) error {
	if s.pinStore == nil || installationID == uuid.Nil {
		return nil
	}
	for _, role := range forceModelClearRoles() {
		pin, found, err := s.pinStore.Get(ctx, sessionKey, role)
		if err != nil {
			return fmt.Errorf("load legacy force-model pin for role %q: %w", role, err)
		}
		if !found || !isUserForcedReason(pin.Reason) {
			continue
		}
		if err := s.expireSessionPin(ctx, installationID, sessionKey, role, userUnforcedReason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) clearForceModelSessionPin(
	ctx context.Context,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
) error {
	if s.pinStore == nil || installationID == uuid.Nil {
		return nil
	}
	if err := s.preserveForceModelControlHistory(ctx, sessionKey, ""); err != nil {
		return fmt.Errorf("preserve force-model control history before clear: %w", err)
	}
	// Keep a durable tombstone so a pre-session-scope user_forced row on any
	// child thread cannot revive after /unforce-model.
	cleared := sessionpin.Pin{
		SessionKey:     sessionKey,
		Role:           forceModelSessionRole,
		InstallationID: installationID,
		Reason:         userUnforcedReason,
		TurnCount:      1,
		PinnedUntil:    pinNeverExpires,
	}
	return s.pinStore.Upsert(context.Background(), cleared)
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
	installationID uuid.UUID,
	forceModelSessionKey [sessionpin.SessionKeyLen]byte,
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
			"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
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
	if err := s.setForceModelSessionPin(ctx, forceModelSessionKey, installationID, canonicalModel, provider); err != nil {
		log.Error("x-weave-force-model: session pin upsert failed", "err", err)
		return canonicalModel, nil
	}
	log.Info("x-weave-force-model applied",
		"input_model", raw,
		"canonical_model", canonicalModel,
		"provider", provider,
		"effort", effortLevel,
		"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
		"role", forceModelSessionRole,
	)
	return canonicalModel, nil
}

// handleForceModelCommand processes a user-issued directive and writes a
// synthetic acknowledgment without dispatching upstream. inputTokens is the
// request's RoutingFeatures.Tokens so counts reflect actual turn input.
func (s *Service) handleForceModelCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	cmd translate.ForceModelResult,
	installationID uuid.UUID,
	threadSessionKey, forceModelSessionKey [sessionpin.SessionKeyLen]byte,
	inputTokens int,
) error {
	_, msg, err := s.applyForceModelCommand(ctx, env, cmd, installationID, threadSessionKey, forceModelSessionKey)
	if err != nil {
		return err
	}
	switch env.SourceFormat() {
	case translate.FormatOpenAI:
		return writeSyntheticOpenAIResponse(w, env, msg, inputTokens)
	default:
		return writeSyntheticAnthropicResponse(w, env, msg, inputTokens)
	}
}

// applyForceModelCommand updates the session pin without deciding whether the
// caller should receive a synthetic response. It returns the canonical model
// when a force was applied.
func (s *Service) applyForceModelCommand(
	ctx context.Context,
	env *translate.RequestEnvelope,
	cmd translate.ForceModelResult,
	installationID uuid.UUID,
	threadSessionKey, forceModelSessionKey [sessionpin.SessionKeyLen]byte,
) (string, string, error) {
	log := observability.FromContext(ctx)

	// Formatted as a routing marker (✦ **Weave Router** → …\n\n) so
	// StripRoutingMarkerFromMessages strips it from later inbound requests;
	// otherwise it'd persist in history and leak router internals upstream.
	var msg string
	if cmd.Clear {
		if err := s.clearLegacyForceModelPins(ctx, installationID, threadSessionKey); err != nil {
			log.Error("/unforce-model: legacy pin cleanup failed", "err", err)
			return "", "", err
		}
		if err := s.clearForceModelSessionPin(ctx, installationID, forceModelSessionKey); err != nil {
			log.Error("/unforce-model: session pin clear failed", "err", err)
			return "", "", err
		}
		msg = "✦ **Weave Router** → force-model cleared · resuming automatic model selection\n\n"
		if env.SourceFormat() == translate.FormatOpenAI {
			msg = "Weave Router: force-model cleared; resuming automatic model selection"
		}
		log.Debug("/unforce-model: session pin cleared",
			"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
			"role", forceModelSessionRole,
		)
		return "", msg, nil
	}

	canonicalModel, provider, known := resolveForceModel(cmd.Model)
	if !known {
		log.Info("/force-model: rejected unknown model",
			"input_model", cmd.Model,
			"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
			"role", forceModelSessionRole,
		)
		msg = fmt.Sprintf("✦ **Weave Router** → force-model: %q isn't a recognized model · keeping automatic routing. Use a full model ID, e.g. claude-opus-5, gpt-5.5, or gemini-3-pro-preview.\n\n", cmd.Model)
		if env.SourceFormat() == translate.FormatOpenAI {
			msg = fmt.Sprintf("Weave Router: force-model: %q isn't a recognized model; keeping automatic routing. Use a full model ID, e.g. claude-opus-5, gpt-5.5, or gemini-3-pro-preview.", cmd.Model)
		}
		return "", msg, nil
	}

	binding, reason := s.forcedModelBinding(ctx, canonicalModel, provider)
	if reason != "" {
		log.Warn("/force-model: rejected excluded model",
			"input_model", cmd.Model,
			"canonical_model", canonicalModel,
			"provider", provider,
			"reason", reason,
			"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
			"role", forceModelSessionRole,
		)
		msg = fmt.Sprintf("✦ **Weave Router** → force-model rejected: %s · keeping automatic routing. Ask an admin to allow the provider, or force a model from one that is permitted.\n\n", reason)
		if env.SourceFormat() == translate.FormatOpenAI {
			msg = fmt.Sprintf("Weave Router: force-model rejected: %s; keeping automatic routing. Ask an admin to allow the provider, or force a model from one that is permitted.", reason)
		}
		return "", msg, nil
	}

	if err := s.clearLegacyForceModelPins(ctx, installationID, threadSessionKey); err != nil {
		log.Error("/force-model: legacy pin cleanup failed", "err", err)
		return "", "", err
	}
	if err := s.setForceModelSessionPin(ctx, forceModelSessionKey, installationID, canonicalModel, binding); err != nil {
		log.Error("/force-model: session pin upsert failed", "err", err)
		return "", "", err
	}
	msg = fmt.Sprintf("✦ **Weave Router** → force-model applied: %s (%s) · Use /unforce-model to clear\n\n", canonicalModel, binding)
	if env.SourceFormat() == translate.FormatOpenAI {
		msg = fmt.Sprintf("Weave Router: force-model applied: %s (%s). Use /unforce-model to clear.", canonicalModel, binding)
	}
	log.Debug("/force-model: session pin set",
		"input_model", cmd.Model,
		"canonical_model", canonicalModel,
		"provider", binding,
		"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
		"role", forceModelSessionRole,
	)
	return canonicalModel, msg, nil
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
