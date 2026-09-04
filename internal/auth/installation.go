// Package auth provides installation/api-key types, repository interfaces, and a
// Service that authenticates incoming bearer tokens.
package auth

import (
	"context"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
)

type Installation struct {
	ID         string
	ExternalID string
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
	CreatedBy  *string
	// ExcludedModels is the per-installation model exclusion list.
	// Empty means no exclusion.
	ExcludedModels []string
	// AllowedModels is the org's positive model allowlist; when non-empty,
	// routing is confined to these models minus ExcludedModels.
	// Empty = no restriction; fail-closed when no eligible overlap remains.
	AllowedModels []string
	// ModelsWhenSubscriptionActive optionally confines routing while the caller's
	// subscription has headroom. Empty means no conditional restriction.
	ModelsWhenSubscriptionActive []string
	// ModelsWhenSubscriptionInactive optionally confines routing after the
	// caller's subscription is exhausted. Empty means no conditional restriction.
	ModelsWhenSubscriptionInactive []string
	// ExcludedProviders is the per-installation provider exclusion list.
	// Empty means no exclusion.
	ExcludedProviders []string
	// PreferredModels is the per-installation model priority ranking, in
	// descending preference (index 0 = first preference). The scorer lifts each
	// preferred model's score by a small, rank-decaying additive bonus so a
	// preferred model wins close calls without overriding a clearly-better
	// model. Empty means no preference.
	PreferredModels []string
	// FastModeModels lists catalog models every dispatch of which is sent on
	// the provider's fast tier and billed at the fast rate. Routing still scores
	// on list price. Empty means no model runs fast.
	FastModeModels []string
	// RoutingQualityWeight is the per-installation routing preference (the
	// "quality vs price" dial), stored as the scorer's quality weight (Alpha)
	// -- a normalized fraction in [0, 1] where 1.0 biases routing fully toward
	// quality and 0.0 fully toward price. The implied price weight is the
	// remainder. nil means no preference -- the scorer keeps its tuned
	// per-cluster defaults.
	RoutingQualityWeight *float64
	// UsageBypassEnabled toggles the per-installation subscription usage-bypass
	// gate. When true, requests presenting a subscription credential whose
	// observed rate-limit utilization is below UsageBypassThreshold pass
	// straight through to the requested model (no routing, no billing debit).
	// Defaults false -- strict opt-in.
	UsageBypassEnabled bool
	// UsageBypassThreshold is the [0, 1] utilization at/above which the bypass
	// gate disengages and normal routing takes over. nil means "use the
	// deployment default" so the toggle can be on before a value is chosen.
	UsageBypassThreshold *float64
	// SubscriptionRoutingDisabled turns off subscription-AWARE ROUTING for this
	// installation. When true, the scorer's subscription subsidy bonus is
	// suppressed, so routing decides purely on quality/cost/speed merits and
	// non-Claude models compete fairly instead of always losing to the
	// subsidized Claude family. It removes only the routing BIAS: a turn that
	// still routes to Claude on its own merits is dispatched on the caller's
	// subscription token exactly as before, so the prepaid billing path is
	// unchanged. Defaults false -- preserves today's behavior.
	SubscriptionRoutingDisabled bool
	// RoutingStrategy is the canonical strategy selected for this installation.
	// Existing installations default to the cluster scorer until allowlisted.
	RoutingStrategy router.Strategy
	// RoutingRolloutID correlates every route/outcome during a controlled rollout.
	RoutingRolloutID string
	// PolicyShadowStrategy optionally identifies a non-serving comparison policy.
	PolicyShadowStrategy router.Strategy
	// PolicyDebugEnabled enables detailed policy diagnostics for this installation.
	PolicyDebugEnabled bool
	// PolicyHeaderOverridesEnabled authorizes internal/eval request overrides.
	PolicyHeaderOverridesEnabled bool
	// PolicyRoutingIntent is a strategy-neutral preset such as low/medium/high.
	PolicyRoutingIntent string
	// AITrainingAllowed is the fail-closed privacy snapshot used by online learning.
	AITrainingAllowed bool
	// ByokEnabled opts a managed-mode installation into BYOK; managed deploys
	// are off by default (they bill via prepaid credits). Self-hosted ignores it.
	ByokEnabled bool
	// ContentCaptureMode caps content capture for this installation
	// ("off"|"hashed"|"full"); nil means no override; can only tighten
	// WV_CAPTURE_CONTENT.
	ContentCaptureMode *string
	// HideTerminalSurfaces suppresses the routing marker, feedback footer, and
	// statusline; routing and feedback recording are unaffected. Defaults false.
	HideTerminalSurfaces bool
	// FirstRequestServedAt is when this installation first routed a request.
	// Set once and never cleared so it survives key rotation.
	FirstRequestServedAt *time.Time
	// FlagOverrides is the sparse per-organization override set for behavioral
	// flags; zero value inherits every deployment default. Ignored when
	// ROUTER_FLAG_OVERRIDES_DISABLED is set.
	FlagOverrides flags.Overrides
}

type CreateInstallationParams struct {
	ExternalID string
	Name       string
	CreatedBy  *string
}

type InstallationRepository interface {
	Create(ctx context.Context, params CreateInstallationParams) (*Installation, error)
	// Get has no caller today; kept for a future admin detail view.
	Get(ctx context.Context, externalID, id string) (*Installation, error)
	ListForExternalID(ctx context.Context, externalID string) ([]*Installation, error)
	SoftDelete(ctx context.Context, externalID, id string) error
	// MarkFirstRequestServed stamps FirstRequestServedAt once; a no-op thereafter, so key rotation can't reset it.
	MarkFirstRequestServed(ctx context.Context, id string) error
	// UpdateFastModeModels replaces the per-installation fast-mode opt-in list.
	// An empty (or nil) slice clears the list.
	UpdateFastModeModels(ctx context.Context, externalID, id string, models []string) error
	// UpdateExcludedModels replaces the per-installation exclusion list.
	// An empty (or nil) slice clears the list.
	UpdateExcludedModels(ctx context.Context, externalID, id string, models []string) error
	// UpdateAllowedModels replaces the positive model allowlist.
	// Empty (or nil) means no restriction — NOT "no models routable".
	UpdateAllowedModels(ctx context.Context, externalID, id string, models []string) error
	// UpdateExcludedProviders replaces the per-installation provider
	// exclusion list. An empty (or nil) slice clears the list.
	UpdateExcludedProviders(ctx context.Context, externalID, id string, providerNames []string) error
	// UpdateRoutingPreference sets the routing quality weight (a normalized
	// fraction in [0, 1]). Passing nil clears the preference so the scorer
	// reverts to its tuned per-cluster defaults.
	UpdateRoutingPreference(ctx context.Context, externalID, id string, qualityWeight *float64) error
	// UpdateUsageBypass sets the subscription usage-bypass gate. enabled toggles
	// the gate; threshold is the [0, 1] utilization at/above which it disengages
	// (nil = use the deployment default).
	UpdateUsageBypass(ctx context.Context, externalID, id string, enabled bool, threshold *float64) error
	// UpdateSubscriptionRoutingDisabled toggles subscription-aware routing for
	// the installation. When true, the scorer's subscription subsidy bonus is
	// suppressed so routing decides on merits.
	UpdateSubscriptionRoutingDisabled(ctx context.Context, externalID, id string, disabled bool) error
	// UpdateContentCaptureMode sets the per-installation capture ceiling; nil
	// clears the override.
	UpdateContentCaptureMode(ctx context.Context, externalID, id string, mode *string) error
	// UpdateHideTerminalSurfaces toggles hiding the router's terminal surfaces
	// (routing marker, feedback footer, statusline) for the installation.
	UpdateHideTerminalSurfaces(ctx context.Context, externalID, id string, hide bool) error
	// UpdateFlagOverrides replaces the per-installation behavioral flag override
	// set. The whole sparse set is written, not a delta, so clearing one override
	// means omitting its key. An empty Overrides clears every override.
	UpdateFlagOverrides(ctx context.Context, externalID, id string, overrides flags.Overrides) error
}
