package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
)

const (
	ctxKeyInstallation   = "router_installation"
	ctxKeyAPIKey         = "router_api_key"
	ctxKeyAdminPrincipal = "router_admin_principal"
)

// RouterKeyHeader carries the Weave Router key when clients need to preserve Authorization / x-api-key for the upstream provider.
const RouterKeyHeader = "X-Weave-Router-Key"

// AnthropicSubscriptionHeader carries a caller's Claude subscription OAuth
// token (sk-ant-oat-) alongside an rk_ router key, so the proxy can bill
// Claude-model turns to the caller's subscription instead of the deployment key.
const AnthropicSubscriptionHeader = "X-Weave-Anthropic-Subscription"

// OpenAISubscriptionHeader/OpenAIAccountIDHeader carry a caller's Codex
// (ChatGPT) OAuth JWT and its paired ChatGPT-Account-ID alongside an rk_
// router key, so Codex turns bill to the caller's ChatGPT plan. Both are
// required — the Codex backend 401/403s on a token without its account id.
const (
	OpenAISubscriptionHeader = "X-Weave-OpenAI-Subscription"
	OpenAIAccountIDHeader    = "X-Weave-OpenAI-Account-ID"
)

// WithAuth validates the inbound request via a bearer rk_ token only. Used on data-plane routes (`/v1/*`). On failure, short-circuits 401.
//
// byokRequiresOptIn gates BYOK keys behind the installation's own opt-in.
// Managed-mode deployments pass true; self-hosted always passes false.
func WithAuth(svc *auth.Service, byokRequiresOptIn bool) gin.HandlerFunc {
	return withAPIKey(svc, byokRequiresOptIn)
}

// WithAdminOrAuth accepts either a signed admin session cookie OR a bearer rk_ token.
// Don't use on `/v1/*` data-plane routes (a cookie shouldn't call provider proxy endpoints)
// or on control-plane mutations (a leaked rk_ shouldn't mint keys or rotate credentials —
// use WithAdminOnly there). See WithAuth for byokRequiresOptIn.
func WithAdminOrAuth(svc *auth.Service, byokRequiresOptIn bool) gin.HandlerFunc {
	apiKeyMW := withAPIKey(svc, byokRequiresOptIn)
	return func(c *gin.Context) {
		if principal := tryAdminCookie(c, svc); principal != nil {
			c.Set(ctxKeyAdminPrincipal, principal)
			c.Next()
			return
		}
		apiKeyMW(c)
	}
}

// WithAdminOnly requires a valid admin session cookie; bearer rk_ tokens are rejected so a leaked installation API key can't mint credentials or rotate provider keys.
func WithAdminOnly(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !svc.AdminLoginEnabled() {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "admin_login_disabled"})
			return
		}
		principal := tryAdminCookie(c, svc)
		if principal == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "admin_session_required"})
			return
		}
		c.Set(ctxKeyAdminPrincipal, principal)
		c.Next()
	}
}

// withAPIKey is the bearer-only auth path shared by WithAuth and the fall-through branch of WithAdminOrAuth.
//
// When byokRequiresOptIn is true, BYOK rows from svc.VerifyAPIKey are dropped
// unless the installation set ByokEnabled. Every downstream BYOK consumer
// (credential resolution, provider gating, usage bookkeeping) reads that
// single ctx key, so gating it here decides the whole path in one place.
func withAPIKey(svc *auth.Service, byokRequiresOptIn bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		parentCtx := c.Request.Context()
		clientSessionID := proxy.ClientIdentityFromHeaders(c.Request.Header).SessionID
		authCtx, authSpan := startAuthSpan(parentCtx, clientSessionID)
		token := extractToken(c)
		installation, apiKey, externalKeys, clusterModelLists, err := svc.VerifyAPIKey(authCtx, token)
		if err != nil {
			finishAuthSpan(authSpan, err)
			handleAuthError(c, err)
			return
		}
		c.Set(ctxKeyInstallation, installation)
		c.Set(ctxKeyAPIKey, apiKey)
		ctx := authCtx
		if apiKey != nil {
			ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, apiKey.ID)
			ctx = proxy.WithManagedSubscriptionUsage(ctx)
			if svc.SubscriptionAccountsEnabled() {
				accounts, listErr := svc.ListSubscriptionAccounts(ctx, apiKey.ID)
				if listErr != nil {
					observability.FromContext(ctx).Error("Failed to load subscription account enrollment", "err", listErr)
					ctx = context.WithValue(ctx, proxy.ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
				} else {
					enrolled := make(map[auth.SubscriptionProvider]struct{})
					for _, account := range accounts {
						enrolled[account.Provider] = struct{}{}
					}
					if len(enrolled) > 0 {
						ctx = context.WithValue(ctx, proxy.ManagedSubscriptionProvidersContextKey{}, enrolled)
					}
					planStates := proxy.ManagedSubscriptionPlanStates(accounts, svc.CurrentTime())
					if len(planStates) > 0 {
						ctx = context.WithValue(ctx, proxy.ManagedSubscriptionPlanStatesContextKey{}, planStates)
					}
				}
			}
		}
		if installation != nil {
			if installation.ExternalID != "" {
				ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, installation.ExternalID)
			}
			if installation.ID != "" {
				ctx = context.WithValue(ctx, proxy.InstallationIDContextKey{}, installation.ID)
			}
			if len(installation.ExcludedModels) > 0 {
				ctx = context.WithValue(ctx, proxy.InstallationExcludedModelsContextKey{}, installation.ExcludedModels)
			}
			if len(installation.AllowedModels) > 0 {
				ctx = context.WithValue(ctx, proxy.InstallationAllowedModelsContextKey{}, installation.AllowedModels)
			}
			if len(installation.ModelsWhenSubscriptionActive) > 0 {
				ctx = context.WithValue(ctx, proxy.InstallationSubscriptionModelsWhenActiveContextKey{}, installation.ModelsWhenSubscriptionActive)
			}
			if len(installation.ModelsWhenSubscriptionInactive) > 0 {
				ctx = context.WithValue(ctx, proxy.InstallationSubscriptionModelsWhenInactiveContextKey{}, installation.ModelsWhenSubscriptionInactive)
			}
			if len(installation.ExcludedProviders) > 0 {
				ctx = context.WithValue(ctx, proxy.InstallationExcludedProvidersContextKey{}, installation.ExcludedProviders)
			}
			if len(installation.PreferredModels) > 0 {
				ctx = context.WithValue(ctx, proxy.InstallationPreferredModelsContextKey{}, installation.PreferredModels)
			}
			if len(installation.FastModeModels) > 0 {
				ctx = context.WithValue(ctx, proxy.InstallationFastModeModelsContextKey{}, installation.FastModeModels)
			}
			if installation.RoutingQualityWeight != nil {
				// User-facing dial position flows in as QualityBias (per-cluster,
				// dispersion-aware), not the uniform Alpha. See router.Overrides.
				ctx = context.WithValue(ctx, proxy.InstallationRoutingKnobsContextKey{}, &router.Overrides{
					QualityBias: installation.RoutingQualityWeight,
				})
			}
			if installation.UsageBypassEnabled {
				ctx = context.WithValue(ctx, proxy.InstallationUsageBypassContextKey{}, proxy.UsageBypassConfig{
					Enabled:   true,
					Threshold: installation.UsageBypassThreshold,
				})
			}
			if installation.SubscriptionRoutingDisabled {
				ctx = context.WithValue(ctx, proxy.InstallationSubscriptionRoutingDisabledContextKey{}, true)
			}
			if installation.HideTerminalSurfaces {
				ctx = context.WithValue(ctx, proxy.InstallationHideTerminalSurfacesContextKey{}, true)
			}
			if installation.RoutingRolloutID != "" {
				ctx = context.WithValue(ctx, proxy.PolicyRolloutIDContextKey{}, installation.RoutingRolloutID)
			}
			if installation.PolicyShadowStrategy != "" {
				ctx = context.WithValue(ctx, proxy.PolicyShadowStrategyContextKey{}, installation.PolicyShadowStrategy)
			}
			if installation.PolicyDebugEnabled {
				ctx = context.WithValue(ctx, proxy.PolicyDebugEnabledContextKey{}, true)
			}
			if installation.PolicyRoutingIntent != "" {
				ctx = context.WithValue(ctx, proxy.PolicyRoutingIntentContextKey{}, installation.PolicyRoutingIntent)
			}
			if installation.AITrainingAllowed {
				ctx = context.WithValue(ctx, proxy.PolicyTrainingAllowedContextKey{}, true)
			}
			if installation.ContentCaptureMode != nil {
				ctx = context.WithValue(ctx, proxy.InstallationCaptureModeContextKey{},
					proxy.ParseCaptureMode(*installation.ContentCaptureMode))
			}
			// Per-organization behavioral flag overrides. Skipped entirely when
			// the deployment-wide escape hatch is set, so an env-var rollback
			// can't be defeated by a stored per-org row. WithOverrides is a
			// no-op for an empty set, which is the common case.
			if !svc.FlagOverridesDisabled() {
				ctx = flags.WithOverrides(ctx, installation.FlagOverrides)
			}
		}
		byokAllowed := !byokRequiresOptIn || (installation != nil && installation.ByokEnabled)
		if externalKeys != nil && byokAllowed {
			ctx = context.WithValue(ctx, proxy.ExternalAPIKeysContextKey{}, externalKeys)
			ctx = proxy.WithForwardedHeaderSnapshot(ctx, externalKeys, c.Request.Header)
		}
		if len(clusterModelLists) > 0 {
			overrides := make(map[string][]string, len(clusterModelLists))
			for _, list := range clusterModelLists {
				if len(list.Models) == 0 {
					continue
				}
				overrides[list.ClusterLabel] = list.Models
			}
			if len(overrides) > 0 {
				ctx = context.WithValue(ctx, proxy.ClusterModelListsContextKey{}, overrides)
			}
		}
		if installation != nil && installation.ID != "" {
			ctx = context.WithValue(ctx, proxy.InstallationIDContextKey{}, installation.ID)
		}
		// Stash the dedicated subscription header (router-keyed path) raw; the
		// proxy validates its shape and decides precedence. Never logged.
		if sub := strings.TrimSpace(c.GetHeader(AnthropicSubscriptionHeader)); sub != "" {
			ctx = context.WithValue(ctx, proxy.AnthropicSubscriptionContextKey{}, sub)
		}
		// Codex (ChatGPT) subscription: stash JWT + account ID raw for the proxy. Never logged.
		if sub := strings.TrimSpace(c.GetHeader(OpenAISubscriptionHeader)); sub != "" {
			ctx = context.WithValue(ctx, proxy.OpenAISubscriptionContextKey{}, sub)
		}
		if acct := strings.TrimSpace(c.GetHeader(OpenAIAccountIDHeader)); acct != "" {
			ctx = context.WithValue(ctx, proxy.OpenAIAccountIDContextKey{}, acct)
		}
		finishAuthSpan(authSpan, nil)
		ctx = restoreRequestParent(ctx, parentCtx)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// tryAdminCookie returns nil so callers fall through to bearer auth when the cookie is absent, admin login is disabled, or the cookie is invalid.
func tryAdminCookie(c *gin.Context, svc *auth.Service) *auth.AdminPrincipal {
	if !svc.AdminLoginEnabled() {
		return nil
	}
	cookie, err := c.Cookie(auth.AdminSessionCookieName)
	if err != nil || cookie == "" {
		return nil
	}
	principal, err := svc.VerifyAdminSession(cookie)
	if err != nil {
		// Stale/tampered cookie: don't fail — caller may still have a valid rk_ bearer.
		return nil
	}
	return principal
}

// extractToken pulls the router token from RouterKeyHeader first, then falls back to Authorization: Bearer or x-api-key.
func extractToken(c *gin.Context) string {
	if t := strings.TrimSpace(c.GetHeader(RouterKeyHeader)); t != "" {
		return t
	}
	if t := extractBearer(c.GetHeader("Authorization")); t != "" {
		return t
	}
	return strings.TrimSpace(c.GetHeader("x-api-key"))
}

func extractBearer(header string) string {
	if header == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func handleAuthError(c *gin.Context, err error) {
	logger := observability.FromGin(c)
	switch {
	case errors.Is(err, auth.ErrInvalidPrefix):
		logger.Debug("Auth rejected: invalid bearer prefix (expected rk_...)")
	case errors.Is(err, auth.ErrInvalidToken):
		logger.Debug("Auth rejected: bearer token did not match an active key")
	case errors.Is(err, auth.ErrWrongKeyScope):
		logger.Debug("Auth rejected: bearer key scope does not cover this surface")
	default:
		// Infra failure — not a bad key. 503 so clients retry instead of treating it as terminal.
		logger.Error("Auth check errored", "err", err)
		c.Header("Retry-After", "1")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "auth_unavailable"})
		return
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
}

// InstallationFrom retrieves the authed installation set by WithAuth. Returns nil for admin-cookie sessions and unauthed requests.
func InstallationFrom(c *gin.Context) *auth.Installation {
	v, ok := c.Get(ctxKeyInstallation)
	if !ok {
		return nil
	}
	installation, _ := v.(*auth.Installation)
	return installation
}

func APIKeyFrom(c *gin.Context) *auth.APIKey {
	v, ok := c.Get(ctxKeyAPIKey)
	if !ok {
		return nil
	}
	apiKey, _ := v.(*auth.APIKey)
	return apiKey
}

// AdminPrincipalFrom retrieves the admin principal set when the request authenticated via the session cookie. Returns nil for rk_-keyed or unauthed requests.
func AdminPrincipalFrom(c *gin.Context) *auth.AdminPrincipal {
	v, ok := c.Get(ctxKeyAdminPrincipal)
	if !ok {
		return nil
	}
	principal, _ := v.(*auth.AdminPrincipal)
	return principal
}
