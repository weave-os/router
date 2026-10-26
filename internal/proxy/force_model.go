package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

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
	// Opus family aliases follow 5.5; Opus 5's own-name aliases keep
	// resolving so existing direct pins still serve.
	"anthropic":   "claude-opus-5-5",
	"claude":      "claude-opus-5-5",
	"opus":        "claude-opus-5-5",
	"claude-opus": "claude-opus-5-5",
	"opus-5.5":    "claude-opus-5-5",
	"opus-5-5":    "claude-opus-5-5",
	"opus5.5":     "claude-opus-5-5",
	"opus55":      "claude-opus-5-5",
	"claude-5.5":  "claude-opus-5-5",
	"claude-5-5":  "claude-opus-5-5",
	"opus-5":      "claude-opus-5",
	"opus-5.0":    "claude-opus-5",
	"opus5":       "claude-opus-5",
	"claude-5":    "claude-opus-5",
	// Opus 4.8 is retired from routing but still servable as passthrough;
	// keep its own-name aliases so direct pins resolve.
	"opus-4-8":   "claude-opus-4-8",
	"opus-4.8":   "claude-opus-4-8",
	"claude-4-8": "claude-opus-4-8",
	"claude-4.8": "claude-opus-4-8",
	// Fable family aliases follow 5.1; Fable 5's own-name aliases keep
	// resolving so existing direct pins still serve.
	"fable":        "claude-fable-5-1",
	"claude-fable": "claude-fable-5-1",
	"fable-5.1":    "claude-fable-5-1",
	"fable-5-1":    "claude-fable-5-1",
	"fable5.1":     "claude-fable-5-1",
	"fable51":      "claude-fable-5-1",
	"fable-5":      "claude-fable-5",
	"fable5":       "claude-fable-5",
	// Sonnet family aliases follow 5.5; Sonnet 5's own-name aliases keep
	// resolving so existing direct pins still serve.
	"sonnet":        "claude-sonnet-5-5",
	"claude-sonnet": "claude-sonnet-5-5",
	"sonnet-5.5":    "claude-sonnet-5-5",
	"sonnet-5-5":    "claude-sonnet-5-5",
	"sonnet5.5":     "claude-sonnet-5-5",
	"sonnet55":      "claude-sonnet-5-5",
	"sonnet-5":      "claude-sonnet-5",
	"sonnet-4-6":    "claude-sonnet-4-6",
	"sonnet-4.6":    "claude-sonnet-4-6",
	"haiku":         "claude-haiku-5-5",
	"claude-haiku":  "claude-haiku-5-5",
	"haiku-5.5":     "claude-haiku-5-5",
	"haiku-5-5":     "claude-haiku-5-5",
	"haiku5.5":      "claude-haiku-5-5",
	"haiku55":       "claude-haiku-5-5",
	"haiku-4-5":     "claude-haiku-4-5",
	"haiku-4.5":     "claude-haiku-4-5",
	// Generic GPT aliases follow the current flagship, not a pinned version;
	// they pointed at gpt-5.5 until it was retired. Version-specific aliases
	// (gpt-5-5*) deliberately still resolve to their exact model, which stays
	// available as priced passthrough.
	"gpt":        "gpt-6.1-sol",
	"openai":     "gpt-6.1-sol",
	"gpt-6":      "gpt-6-astra",
	"gpt6":       "gpt-6-astra",
	"gpt-6astra": "gpt-6-astra",
	"astra":      "gpt-6-astra",
	// Bare sol/luna follow the GPT-6 generation; gpt-5-6-* keeps the 5.6 rows.
	"sol":         "gpt-6.1-sol",
	"gpt-6-1-sol": "gpt-6.1-sol",
	"gpt6sol":     "gpt-6-sol",
	"gpt-6sol":    "gpt-6-sol",
	"luna":        "gpt-6-luna",
	"gpt6luna":    "gpt-6-luna",
	"gpt-6luna":   "gpt-6-luna",
	// The bare gpt-5.6 alias routes to Sol, matching OpenAI's own alias.
	"gpt-5.6":       "gpt-5.6-sol",
	"gpt-5-6":       "gpt-5.6-sol",
	"gpt-5-6-sol":   "gpt-5.6-sol",
	"terra":         "gpt-5.6-terra",
	"gpt-5-6-terra": "gpt-5.6-terra",
	"gpt-5-6-luna":  "gpt-5.6-luna",
	"gpt-5-5":       "gpt-5.5",
	"gpt-5-5-pro":   "gpt-5.5-pro",
	"gpt-5-5-mini":  "gpt-5.5-mini",
	"gpt-5-5-nano":  "gpt-5.5-nano",
	// grok-4.5 is retired from routing (no AA Agentic Index score, never rostered).
	// Family aliases follow flagship 4.7; own-name aliases keep older models as passthrough.
	"grok":                  "grok-4.7",
	"grok-4.5":              "grok-4.5",
	"grok4.5":               "grok-4.5",
	"xai":                   "grok-4.7",
	"grok-4.6":              "grok-4.6",
	"grok4.6":               "grok-4.6",
	"grok-4.7":              "grok-4.7",
	"grok4.7":               "grok-4.7",
	"grok-max":              "grok-4.7",
	"muse-spark-1.3":        "muse-spark-1.3",
	"muse-spark-1-3":        "muse-spark-1.3",
	"musespark-1.3":         "muse-spark-1.3",
	"musespark":             "muse-spark-1.3",
	"muse-spark":            "muse-spark-1.3",
	"muse":                  "muse-spark-1.3",
	"meta":                  "muse-spark-1.3",
	"gpt-5-4":               "gpt-5.4",
	"gpt-5-4-pro":           "gpt-5.4-pro",
	"gpt-5-4-mini":          "gpt-5.4-mini",
	"gpt-5-4-nano":          "gpt-5.4-nano",
	"google":                "gemini-3.1-pro-preview",
	"gemini":                "gemini-3.1-pro-preview",
	"gemini-pro":            "gemini-3.1-pro-preview",
	"gemini-flash":          "gemini-3.8-flash",
	"gemini-3-6-flash":      "gemini-3.6-flash",
	"gemini-3-5-flash-lite": "gemini-3.5-flash-lite",
	"gemini-3-7-flash":      "gemini-3.7-flash",
	"gemini-3-8-flash":      "gemini-3.8-flash",
	// V4 Flash is retired on Makora. Explicit version pins stay versioned.
	"deepseek":            "deepseek/deepseek-v4.1-flash",
	"deepseek-pro":        "deepseek/deepseek-v4-pro-0813",
	"deepseek-flash":      "deepseek/deepseek-v4.1-flash",
	"deepseek-v4-1-flash": "deepseek/deepseek-v4.1-flash",
	"deepseek-v4p1-flash": "deepseek/deepseek-v4.1-flash",
	// Generic Qwen follows the current flagship; coder remains a specialized alias.
	"qwen":       "qwen/qwen3.8-max",
	"qwen-coder": "qwen/qwen3-coder",
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
	// Generic Kimi aliases follow the newest managed-servable release; version-specific pins stay exact.
	"kimi":                     "moonshotai/kimi-k3",
	"kimi-k3.1":                "moonshotai/kimi-k3.1",
	"kimi-k3.1-pro":            "moonshotai/kimi-k3.1",
	"moonshotai/kimi-k3.1-pro": "moonshotai/kimi-k3.1",
	"kimi-k3":                  "moonshotai/kimi-k3",
	"kimi-k2.7":                "moonshotai/kimi-k2.7",
	"kimi-k2.6":                "moonshotai/kimi-k2.6",
	// Generic GLM aliases follow 5.3; version-specific pins stay exact.
	"glm":           "z-ai/glm-5.3",
	"zai":           "z-ai/glm-5.3",
	"z-ai":          "z-ai/glm-5.3",
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

// forceModelExamples follows the family aliases so the unknown-model hint
// never names a retired release.
var forceModelExamples = fmt.Sprintf("opus, %s, %s, or %s",
	forceModelAliases["opus"], forceModelAliases["gpt"], forceModelAliases["gemini"])

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
// Matching is exact up to separator spelling (see foldModelSpelling): no
// prefix, substring, or nearest-match fallback. Approximate matching silently
// served the wrong model; an unrecognized name must fail loudly instead.
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
		if m, ok := servableForceModel(id, requiredProvider); ok {
			return m.ID, m.Providers[0].Provider, true, effort
		}
	} else if effort == "" {
		if head, level, ok := cutSpelledEffort(model); ok {
			head, _ = translate.CanonicalModel(head)
			if id, ok := lookupForceModelName(head); ok {
				if m, ok := servableForceModel(id, requiredProvider); ok {
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

func servableForceModel(id, requiredProvider string) (catalog.Model, bool) {
	m, ok := catalog.ByID(id)
	if !ok || len(m.Providers) == 0 || (requiredProvider != "" && m.Providers[0].Provider != requiredProvider) {
		return catalog.Model{}, false
	}
	return m, true
}

// lookupForceModelName resolves an exact alias, bare catalog name, or catalog
// ID, then the same names with separators folded. Folding never guesses: it
// only reaches keys that differ from a real name in separator spelling.
func lookupForceModelName(name string) (string, bool) {
	if alias, ok := forceModelAliases[name]; ok {
		return alias, true
	}
	if canonical, ok := bareCatalogNames[name]; ok {
		return canonical, true
	}
	if _, ok := catalog.ByID(name); ok {
		return name, true
	}
	id, ok := foldedForceModelNames[foldModelSpelling(name)]
	return id, ok
}

// foldModelSpelling treats every run of '.', '_', ' ' and '-' as one '-', so
// "claude-opus-5.5", "opus 5.5" and "opus_5_5" spell the same name. Edge
// separators are kept, so a truncated "gpt-" never folds onto "gpt".
var modelSpellingSeparators = regexp.MustCompile(`[._ -]+`)

func foldModelSpelling(name string) string {
	return modelSpellingSeparators.ReplaceAllString(name, "-")
}

// cutSpelledEffort splits an effort level typed without the colon
// ("astra high", "gpt-6-sol-max") off the end of name.
func cutSpelledEffort(name string) (head, level string, ok bool) {
	folded := foldModelSpelling(name)
	idx := strings.LastIndexByte(folded, '-')
	if idx <= 0 || !looksLikeEffortAlias(folded[idx+1:]) {
		return "", "", false
	}
	return folded[:idx], folded[idx+1:], true
}

// forceModelNames lists every exact name /force-model accepts, keyed to its
// catalog ID: servable catalog IDs, Claude IDs without "claude-", aliases,
// and bare catalog names.
func forceModelNames() map[string]string {
	names := make(map[string]string, len(catalog.Models)*2+len(forceModelAliases))
	for _, m := range catalog.Models {
		if len(m.Providers) == 0 {
			continue
		}
		names[m.ID] = m.ID
		if unprefixed, ok := strings.CutPrefix(m.ID, "claude-"); ok {
			names[unprefixed] = m.ID
		}
	}
	for bare, id := range bareCatalogNames {
		names[bare] = id
	}
	for alias, id := range forceModelAliases {
		names[alias] = id
	}
	return names
}

// foldedForceModelNames indexes forceModelNames by folded spelling. A folded
// key that two names share for different models is dropped rather than
// guessed; TestFoldedForceModelNames_Unambiguous asserts the invariant.
var foldedForceModelNames = func() map[string]string {
	out := make(map[string]string)
	ambiguous := make(map[string]bool)
	for name, id := range forceModelNames() {
		key := foldModelSpelling(name)
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

// preferredOver orders display names: shortest, then the catalog ID's own
// spelling, then lexical, so the choice is deterministic.
func (c forceModelSuggestion) preferredOver(o forceModelSuggestion) bool {
	if len(c.name) != len(o.name) {
		return len(c.name) < len(o.name)
	}
	if (c.name == c.id) != (o.name == o.id) {
		return c.name == c.id
	}
	return c.name < o.name
}

// forceModelSuggestions holds one display name per unambiguous folded key:
// the shortest accepted name, so "opsu" suggests "opus", not a full ID.
var forceModelSuggestions = func() map[string]forceModelSuggestion {
	out := make(map[string]forceModelSuggestion, len(foldedForceModelNames))
	for name := range forceModelNames() {
		key := foldModelSpelling(name)
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

// suggestForceModel names the accepted spelling nearest an unrecognized input
// for a "did you mean" hint. It is display-only and never feeds resolution.
// Candidates must keep the input's digits: a near-miss version ("gpt-6-1" vs
// "gpt-4.1") is a different model, not a typo. It returns "" when nothing is
// within a small edit distance or the nearest names belong to different models.
func suggestForceModel(input string) string {
	effort, model := stripEffortSuffix(input)
	model, _ = translate.CanonicalModel(strings.ToLower(strings.TrimSpace(model)))
	key := foldModelSpelling(model)
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

// editDistance is the optimal-string-alignment distance: insertions,
// deletions, substitutions, and adjacent transpositions ("astar" -> "astra").
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
	return router.CanonicalizeEffort(tail), model[:idx]
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
	canonicalModel, provider, effort, forcedThreadKey string,
) error {
	if s.pinStore == nil || installationID == uuid.Nil {
		return nil
	}
	if err := s.preserveForceModelControlHistory(ctx, sessionKey, canonicalModel); err != nil {
		return fmt.Errorf("preserve force-model control history: %w", err)
	}
	forced := sessionpin.Pin{
		SessionKey:      sessionKey,
		Role:            forceModelSessionRole,
		InstallationID:  installationID,
		Provider:        provider,
		Model:           canonicalModel,
		Effort:          effort,
		ForcedThreadKey: forcedThreadKey,
		Reason:          translate.ReasonUserForceModel,
		TurnCount:       1,
		PinnedUntil:     pinNeverExpires,
	}
	return s.pinStore.Upsert(context.Background(), forced)
}

func (s *Service) loadForceModelSessionPin(
	ctx context.Context,
	sessionKey, threadSessionKey [sessionpin.SessionKeyLen]byte,
	isSubagent bool,
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
	if pin.ForcedThreadKey != "" && (isSubagent || pin.ForcedThreadKey != sessionKeyHex(threadSessionKey)) {
		// Suppress pre-session-scope force rows on children as an explicit clear
		// does, so switching from force-model cannot revive an inherited pin.
		return pin, false, true
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
// A `:level` suffix is stashed on the returned context (and on *r) as
// router.Overrides.ForceEffort. The returned model spec also keeps the suffix
// so plan-owned callers that omit routing knobs carry effort into same-turn
// req.ForceModel; callers must continue with the returned context for
// routingKnobsForRequest to see it.
func (s *Service) applyForceModelHeader(
	ctx context.Context,
	r *http.Request,
	installationID uuid.UUID,
	forceModelSessionKey [sessionpin.SessionKeyLen]byte,
) (context.Context, string, error) {
	raw := strings.TrimSpace(r.Header.Get(ForceModelHeader))
	if raw == "" {
		return ctx, "", nil
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
	binding, reason := s.forcedModelBinding(ctx, canonicalModel, provider)
	if reason != "" {
		log.Warn("x-weave-force-model: rejected excluded model",
			"input_model", raw,
			"canonical_model", canonicalModel,
			"provider", provider,
			"reason", reason,
		)
		return ctx, "", &ForcedModelExcludedError{Model: canonicalModel, Reason: reason}
	}
	provider = binding
	forcedModel := canonicalModel
	if effortLevel != "" {
		// The caller feeds this value into req.ForceModel for the same turn.
		// Keep the effort suffix there because plan-owned routing deliberately
		// omits routing knobs from the request passed to the scorer.
		forcedModel += ":" + effortLevel
	}
	if err := s.setForceModelSessionPin(ctx, forceModelSessionKey, installationID, canonicalModel, provider, effortLevel, ""); err != nil {
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
	_, ack, err := s.applyForceModelCommand(ctx, cmd, installationID, threadSessionKey, forceModelSessionKey)
	if err != nil {
		return err
	}
	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}

// applyForceModelCommand updates the session pin without deciding whether the
// caller should receive a synthetic response. It returns the canonical model
// (with its `:level` suffix when one was given, so a same-turn dispatch via
// req.ForceModel resolves the same effort) when a force was applied.
func (s *Service) applyForceModelCommand(
	ctx context.Context,
	cmd translate.ForceModelResult,
	installationID uuid.UUID,
	threadSessionKey, forceModelSessionKey [sessionpin.SessionKeyLen]byte,
) (string, commandAcknowledgment, error) {
	log := observability.FromContext(ctx)

	ack := newCommandAcknowledgment(translate.CommandForceModel)
	if cmd.Clear {
		if err := s.clearLegacyForceModelPins(ctx, installationID, threadSessionKey); err != nil {
			log.Error("/unforce-model: legacy pin cleanup failed", "err", err)
			return "", commandAcknowledgment{}, err
		}
		if err := s.clearForceModelSessionPin(ctx, installationID, forceModelSessionKey); err != nil {
			log.Error("/unforce-model: session pin clear failed", "err", err)
			return "", commandAcknowledgment{}, err
		}
		ack.outcome.Pin.Transition = translate.PinCleared
		log.Debug("/unforce-model: session pin cleared",
			"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
			"role", forceModelSessionRole,
		)
		return "", ack, nil
	}

	canonicalModel, provider, known, effortLevel := resolveForceModelWithEffort(cmd.Model)
	if !known {
		suggestion := suggestForceModel(cmd.Model)
		log.Info("/force-model: rejected unknown model",
			"input_model", cmd.Model,
			"suggested_model", suggestion,
			"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
			"role", forceModelSessionRole,
		)
		ack.outcome.Disposition = translate.CommandRejected
		ack.force = forceAcknowledgment{rejection: forceUnknownModel, inputModel: cmd.Model, suggestion: suggestion}
		return "", ack, nil
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
		ack.outcome.Disposition = translate.CommandRejected
		ack.force = forceAcknowledgment{rejection: forceExcludedModel, reason: reason}
		return "", ack, nil
	}

	if err := s.clearLegacyForceModelPins(ctx, installationID, threadSessionKey); err != nil {
		log.Error("/force-model: legacy pin cleanup failed", "err", err)
		return "", commandAcknowledgment{}, err
	}
	forcedThreadKey := ""
	command := "force-model"
	if cmd.ParentOnly {
		forcedThreadKey = sessionKeyHex(threadSessionKey)
		command = "force-parent"
	}
	if err := s.setForceModelSessionPin(ctx, forceModelSessionKey, installationID, canonicalModel, binding, effortLevel, forcedThreadKey); err != nil {
		log.Error("/force-model: session pin upsert failed", "err", err)
		return "", commandAcknowledgment{}, err
	}
	shownModel := canonicalModel
	if effortLevel != "" {
		shownModel = canonicalModel + ":" + effortLevel
	}
	ack.outcome.Pin = translate.CommandPin{Transition: translate.PinApplied, Model: canonicalModel, Provider: binding, Effort: effortLevel}
	ack.force.clearCommand = directivePrefix(ClientIdentityFrom(ctx).ClientApp) + "unforce-model"
	ack.force.parentOnly = cmd.ParentOnly
	log.Debug("/"+command+": session pin set",
		"forced_thread_key", forcedThreadKey,
		"input_model", cmd.Model,
		"canonical_model", canonicalModel,
		"provider", binding,
		"effort", effortLevel,
		"force_model_session_key_hex", fmt.Sprintf("%x", forceModelSessionKey),
		"role", forceModelSessionRole,
	)
	return shownModel, ack, nil
}

// writeSyntheticAnthropicResponse writes a minimal Anthropic Messages API
// response without hitting an upstream, handling both streaming and
// non-streaming shapes.
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
