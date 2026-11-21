package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/tidwall/gjson"
)

func anthropicSubscriptionModelRejected(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) || upstream.Status != http.StatusNotFound {
		return false
	}
	return gjson.GetBytes(upstream.Body, "error.type").String() == "not_found_error" &&
		strings.HasPrefix(strings.ToLower(gjson.GetBytes(upstream.Body, "error.message").String()), "model:")
}

func anthropicSubscriptionModelUnavailable(model string) error {
	body, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "not_found_error",
			"message": "model: " + router.StripDateSuffix(model),
		},
	})
	return &providers.UpstreamErrorResponse{Status: http.StatusNotFound, Body: body}
}

func recordSubscriptionModelOutcome(s *Service, ctx context.Context, provider, model string, err error) {
	if err == nil {
		s.credentials.SubscriptionModels.MarkServedForCaller(CredentialsFromContext(ctx), provider, model, s.clock.Now())
		return
	}
	s.recordSubscriptionModelRejection(ctx, provider, model, err)
}

func (s *Service) recordSubscriptionModelRejection(ctx context.Context, provider, model string, err error) {
	creds := CredentialsFromContext(ctx)
	if creds == nil || !creds.OAuth || len(creds.APIKey) == 0 {
		return
	}
	until := s.clock.Now().Add(credentials.SubscriptionModelDenialTTL)
	switch provider {
	case providers.ProviderAnthropic:
		if anthropicSubscriptionModelRejected(err) {
			s.credentials.SubscriptionModels.Deny(creds.APIKey, model, until)
		}
	case providers.ProviderOpenAI:
		if len(creds.AccountID) > 0 && codexSubscriptionModelRejected(err) {
			s.credentials.SubscriptionModels.DenyForProvider(creds.APIKey, provider, model, until)
		}
	}
}

type suppressClaudeModelContextKey struct{}

func claudeModelSuppressed(ctx context.Context, model string) bool {
	models, _ := ctx.Value(suppressClaudeModelContextKey{}).(map[string]struct{})
	_, suppressed := models[router.StripDateSuffix(model)]
	return suppressed
}

func (s *Service) resolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	resolved := resolveAndInjectCredentials(ctx, provider, model, headers)
	creds := CredentialsFromContext(resolved)
	if provider == providers.ProviderOpenAI && creds != nil && creds.OAuth && len(creds.AccountID) > 0 &&

		s.credentials.SubscriptionModels.DeniedForProvider(creds.APIKey, provider, model, s.clock.Now()) {
		return resolveAndInjectCredentials(withSuppressedCodexModel(resolved, model), provider, model, headers)
	}
	if provider != providers.ProviderAnthropic || creds == nil || !creds.OAuth ||
		!s.credentials.SubscriptionModels.Denied(creds.APIKey, model, s.clock.Now()) {
		return resolved
	}
	previous, _ := ctx.Value(suppressClaudeModelContextKey{}).(map[string]struct{})
	models := make(map[string]struct{}, len(previous))
	for id := range previous {
		models[id] = struct{}{}
	}
	models[router.StripDateSuffix(model)] = struct{}{}
	return resolveAndInjectCredentials(context.WithValue(ctx, suppressClaudeModelContextKey{}, models), provider, model, headers)
}
