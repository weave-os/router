package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability/otel"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"
)

const (
	legacyCacheProduct         = "legacy"
	cacheMissNoCompatibleEntry = "no_compatible_entry"
)

func (s *Service) semanticCacheProvenance(ctx context.Context, decision router.Decision) cache.Provenance {
	if _, known := catalog.ByID(decision.Model); !known {
		return cache.Provenance{}
	}
	scope := cache.ProvenanceScope{CredentialSubject: apiKeyIDFromContext(ctx), Product: legacyCacheProduct, Model: decision.Model, Provider: decision.Provider}
	if owner := subscriptionOwnerFromContext(ctx); owner.SubscriberID != "" {
		scope.CredentialSubject = owner.SubscriberID
	}
	if product, scoped := entitlement.ProductScopeFromContext(ctx); scoped {
		if product != entitlement.PlanBoost && product != entitlement.PlanMax {
			return cache.Provenance{}
		}
		scope.Product = string(product)
	}
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed {
		// A partial managed identity must never borrow a legacy key's scope.
		scope.CredentialSubject = identity.CredentialIdentity
		scope.Profile = identity.ProfileKey
		scope.ProfileRevision = identity.ProfileRevision
		scope.Release = identity.ReleaseID
		scope.Binding = identity.BindingID
		if identity.Plan != "" {
			if identity.Plan != string(entitlement.PlanBoost) && identity.Plan != string(entitlement.PlanMax) {
				return cache.Provenance{}
			}
			if scope.Product != legacyCacheProduct && scope.Product != identity.Plan {
				return cache.Provenance{}
			}
			scope.Product = identity.Plan
		}
	}
	scope.UpstreamScope = s.reasoningReplayScope(ctx, decision)
	return cache.NewProvenance(scope)
}

func semanticCacheRequestAllowed(ctx context.Context, req router.Request) bool {
	if creds := CredentialsFromContext(ctx); creds != nil && creds.OAuth {
		return false
	}
	if enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{}); len(enrolled) > 0 {
		return false
	}
	requirements := req.TranslationRequirements
	return len(req.AllowedModels) == 0 && len(req.ExcludedModels) == 0 && len(req.AutomaticExcludedModels) == 0 &&
		!req.HasTools && !req.HasImages && !requirements.FunctionTools && !requirements.CustomTools &&
		!requirements.ReasoningReplay && !requirements.ReasoningSignature && !requirements.Images && !requirements.Audio &&
		!requirements.Files && !requirements.CitationsOrSearch && !requirements.StructuredOutput && !requirements.NativeOnly
}

func (s *Service) semanticCacheStoreAllowed(ctx context.Context, provenance cache.Provenance, decision router.Decision, finalProvider string) bool {
	if decision.Model != provenance.Model() || finalProvider != provenance.Provider() {
		return false
	}
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
		if usage.SubscriptionAttempted || usage.Served {
			return false
		}
		if usage.Finished {
			ctx = requestcontext.WithCredentials(ctx, usage.WinningCredentials)
		}
	}
	return provenance.Valid() && s.semanticCacheProvenance(ctx, decision) == provenance
}

// recordSemanticCacheMiss exports aggregate activity without replay identities.
func (s *Service) recordSemanticCacheMiss(ctx context.Context) {
	stats := s.semanticCache.Stats()
	now := time.Now()
	otel.Record(ctx, otel.Span{
		Name: "router.cache_miss", Start: now, End: now,
		Attrs: otel.NewAttrBuilder(6).
			Bool("cache.hit", false).
			String("cache.reason", cacheMissNoCompatibleEntry).
			Int64("cache.hits", int64(stats.Hits)).
			Int64("cache.misses", int64(stats.Misses)).
			Int64("cache.bucket_evictions", int64(stats.BucketEvictions)).
			Int64("cache.installation_evictions", int64(stats.InstallationEvictions)).Build(),
	})
}
