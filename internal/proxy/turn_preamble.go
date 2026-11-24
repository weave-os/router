package proxy

import (
	"context"
	"fmt"
	"log/slog"

	"weave-os/router/internal/translate"
)

func stripInboundEchoes(ctx context.Context, log *slog.Logger, body []byte, protocol string) (context.Context, []byte, string, bool, error) {
	ctx = withSubscriptionOnlyWarningEcho(ctx, body)
	replayedModel := translate.LastAnnouncedMessagesModel(body)
	stripped, err := stripRoutingMarkerFromMessages(body)
	if err != nil {
		log.Error("Failed to strip routing marker from "+protocol+" messages", "err", err)
		return ctx, body, replayedModel, false, fmt.Errorf("strip routing marker: %w", err)
	}
	echoed, _ := ctx.Value(responsesFooterEchoedContextKey{}).(bool)
	return ctx, stripped, replayedModel, translate.FeedbackFooterInHistory(stripped) || echoed, nil
}
