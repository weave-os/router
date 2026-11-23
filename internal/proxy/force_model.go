package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

var ErrForcedModelUnknown = errors.New("forced model is not a known model")

type ForcedModelUnknownError struct {
	Model string
}

func (e *ForcedModelUnknownError) Error() string {
	return fmt.Sprintf("%q is not a known model", e.Model)
}

func (e *ForcedModelUnknownError) Unwrap() error { return ErrForcedModelUnknown }

var forceModelExamples = fmt.Sprintf("opus, %s, %s, or %s",
	routingpolicy.ForceModelAliases["opus"], routingpolicy.ForceModelAliases["gpt"], routingpolicy.ForceModelAliases["gemini"])

func resolveForceModel(model string) (canonicalID, provider string, known bool) {
	canon, prov, kn, _ := resolveForceModelWithEffort(model)
	return canon, prov, kn
}

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
	if variant, ok := translate.CanonicalModel(model); ok {
		model = variant
		unknownID = variant
	}
	if id, ok := lookupForceModelName(model); ok {
		if m, ok := routingpolicy.ServableForceModel(id, requiredProvider); ok {
			return m.ID, m.Providers[0].Provider, true, effort
		}
	} else if effort == "" {
		if head, level, ok := routingpolicy.CutSpelledEffort(model); ok {
			head, _ = translate.CanonicalModel(head)
			if id, ok := lookupForceModelName(head); ok {
				if m, ok := routingpolicy.ServableForceModel(id, requiredProvider); ok {
					return m.ID, m.Providers[0].Provider, true, router.CanonicalizeEffort(level)
				}
			}
		}
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

func lookupForceModelName(name string) (string, bool) {
	if alias, ok := routingpolicy.ForceModelAliases[name]; ok {
		return alias, true
	}
	if canonical, ok := bareCatalogNames[name]; ok {
		return canonical, true
	}
	if _, ok := catalog.ByID(name); ok {
		return name, true
	}
	id, ok := foldedForceModelNames[routingpolicy.FoldModelSpelling(name)]
	return id, ok
}

func forceModelNames() map[string]string {
	names := make(map[string]string, len(catalog.Models)*2+len(routingpolicy.ForceModelAliases))
	for _, m := range catalog.Models {
		if len(m.Providers) == 0 {
			continue
		}
		names[m.ID] = m.ID
		if unprefixed, ok := strings.CutPrefix(m.ID, "claude-"); ok {
			names[unprefixed] = m.ID
		}
		if variantFirst, ok := variantFirstGPTName(m.ID); ok {
			names[variantFirst] = m.ID
		}
	}
	for bare, id := range bareCatalogNames {
		names[bare] = id
	}
	for alias, id := range routingpolicy.ForceModelAliases {
		names[alias] = id
	}
	return names
}

var gptVariantID = regexp.MustCompile(`^gpt-([0-9]+\.[0-9]+)-([a-z]+)$`)

func variantFirstGPTName(id string) (string, bool) {
	match := gptVariantID.FindStringSubmatch(id)
	if match == nil {
		return "", false
	}
	return match[2] + "-" + match[1], true
}

var foldedForceModelNames = func() map[string]string {
	out := make(map[string]string)
	ambiguous := make(map[string]bool)
	for name, id := range forceModelNames() {
		key := routingpolicy.FoldModelSpelling(name)
		if prev, seen := out[key]; seen && prev != id {
			ambiguous[key] = true
		}
		out[key] = id
	}
	for key := range ambiguous {
		delete(out, key)
	}
	return out
}()

type forceModelSuggestion struct{ name, id string }

func (c forceModelSuggestion) preferredOver(o forceModelSuggestion) bool {
	if len(c.name) != len(o.name) {
		return len(c.name) < len(o.name)
	}
	if (c.name == c.id) != (o.name == o.id) {
		return c.name == c.id
	}
	return c.name < o.name
}

var forceModelSuggestions = func() map[string]forceModelSuggestion {
	out := make(map[string]forceModelSuggestion, len(foldedForceModelNames))
	for name := range forceModelNames() {
		key := routingpolicy.FoldModelSpelling(name)
		id, ok := foldedForceModelNames[key]
		if !ok {
			continue
		}
		cand := forceModelSuggestion{name: name, id: id}
		if prev, seen := out[key]; seen && !cand.preferredOver(prev) {
			continue
		}
		out[key] = cand
	}
	return out
}()

func suggestForceModel(input string) string {
	effort, model := stripEffortSuffix(input)
	model, _ = translate.CanonicalModel(strings.ToLower(strings.TrimSpace(model)))
	key := routingpolicy.FoldModelSpelling(model)
	if len(key) < 3 {
		return ""
	}
	maxDist := 1
	if len(key) > 5 {
		maxDist = 2
	}
	digits := digitsOf(key)
	var best forceModelSuggestion
	bestDist, ambiguous := maxDist+1, false
	for k, cand := range forceModelSuggestions {
		if digitsOf(k) != digits {
			continue
		}
		d := editDistance(key, k)
		switch {
		case d < bestDist:
			best, bestDist, ambiguous = cand, d, false
		case d == bestDist && best.id != "" && cand.id != best.id:
			ambiguous = true
		case d == bestDist && cand.id == best.id && cand.preferredOver(best):
			best = cand
		}
	}
	if best.id == "" || ambiguous {
		return ""
	}
	if effort != "" {
		return best.name + ":" + effort
	}
	return best.name
}

func digitsOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

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
		if _, aliased := routingpolicy.ForceModelAliases[tail]; aliased {
			continue
		}
		out[tail] = ids[0]
	}
	return out
}()

const forceModelSessionRole = routingpolicy.ForceModelSessionRole

func (s *Service) loadForceModelHistory(
	ctx context.Context,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
) sessionpin.Pin {
	return routingpolicy.NewForceModelPins(s.pinStore, s.clock.Now).LoadHistory(ctx, sessionKey, role)
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
	ctx context.Context, strategy router.Strategy,
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
		if err := s.expireSessionPin(ctx, strategy, installationID, sessionKey, role, userUnforcedReason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyForceModelHeader(
	ctx context.Context,
	r *http.Request,
	installationID uuid.UUID,
	forceModelSessionKey [sessionpin.SessionKeyLen]byte,
) (context.Context, string, error) {
	raw := strings.TrimSpace(r.Header.Get(requestcontext.ForceModelHeader))
	if raw == "" {
		return ctx, "", nil
	}
	log := observability.FromContext(ctx)
	canonicalModel, provider, known, effortLevel := resolveForceModelWithEffort(raw)
	if effortLevel != "" {
		merged := router.Overrides{ForceEffort: effortLevel}
		if existing := router.RoutingKnobsFromContext(r.Context()); existing != nil {
			merged.Alpha = existing.Alpha
			merged.QualityBias = existing.QualityBias
			merged.SpeedWeight = existing.SpeedWeight
			merged.OutputCostRatio = existing.OutputCostRatio
			merged.ExpectedOutputTokens = existing.ExpectedOutputTokens
			merged.PerModelVerbosity = existing.PerModelVerbosity
		}
		ctx = router.WithRoutingKnobs(ctx, &merged)
		*r = *r.WithContext(router.WithRoutingKnobs(r.Context(), &merged))
	}
	if !known {
		log.Warn("x-weave-force-model: rejected unrecognized model",
			"input_model", raw,
			"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
		)
		return ctx, "", &ForcedModelUnknownError{Model: raw}
	}
	binding, refusal := s.forcedModelBinding(ctx, canonicalModel, provider)
	if refusal != nil {
		log.Warn("x-weave-force-model: rejected excluded model",
			"input_model", raw,
			"canonical_model", canonicalModel,
			"provider", provider,
			"reason", refusal.Reason,
		)
		return ctx, "", refusal
	}
	provider = binding
	forcedModel := canonicalModel
	if effortLevel != "" {
		forcedModel += ":" + effortLevel
	}
	if err := routingpolicy.NewForceModelPins(s.pinStore, s.clock.Now).Set(ctx, forceModelSessionKey, installationID, canonicalModel, provider, effortLevel, ""); err != nil {
		log.Error("x-weave-force-model: session pin upsert failed", "err", err)
		return ctx, forcedModel, nil
	}
	log.Info("x-weave-force-model applied",
		"input_model", raw,
		"canonical_model", canonicalModel,
		"provider", provider,
		"effort", effortLevel,
		"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
		"role", forceModelSessionRole,
	)
	return ctx, forcedModel, nil
}

type forceModelCommands struct {
	s *Service
}

func (f forceModelCommands) handle(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	cmd translate.ForceModelResult,
	target sessionTarget,
	forceModelSessionKey [sessionpin.SessionKeyLen]byte,
	inputTokens int,
) error {
	_, ack, err := f.apply(ctx, target.strategy, cmd, target.installationID, target.sessionKey, forceModelSessionKey)
	if err != nil {
		return err
	}
	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}

func (f forceModelCommands) apply(
	ctx context.Context, strategy router.Strategy,
	cmd translate.ForceModelResult,
	installationID uuid.UUID,
	threadSessionKey, forceModelSessionKey [sessionpin.SessionKeyLen]byte,
) (string, commandAcknowledgment, error) {
	ack := newCommandAcknowledgment(translate.CommandForceModel)
	applied, err := conversation.ForceModelCommand{
		Resolver: forceModelResolver(f),
		Pins:     routingpolicy.NewForceModelPins(f.s.pinStore, f.s.clock.Now),
		Controls: forceModelControls{s: f.s, strategy: strategy},
	}.Apply(ctx, conversation.ForceModelRequest{
		Command:              cmd,
		InstallationID:       installationID,
		ThreadSessionKey:     threadSessionKey,
		ForceModelSessionKey: forceModelSessionKey,
		CommandID:            ack.outcome.ID,
		UnforceCommand:       requestcontext.DirectivePrefix(requestcontext.ClientIdentityFrom(ctx).ClientApp) + "unforce-model",
	})
	if err != nil {
		return "", commandAcknowledgment{}, err
	}
	if applied.Rejection != "" {
		ack.outcome.Disposition = translate.CommandRejected
		ack.force = forceAcknowledgment{rejection: forceRejection(applied.Rejection), inputModel: applied.InputModel, suggestion: applied.Suggestion, reason: applied.Reason}
		return "", ack, nil
	}
	ack.outcome.Pin = applied.Pin
	ack.force.clearCommand = applied.ClearCommand
	ack.force.parentOnly = applied.ParentOnly
	return applied.ShownModel, ack, nil
}

type forceModelResolver struct {
	s *Service
}

func (forceModelResolver) Resolve(model string) (string, string, bool, string) {
	return resolveForceModelWithEffort(model)
}

func (forceModelResolver) Suggest(input string) string { return suggestForceModel(input) }

func (r forceModelResolver) Binding(ctx context.Context, model, provider string) (string, string, bool) {
	binding, refusal := r.s.forcedModelBinding(ctx, model, provider)
	if refusal != nil {
		return "", refusal.Reason, true
	}
	return binding, "", false
}

type forceModelControls struct {
	s        *Service
	strategy router.Strategy
}

func (c forceModelControls) ClearLegacyPins(ctx context.Context, installationID uuid.UUID, sessionKey [sessionpin.SessionKeyLen]byte) error {
	return c.s.clearLegacyForceModelPins(ctx, c.strategy, installationID, sessionKey)
}

func (c forceModelControls) RecordCommandBoundary(ctx context.Context, installationID uuid.UUID, threadKey [sessionpin.SessionKeyLen]byte, commandID string) {
	c.s.recordAgreementShadowCommandBoundary(ctx, installationID, threadKey, commandID)
}

func writeSyntheticAnthropicResponse(w http.ResponseWriter, env *translate.RequestEnvelope, text string, inputTokens int) error {
	return translate.WriteSyntheticAnthropicResponse(w, env, text, inputTokens)
}

func writeSyntheticOpenAIResponse(w http.ResponseWriter, env *translate.RequestEnvelope, text string, inputTokens int) error {
	return translate.WriteSyntheticOpenAIResponse(w, env, text, inputTokens)
}

func sseEvent(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

func mustMarshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
