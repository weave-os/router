package dispatch

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type RescueMarkers interface {
	SubscriptionTarget(ctx context.Context, picked, target router.Decision, pickedMarker string) string
	Sibling(ctx context.Context, failedModel, siblingModel string) string
	CyberRefusal(ctx context.Context, targetModel string) string
	EarlyStop(ctx context.Context, targetModel string) string
	Baseline(ctx context.Context, baselineModel string) string
	WithFailureDetail(ctx context.Context, marker string) string
	ClaudeNotice(ctx context.Context, model, marker string) translate.RouterNotice
	WithOwnClaudeRejectionNudge(ctx context.Context) context.Context
}
