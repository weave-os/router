package continuity

import (
	"context"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
)

func (c Config) ResolvePlannerEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyPlannerEnabled, c.PlannerEnabled)
}

func (c Config) ResolveScoreToolResultTurns(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyScoreToolResultTurns, c.ScoreToolResultTurns)
}

func (p StrategyPorts) ResolveHMMToolResultHold(ctx context.Context, strategy router.Strategy) bool {
	return p.Ownership.Owns(strategy) && flags.BoolOr(ctx, flags.KeyHMMToolResultHold, false)
}

func (c AuthoritativeConfig) ResolveAuthoritativeUpgradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeUpgradeGate, c.UpgradeGate)
}

func (c AuthoritativeConfig) ResolveAuthoritativeUpgradePolicy(ctx context.Context) flags.AuthoritativeUpgradePolicy {
	defaultPolicy := c.UpgradePolicy
	if defaultPolicy == "" {
		defaultPolicy = flags.AuthoritativeUpgradePolicyScore
	}
	return flags.AuthoritativeUpgradePolicy(flags.StringOr(ctx, flags.KeyAuthoritativeUpgradePolicy, string(defaultPolicy)))
}

func (c AuthoritativeConfig) ResolveAuthoritativeUpgradeVotes(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyAuthoritativeUpgradeVotes, c.UpgradeVotes)
}

func (c AuthoritativeConfig) ResolveAuthoritativeDowngradeGate(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeDowngradeGate, c.DowngradeGate)
}

func (c AuthoritativeConfig) ResolveAuthoritativeUpgradeSlidingThreshold(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthoritativeUpgradeSlidingThreshold, c.UpgradeSlidingThreshold)
}

func (c AuthoritativeConfig) ResolveHMMDowngradeHysteresisTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisTurns, c.DowngradeHysteresisTurns)
}

func (c AuthoritativeConfig) ResolveHMMDowngradeHysteresisShadowTurns(ctx context.Context) int {
	return flags.IntOr(ctx, flags.KeyHMMDowngradeHysteresisShadowTurns, c.DowngradeHysteresisShadowTurns)
}

func (c AuthoritativeConfig) ResolveAuthorityCacheShadow(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyAuthorityCacheShadow, c.AuthorityCacheShadow)
}
