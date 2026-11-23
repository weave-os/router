package conversation

import (
	"log/slog"

	"weave-os/router/internal/translate"
)

func TurnPromptText(feats translate.RoutingFeatures) (promptText, embedInput string) {
	if feats.OnlyUserMessageText != "" {
		return feats.OnlyUserMessageText, "only_user_message"
	}
	return feats.PromptText, "concatenated_stream"
}

func LogTurnStart(log *slog.Logger, message string, stream bool, feats translate.RoutingFeatures, promptPreview string) {
	log.Info(message,
		"requested_model", feats.Model,
		"stream", stream,
		"message_count", feats.MessageCount,
		"has_tools", feats.HasTools,
		"total_input_tokens", feats.Tokens,
		"prompt_preview", promptPreview,
	)
}
