package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"hash/maphash"
	"net/http"
	"strings"
	"sync"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/tidwall/gjson"
)

const subscriptionModelDenialTTL = 15 * time.Minute

type subscriptionModelKey struct {
	token    uint64
	owner    string
	account  string
	provider string
	model    string
}

type subscriptionModelAccess struct {
	once    sync.Once
	seed    maphash.Seed
	entries *lru.Cache[subscriptionModelKey, time.Time]
}

func (a *subscriptionModelAccess) cache() *lru.Cache[subscriptionModelKey, time.Time] {
	a.once.Do(func() {
		a.seed = maphash.MakeSeed()
		a.entries, _ = lru.New[subscriptionModelKey, time.Time](4096)
	})
	return a.entries
}

func (a *subscriptionModelAccess) key(token []byte, model string) subscriptionModelKey {
	a.cache()
	return subscriptionModelKey{token: maphash.Bytes(a.seed, token), model: router.StripDateSuffix(model)}
}

func (a *subscriptionModelAccess) managedKey(owner, account, provider, model string) subscriptionModelKey {
	return subscriptionModelKey{owner: owner, account: account, provider: provider, model: router.StripDateSuffix(model)}
}

func (a *subscriptionModelAccess) denied(token []byte, model string, now time.Time) bool {
	until, ok := a.cache().Get(a.key(token, model))
	return ok && now.Before(until)
}

func (a *subscriptionModelAccess) managedDenied(owner, account, provider, model string, now time.Time) bool {
	until, ok := a.cache().Get(a.managedKey(owner, account, provider, model))
	return ok && now.Before(until)
}

func (a *subscriptionModelAccess) denyManaged(owner, account, provider, model string, until time.Time) {
	a.cache().Add(a.managedKey(owner, account, provider, model), until)
}

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

func (s *Service) recordSubscriptionModelRejection(ctx context.Context, provider, model string, err error) {
	creds := CredentialsFromContext(ctx)
	if provider != providers.ProviderAnthropic || creds == nil || !creds.OAuth || len(creds.APIKey) == 0 || !anthropicSubscriptionModelRejected(err) {
		return
	}
	s.subscriptionModels.cache().Add(s.subscriptionModels.key(creds.APIKey, model), s.clockNow().Add(subscriptionModelDenialTTL))
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
	if provider != providers.ProviderAnthropic || creds == nil || !creds.OAuth ||
		billing.SubscriptionOnlyFromContext(ctx) || !s.anthropicFallbackKeyAvailable(ctx) ||
		!s.subscriptionModels.denied(creds.APIKey, model, s.clockNow()) {
		return resolved
	}
	previous, _ := ctx.Value(suppressClaudeModelContextKey{}).(map[string]struct{})
	models := make(map[string]struct{}, len(previous)+1)
	for id := range previous {
		models[id] = struct{}{}
	}
	models[router.StripDateSuffix(model)] = struct{}{}
	return resolveAndInjectCredentials(context.WithValue(ctx, suppressClaudeModelContextKey{}, models), provider, model, headers)
}

func (s *Service) excludeUnavailableSubscriptionModels(ctx context.Context, headers http.Header, enabled, excluded map[string]struct{}) map[string]struct{} {
	_, token := presentSubscriptionTokens(ctx, headers)
	if token == "" || (!billing.SubscriptionOnlyFromContext(ctx) && s.anthropicFallbackKeyAvailable(ctx)) {
		return excluded
	}
	for _, model := range catalog.Models {
		if !s.subscriptionModels.denied([]byte(token), model.ID, s.clockNow()) {
			continue
		}
		for _, binding := range model.Providers {
			if enabled != nil {
				if _, ok := enabled[binding.Provider]; !ok {
					continue
				}
			}
			if binding.Provider == providers.ProviderAnthropic {
				excluded = excludingModel(excluded, model.ID)
			}
			break
		}
	}
	return excluded
}
