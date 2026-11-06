package proxy

import (
	"context"
	"fmt"
	"strings"
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
	env.StripRouterFeedbackArtifacts()
	env.StripBetaArtifacts()
	env.CoalesceDuplicateToolResults()
	env.SanitizeOrphanedToolCalls()
	return context.WithValue(ctx, responsesRoutingEnvelopeContextKey{}, env), nil
}

func routingEnvelopeForRequest(ctx context.Context, env *translate.RequestEnvelope) *translate.RequestEnvelope {
	if projected, ok := ctx.Value(responsesRoutingEnvelopeContextKey{}).(*translate.RequestEnvelope); ok {
		return projected
	}
	return env
}

func routingInputForRequest(ctx context.Context, env *translate.RequestEnvelope, promptText string, onlyUser bool) (string, []router.ConversationMessage) {
	routingEnv := routingEnvelopeForRequest(ctx, env)
	if routingEnv != env {
		features := routingEnv.RoutingFeatures(onlyUser)
		promptText = features.PromptText
		if onlyUser && features.OnlyUserMessageText != "" {
			promptText = features.OnlyUserMessageText
		}
	}
	clientApp := ClientIdentityFrom(ctx).ClientApp
	var projectedMessages []translate.ConversationMessage
	var projected bool
	if clientApp == ClientAppClaudeCode {
		projectedMessages, projected = routingEnv.ClaudeCodeRoutingMessages()
	} else {
		projectedMessages = routingEnv.ConversationMessages()
		if clientApp == ClientAppOpencode {
			projected = projectOpenCodeBackgroundCompletions(projectedMessages)
		}
	}
	if projected && onlyUser {
		var userText []string
		for _, message := range projectedMessages {
			if message.Role == string(translate.EscalationRoleUser) && message.Text != "" {
				userText = append(userText, message.Text)
			}
		}
		promptText = strings.Join(userText, "\n")
	}
	return promptText, routingConversationMessages(projectedMessages)
}

func routingEndsWithUserPrompt(ctx context.Context, env *translate.RequestEnvelope) bool {
	routingEnv := routingEnvelopeForRequest(ctx, env)
	if ClientIdentityFrom(ctx).ClientApp == ClientAppOpencode {
		messages := routingEnv.ConversationMessages()
		if projectOpenCodeBackgroundCompletions(messages) {
			for index := len(messages) - 1; index >= 0; index-- {
				last := messages[index]
				if last.Role == string(translate.EscalationRoleSystem) || last.Role == string(translate.EscalationRoleDeveloper) {
					continue
				}
				if _, completion := openCodeBackgroundCompletion(last.Text); last.Role == string(translate.EscalationRoleAssistant) && completion {
					return false
				}
				break
			}
		}
	}
	return routingEnv.EndsWithUserPrompt()
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
