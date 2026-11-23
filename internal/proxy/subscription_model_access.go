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
		s.credentials.SubscriptionModels.MarkServedForCaller(credentials.CredentialsFromContext(ctx), provider, model, s.clock.Now())
		return
	}
	s.recordSubscriptionModelRejection(ctx, provider, model, err)
}

func (s *Service) recordSubscriptionModelRejection(ctx context.Context, provider, model string, err error) {
	creds := credentials.CredentialsFromContext(ctx)
	if creds == nil || !creds.OAuth || len(creds.APIKey) == 0 {
		return
	}
	until := s.clock.Now().Add(credentials.SubscriptionModelDenialTTL)
	switch provider {
	case providers.ProviderAnthropic:
		if anthropicSubscriptionModelRejected(err) {
			s.credentials.SubscriptionModels.DenyForProvider(creds.APIKey, "", model, until)
		}
	case providers.ProviderOpenAI:
		if len(creds.AccountID) > 0 && codexSubscriptionModelRejected(err) {
			s.credentials.SubscriptionModels.DenyForProvider(creds.APIKey, provider, model, until)
		}
	}
}

func (s *Service) resolveCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	resolved := credentials.ResolveAndInjectCredentials(ctx, provider, model, headers)
	creds := credentials.CredentialsFromContext(resolved)
	if provider == providers.ProviderOpenAI && creds != nil && creds.OAuth && len(creds.AccountID) > 0 &&

		s.credentials.SubscriptionModels.DeniedForProvider(creds.APIKey, provider, model, s.clock.Now()) {
		return credentials.ResolveAndInjectCredentials(credentials.WithSuppressedCodexModel(resolved, model), provider, model, headers)
	}
	if provider != providers.ProviderAnthropic || creds == nil || !creds.OAuth ||
		!s.credentials.SubscriptionModels.Denied(creds.APIKey, model, s.clock.Now()) {
		return resolved
	}
	previous, _ := ctx.Value(credentials.SuppressClaudeModelContextKey{}).(map[string]struct{})
	models := make(map[string]struct{}, len(previous))
	for id := range previous {
		models[id] = struct{}{}
	}
	models[router.StripDateSuffix(model)] = struct{}{}
	return credentials.ResolveAndInjectCredentials(context.WithValue(ctx, credentials.SuppressClaudeModelContextKey{}, models), provider, model, headers)
}
