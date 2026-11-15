package postgres

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
)

var _ conversation.LoopEscalationStore = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) InsertLoopEscalationEvent(ctx context.Context, p conversation.LoopEscalationEvent) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.InsertLoopEscalationEvent(ctx, sqlc.InsertLoopEscalationEventParams{
		InstallationID:   id,
		SessionKey:       p.SessionKey,
		Role:             p.Role,
		LoopingModel:     p.LoopingModel,
		Action:           p.Action,
		EscalationTarget: p.EscalationTarget,
		LoopTool:         p.LoopTool,
		LoopInputHash:    p.LoopInputHash,
		RepeatCount:      p.RepeatCount,
		DistinctRatio:    p.DistinctRatio,
		WindowSize:       p.WindowSize,
	})
}

func (r *TelemetryRepo) CountLoopEscalationEvents(ctx context.Context, sessionKey []byte, role string) (count int64, err error) {
	q := dbbudget.Queries(r.tx)
	return q.CountLoopEscalationEvents(ctx, sqlc.CountLoopEscalationEventsParams{
		SessionKey: sessionKey,
		Role:       role,
	})
}

var _ conversation.SpiralShadowStore = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) InsertSpiralShadowEvent(ctx context.Context, p conversation.SpiralShadowEvent) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.InsertSpiralShadowEvent(ctx, sqlc.InsertSpiralShadowEventParams{
		InstallationID:     id,
		SessionKey:         p.SessionKey,
		Role:               p.Role,
		RoutedModel:        p.RoutedModel,
		TurnType:           p.TurnType,
		Reason:             p.Reason,
		ErrStreak:          p.ErrStreak,
		ErroredResults:     p.ErroredResults,
		ToolResults:        p.ToolResults,
		MaxSameFileEdits:   p.MaxSameFileEdits,
		SameFilePathHash:   p.SameFilePathHash,
		RepeatFrac:         p.RepeatFrac,
		MonologueLen:       p.MonologueLen,
		ToolCallCount:      p.ToolCallCount,
		MessageCount:       p.MessageCount,
		PingPongLen:        p.PingPongLen,
		StepsSinceProgress: p.StepsSinceProgress,
	})
}

func (r *TelemetryRepo) CountSpiralShadowEvents(ctx context.Context, sessionKey []byte, role, reason string) (count int64, err error) {
	q := dbbudget.Queries(r.tx)
	return q.CountSpiralShadowEvents(ctx, sqlc.CountSpiralShadowEventsParams{
		SessionKey: sessionKey,
		Role:       role,
		Reason:     reason,
	})
}

var _ conversation.StruggleShadowStore = (*TelemetryRepo)(nil)

func (r *TelemetryRepo) InsertStruggleShadowEvent(ctx context.Context, p conversation.StruggleShadowEvent) error {
	id, err := uuid.Parse(p.InstallationID)
	if err != nil {
		return err
	}
	q := dbbudget.Queries(r.tx)
	return q.InsertStruggleShadowEvent(ctx, sqlc.InsertStruggleShadowEventParams{
		InstallationID:      id,
		SessionKey:          p.SessionKey,
		Role:                p.Role,
		RoutedModel:         p.RoutedModel,
		TurnType:            p.TurnType,
		Reason:              p.Reason,
		TurnCount:           p.TurnCount,
		WallSeconds:         p.WallSeconds,
		SessionEverSwitched: p.SessionEverSwitched,
		EstInputTokens:      p.EstInputTokens,
	})
}

func (r *TelemetryRepo) CountStruggleShadowEvents(ctx context.Context, sessionKey []byte, role, reason string) (count int64, err error) {
	q := dbbudget.Queries(r.tx)
	return q.CountStruggleShadowEvents(ctx, sqlc.CountStruggleShadowEventsParams{
		SessionKey: sessionKey,
		Role:       role,
		Reason:     reason,
	})
}
