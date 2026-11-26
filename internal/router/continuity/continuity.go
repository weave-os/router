package continuity

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/planner"
	"weave-os/router/internal/router/sessionpin"
)

type Turn struct {
	IsToolResult         bool
	Escalated            bool
	AuthoritativePerTurn bool
}

type Config struct {
	PlannerEnabled            bool
	ScoreToolResultTurns      bool
	HoldToolResult            bool
	SwitchDecideAuthoritative bool
	Planner                   planner.EVConfig
}

func ToolResultSticky(session router.SessionView, turn Turn, cfg Config) bool {
	return !turn.Escalated && !turn.AuthoritativePerTurn &&
		(!cfg.ScoreToolResultTurns || cfg.HoldToolResult) &&
		turn.IsToolResult && session.Pin.Found
}

func CanReusePin(session router.SessionView, turn Turn, cfg Config) bool {
	return !turn.Escalated && !turn.AuthoritativePerTurn && !cfg.PlannerEnabled && session.Pin.Found
}

type PlanInputs struct {
	EstimatedInputTokens  int
	CacheablePrefixTokens int
	CachePrefixKnown      bool
	PinCacheCold          bool
	AvailableModels       map[string]struct{}
}

func Plan(session router.SessionView, fresh router.Decision, in PlanInputs, cfg planner.EVConfig) planner.Decision {
	pin := sessionpin.Pin{}
	if session.Pin.Found {
		pin = sessionpin.Pin{
			Provider:        session.Pin.Provider,
			Model:           session.Pin.Model,
			LastTurnEndedAt: session.Pin.LastTurnEndedAt,
		}
	}
	return planner.Decide(planner.Inputs{
		Pin:                   pin,
		Fresh:                 fresh,
		EstimatedInputTokens:  in.EstimatedInputTokens,
		CacheablePrefixTokens: in.CacheablePrefixTokens,
		CachePrefixKnown:      in.CachePrefixKnown,
		PriorOutputTokens:     session.Pin.LastOutputTokens,
		AvailableModels:       in.AvailableModels,
		PinCacheCold:          session.Pin.Found && in.PinCacheCold,
	}, cfg)
}
