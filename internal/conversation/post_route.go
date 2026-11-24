package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/router"
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
}

type PostRouteOutcome struct {
	LoopStopped     bool
	LoopStopMessage string
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
	return PostRouteOutcome{}, nil
}
