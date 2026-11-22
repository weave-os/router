package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type PostRouteChecks struct {
	AgentShadowMode bool
	Route           routingpolicy.TurnRoute
	Env             *translate.RequestEnvelope
	Features        translate.RoutingFeatures
	PromptText      string
	ToolCallCount   int
	ToolBearing     bool
	ClientBudget    router.ClientBudget
	Scope           router.Request

	LoopBreak             LoopBreak
	NoProgress            *NoProgressTracker
	Now                   func() time.Time
	TextRepetitionEnabled bool

	RecoverClientCompaction bool
	SummaryMaxTokens        int
	ClientDefaultWindow     int

	HandoverEligible   bool
	CompactionHandover func(context.Context) handover.Outcome
}

type PostRouteOutcome struct {
	LoopStopped        bool
	LoopStopMessage    string
	CompactionHandover handover.Outcome
}

func (c PostRouteChecks) Run(ctx context.Context) (PostRouteOutcome, error) {
	if c.AgentShadowMode {
		return PostRouteOutcome{}, nil
	}
	route, env, feats := c.Route, c.Env, c.Features
	if !route.CallerModelPassthrough && !route.AuthoritativePerTurn && c.ToolBearing && c.NoProgress != nil {
		fp := ComputeNoProgressFingerprint(route.Decision, c.PromptText, feats.MessageCount, ToolProgressMarker(env))
		if looped, count := c.NoProgress.RecordAndDetect(route.SessionKey, c.LoopBreak.InstallationID, c.LoopBreak.Role, fp, c.Now()); looped {
			return PostRouteOutcome{LoopStopped: true, LoopStopMessage: c.LoopBreak.NoProgress(ctx, env, count, route.Decision.Reason)}, nil
		}
	}

	if !route.CallerModelPassthrough && !route.AuthoritativePerTurn && c.TextRepetitionEnabled && (turntype.DetectFromEnvelope(env, feats, "") == turntype.MainLoop || turntype.DetectFromEnvelope(env, feats, "") == turntype.ToolResult) {
		if looped, count, sampleHash := DetectTextRepetition(env); looped {
			return PostRouteOutcome{LoopStopped: true, LoopStopMessage: c.LoopBreak.TextRepetition(ctx, env, count, sampleHash)}, nil
		}
	}

	if c.RecoverClientCompaction {
		if _, recoveryErr := ApplyClientCompactionRecovery(ctx, env, c.ClientBudget, route.TurnType, c.Scope.ConversationMessages, c.SummaryMaxTokens, c.ClientDefaultWindow); recoveryErr != nil {
			return PostRouteOutcome{}, recoveryErr
		}
	}

	var out PostRouteOutcome
	if c.HandoverEligible && !route.AuthoritativePerTurn && !route.HardPinned && !route.Handover.Invoked && route.PrefixTrimmed {
		observability.FromContext(ctx).Info("Context trimming detected on non-Anthropic route; rewriting context with handover summary",
			"message_count", feats.MessageCount,
			"tool_call_count", c.ToolCallCount,
			"decision_model", route.Decision.Model,
			"decision_provider", route.Decision.Provider,
		)
		out.CompactionHandover = c.CompactionHandover(ctx)
	}
	return out, nil
}
