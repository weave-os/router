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

func turnPromptText(feats translate.RoutingFeatures) (promptText, embedInput string) {
	if feats.OnlyUserMessageText != "" {
		return feats.OnlyUserMessageText, "only_user_message"
	}
	return feats.PromptText, "concatenated_stream"
}

func logTurnStart(log *slog.Logger, message string, stream bool, feats translate.RoutingFeatures, promptPreview string) {
	log.Info(message,
		"requested_model", feats.Model,
		"stream", stream,
		"message_count", feats.MessageCount,
		"has_tools", feats.HasTools,
		"total_input_tokens", feats.Tokens,
		"prompt_preview", promptPreview,
	)
}
