package proxy

import (
	"context"
	"fmt"
	"unicode/utf8"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type responsesRoutingEnvelopeContextKey struct{}

func withResponsesRoutingEnvelope(ctx context.Context, body []byte) (context.Context, error) {
	env, err := translate.ParseOpenAI(body)
	if err != nil {
		return ctx, fmt.Errorf("parse Responses routing history: %w", err)
	}
	if err := env.StripCommandOutcomes(); err != nil {
		return ctx, fmt.Errorf("strip Responses routing outcomes: %w", err)
	}
	if _, err := env.StripHistoricalCommands(); err != nil {
		return ctx, fmt.Errorf("strip Responses routing directives: %w", err)
	}
	env.StripRouterFeedbackArtifacts()
	env.StripBetaArtifacts()
	return context.WithValue(ctx, responsesRoutingEnvelopeContextKey{}, env), nil
}

func routingEnvelopeForRequest(ctx context.Context, env *translate.RequestEnvelope) *translate.RequestEnvelope {
	if projected, ok := ctx.Value(responsesRoutingEnvelopeContextKey{}).(*translate.RequestEnvelope); ok {
		return projected
	}
	return env
}

func routingInputForRequest(ctx context.Context, env *translate.RequestEnvelope, promptText string) (string, []router.ConversationMessage) {
	routingEnv := routingEnvelopeForRequest(ctx, env)
	if routingEnv != env {
		features := routingEnv.RoutingFeatures(true)
		promptText = features.PromptText
		if features.OnlyUserMessageText != "" {
			promptText = features.OnlyUserMessageText
		}
	}
	promptText, messages := routingEnv.RoutingInput(routingHistoryProfile(ctx), promptText, true)
	return promptText, routingConversationMessages(messages)
}

func routingHistoryProfile(ctx context.Context) translate.RoutingHistoryProfile {
	switch ClientIdentityFrom(ctx).ClientApp {
	case ClientAppClaudeCode:
		return translate.RoutingHistoryClaudeCode
	case ClientAppOpencode:
		return translate.RoutingHistoryOpenCode
	default:
		return translate.RoutingHistoryDefault
	}
}

func routingEndsWithUserPrompt(ctx context.Context, env *translate.RequestEnvelope) bool {
	return routingEnvelopeForRequest(ctx, env).RoutingEndsWithUserPrompt(routingHistoryProfile(ctx))
}

func conversationMessagesForRouting(env *translate.RequestEnvelope) []router.ConversationMessage {
	if env == nil {
		return nil
	}
	return routingConversationMessages(env.ConversationMessages())
}

func routingConversationMessages(messages []translate.ConversationMessage) []router.ConversationMessage {
	out := make([]router.ConversationMessage, 0, len(messages))
	for _, msg := range messages {
		calls := make([]router.ConversationToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			calls = append(calls, router.ConversationToolCall{
				Name:      call.Name,
				InputKeys: append([]string(nil), call.InputKeys...),
				InputJSON: call.InputJSON,
			})
		}
		results := make([]router.ConversationToolResult, 0, len(msg.ToolResults))
		for _, result := range msg.ToolResults {
			exitCategory := "success"
			if result.IsError {
				exitCategory = "error"
			}
			results = append(results, router.ConversationToolResult{
				ToolUseID:     result.ToolUseID,
				IsError:       result.IsError,
				Text:          result.Text,
				ResultPresent: true,
				CharCount:     utf8.RuneCountInString(result.Text),
				ByteCount:     len(result.Text),
				ExitCategory:  exitCategory,
			})
		}
		out = append(out, router.ConversationMessage{
			Role:        msg.Role,
			Text:        msg.Text,
			ToolCalls:   calls,
			ToolResults: results,
		})
	}
	return out
}

func toolsForRouting(env *translate.RequestEnvelope) []router.ToolDescriptor {
	if env == nil {
		return nil
	}
	tools := env.ToolDescriptors()
	if len(tools) == 0 {
		return nil
	}
	return append([]router.ToolDescriptor(nil), tools...)
}

func availableToolsForRouting(env *translate.RequestEnvelope) []string {
	if env == nil {
		return nil
	}
	names := env.AvailableToolNames()
	if len(names) == 0 {
		return nil
	}
	return append([]string(nil), names...)
}
