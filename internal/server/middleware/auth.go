package middleware

import (
	"context"
	"errors"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/requestcontext"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
)

const (
	ctxKeyInstallation      = "router_installation"
	ctxKeyAPIKey            = "router_api_key"
	ctxKeyAdminPrincipal    = "router_admin_principal"
	ctxKeySubscriptionOwner = "router_subscription_owner"
)

const RouterKeyHeader = auth.RouterKeyHeader

func WithAuth(svc *auth.Service, byokRequiresOptIn bool, serving ...*ServingAdmissionConfig) gin.HandlerFunc {
	return withAPIKey(svc, byokRequiresOptIn, serving...)
}

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

func WithAdminOnly(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !svc.AdminLoginEnabled() {
			abortRefusal(c, refusal.AdminLoginDisabled, nil)
			return
		}
		principal := tryAdminCookie(c, svc)
		if principal == nil {
			abortRefusal(c, refusal.AdminSessionRequired, nil)
			return
		}
		c.Set(ctxKeyAdminPrincipal, principal)
		c.Next()
	}
}

func withAPIKey(svc *auth.Service, byokRequiresOptIn bool, serving ...*ServingAdmissionConfig) gin.HandlerFunc {
	allowedOrganizations := configuredOrganizationAllowlist()
	return func(c *gin.Context) {
		parentCtx := c.Request.Context()
		var testPlan *policyregistry.TestPlanScope
		if len(serving) > 0 && serving[0] != nil {
			var err error
			testPlan, err = prepareDirectTest(c, svc, serving[0])
			if err != nil {
				observability.FromGin(c).Warn("Direct test admission rejected", "err", err)
				if isAuthFailure(err) {
					handleAuthError(c, err)
					return
				}
				abortRefusal(c, refusal.TestScopeRejected, nil)
				return
			}
			parentCtx = c.Request.Context()
		}

		clientSessionID := requestcontext.ClientIdentityFromHeaders(c.Request.Header).SessionID
		if testPlan != nil {
			clientSessionID = testPlan.SessionID
		}
		authCtx, authSpan := startAuthSpan(parentCtx, clientSessionID)
		installation, apiKey, externalKeys, clusterModelLists, delegatedSession, err := verifyCredential(authCtx, svc, extractToken(c), testPlan, allowedOrganizations)
		if err != nil {
			finishAuthSpan(authSpan, err)
			handleAuthError(c, err)
			return
		}
		if testPlan != nil && (apiKey.CredentialSubjectID != testPlan.SubjectID || apiKey.InstallationID != installation.ID) {
			abortRefusal(c, refusal.TestIdentityMismatch, nil)
			return
		}
		c.Set(ctxKeyInstallation, installation)
		c.Set(ctxKeyAPIKey, apiKey)
		ctx := authCtx
		if testPlan == nil {
			ctx, err = svc.WithRoutingPolicy(ctx, installation.ID)
			if err != nil {
				observability.FromContext(ctx).Error("Failed to load installation routing policy", "installation_id", installation.ID, "err", err)
				finishAuthSpan(authSpan, err)
				abortRefusal(c, refusal.RoutingPolicyUnavailable, nil)
				return
			}
		}
		ctx = auth.WithDelegatedSession(ctx, delegatedSession)
		if apiKey != nil {
			ctx = context.WithValue(ctx, requestcontext.APIKeyIDContextKey{}, apiKey.ID)
			if apiKey.PaidFallbackOnSubscriptionRefusal {
				ctx = context.WithValue(ctx, requestcontext.APIKeyPaidFallbackOnSubscriptionRefusalContextKey{}, true)
			}
			ctx = credentials.WithManagedSubscriptionUsage(ctx)
			owner := subscriptionOwnerForRequest(c, apiKey)
			c.Set(ctxKeySubscriptionOwner, owner)
			ctx = credentials.WithSubscriptionOwner(ctx, owner)
			if testPlan == nil && svc.SubscriptionAccountsEnabled() {
				accounts, listErr := svc.ListSubscriptionCandidates(ctx, owner)
				if listErr != nil {
					observability.FromContext(ctx).Error("Failed to load subscription account enrollment", "err", listErr)
					ctx = context.WithValue(ctx, credentials.ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
				} else {
					ctx = credentials.WithManagedSubscriptionCandidates(ctx, accounts)
				}
			}
		}
		if installation != nil {
			if installation.ExternalID != "" {
				ctx = context.WithValue(ctx, requestcontext.ExternalIDContextKey{}, installation.ExternalID)
			}
			if installation.ID != "" {
				ctx = context.WithValue(ctx, requestcontext.InstallationIDContextKey{}, installation.ID)
			}
			if len(installation.ExcludedModels) > 0 {
				ctx = context.WithValue(ctx, requestcontext.InstallationExcludedModelsContextKey{}, installation.ExcludedModels)
			}
			if len(installation.AllowedModels) > 0 {
				ctx = context.WithValue(ctx, requestcontext.InstallationAllowedModelsContextKey{}, installation.AllowedModels)
			}
			if installation.SubscriptionStateModelsEnabled {
				ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionStateModelsEnabledContextKey{}, true)
			}
			if len(installation.ModelsWhenSubscriptionActive) > 0 {
				ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenActiveContextKey{}, installation.ModelsWhenSubscriptionActive)
			}
			if len(installation.ModelsWhenSubscriptionInactive) > 0 {
				ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenInactiveContextKey{}, installation.ModelsWhenSubscriptionInactive)
			}
			if len(installation.ExcludedProviders) > 0 {
				ctx = context.WithValue(ctx, requestcontext.InstallationExcludedProvidersContextKey{}, installation.ExcludedProviders)
			}
			if len(installation.PreferredModels) > 0 {
				ctx = context.WithValue(ctx, requestcontext.InstallationPreferredModelsContextKey{}, installation.PreferredModels)
			}
			if len(installation.FastModeModels) > 0 {
				ctx = context.WithValue(ctx, requestcontext.InstallationFastModeModelsContextKey{}, installation.FastModeModels)
			}
			if installation.RoutingQualityWeight != nil {
				ctx = context.WithValue(ctx, requestcontext.InstallationRoutingKnobsContextKey{}, &router.Overrides{
					QualityBias: installation.RoutingQualityWeight,
				})
			}
			if installation.UsageBypassEnabled {
				ctx = context.WithValue(ctx, requestcontext.InstallationUsageBypassContextKey{}, requestcontext.UsageBypassConfig{
					Enabled:   true,
					Threshold: installation.UsageBypassThreshold,
				})
			}
			if installation.SubscriptionRoutingDisabled {
				ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionRoutingDisabledContextKey{}, true)
			}
			if installation.PickedModelCreditFallbackEnabled {
				ctx = context.WithValue(ctx, requestcontext.InstallationPickedModelCreditFallbackContextKey{}, true)
			}
			if installation.HideTerminalSurfaces {
				ctx = context.WithValue(ctx, requestcontext.InstallationHideTerminalSurfacesContextKey{}, true)
			}
			if installation.ShowModelSelectionReasoning {
				ctx = context.WithValue(ctx, requestcontext.InstallationShowModelSelectionReasoningContextKey{}, true)
			}
			if installation.TrialCaptureEnabled {
				ctx = context.WithValue(ctx, requestcontext.InstallationTrialCaptureContextKey{}, true)
			}
			if installation.RoutingRolloutID != "" {
				ctx = context.WithValue(ctx, requestcontext.PolicyRolloutIDContextKey{}, installation.RoutingRolloutID)
			}
			if installation.PolicyShadowStrategy != "" {
				ctx = context.WithValue(ctx, requestcontext.PolicyShadowStrategyContextKey{}, installation.PolicyShadowStrategy)
			}
			if installation.PolicyDebugEnabled {
				ctx = context.WithValue(ctx, requestcontext.PolicyDebugEnabledContextKey{}, true)
			}
			if installation.PolicyRoutingIntent != "" {
				ctx = context.WithValue(ctx, requestcontext.PolicyRoutingIntentContextKey{}, installation.PolicyRoutingIntent)
			}
			if installation.AITrainingAllowed {
				ctx = context.WithValue(ctx, requestcontext.PolicyTrainingAllowedContextKey{}, true)
			}
			if installation.ContentCaptureMode != nil {
				ctx = context.WithValue(ctx, requestcontext.InstallationCaptureModeContextKey{},
					proxy.ParseCaptureMode(*installation.ContentCaptureMode))
			}
			if !svc.FlagOverridesDisabled() {
				ctx = flags.WithOverrides(ctx, installation.FlagOverrides)
			}
		}
		byokAllowed := testPlan == nil && (!byokRequiresOptIn || (installation != nil && installation.ByokEnabled))
		if externalKeys != nil && byokAllowed {
			ctx = context.WithValue(ctx, credentials.ExternalAPIKeysContextKey{}, externalKeys)
			ctx = requestcontext.WithForwardedHeaderSnapshot(ctx, externalKeys, c.Request.Header)
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
				ctx = context.WithValue(ctx, requestcontext.ClusterModelListsContextKey{}, overrides)
			}
		}
		if installation != nil && installation.ID != "" {
			ctx = context.WithValue(ctx, requestcontext.InstallationIDContextKey{}, installation.ID)
		}
		finishAuthSpan(authSpan, nil)
		ctx = restoreRequestParent(ctx, parentCtx)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func isAuthFailure(err error) bool {
	return errors.Is(err, auth.ErrInvalidPrefix) || errors.Is(err, auth.ErrInvalidToken) ||
		errors.Is(err, auth.ErrWrongKeyScope) || errors.Is(err, auth.ErrPersonalCredentialRequired)
}

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
		return nil
	}
	return principal
}

func extractToken(c *gin.Context) string {
	return auth.RoutingTokenFromHeaders(c.Request.Header)
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
	case errors.Is(err, auth.ErrPersonalCredentialRequired):
		logger.Debug("Auth rejected: personal credential subject is disabled or revoked")
	case errors.Is(err, errOrganizationNotAllowed):
		logger.Info("Auth rejected: organization is not on the inference allowlist")
		abortRefusal(c, refusal.OrganizationNotAllowed, nil)
		return
	default:
		logger.Error("Auth check errored", "err", err)
		abortRefusal(c, refusal.AuthUnavailable, nil)
		return
	}
	abortRefusal(c, refusal.InvalidKey, nil)
}

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

func SubscriptionOwnerFrom(c *gin.Context) auth.SubscriptionOwner {
	if _, ok := requestcontext.InternalTestIdentityFrom(c.Request.Context()); ok {
		return auth.SubscriptionOwner{}
	}
	if owner, ok := c.Get(ctxKeySubscriptionOwner); ok {
		resolved, _ := owner.(auth.SubscriptionOwner)
		if resolved.Valid() {
			return resolved
		}
	}
	return auth.SubscriptionOwnerForKey(APIKeyFrom(c))
}

func SubscriptionOwnerLive(c *gin.Context, svc *auth.Service) (auth.SubscriptionOwner, error) {
	apiKey := APIKeyFrom(c)
	if apiKey == nil || svc == nil {
		return auth.SubscriptionOwner{}, nil
	}
	owner, err := svc.SubscriptionOwnerForRequestUncached(c.Request.Context(), apiKey)
	if err != nil {
		observability.FromGin(c).Error("Failed to resolve request identity for subscription management", "err", err)
		return auth.SubscriptionOwner{}, err
	}
	return owner, nil
}

func subscriptionOwnerForRequest(c *gin.Context, apiKey *auth.APIKey) auth.SubscriptionOwner {
	if _, ok := requestcontext.InternalTestIdentityFrom(c.Request.Context()); ok {
		return auth.SubscriptionOwner{}
	}
	return auth.SubscriptionOwnerForKey(apiKey)
}

func AdminPrincipalFrom(c *gin.Context) *auth.AdminPrincipal {
	v, ok := c.Get(ctxKeyAdminPrincipal)
	if !ok {
		return nil
	}
	principal, _ := v.(*auth.AdminPrincipal)
	return principal
}
