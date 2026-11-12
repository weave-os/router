package server

import (
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// registerAdminMutationRoutes mounts the self-hosted admin mutations. rk_ tokens
// are rejected so a leaked data-plane key can't mint fresh router keys or rotate
// provider credentials.
func registerAdminMutationRoutes(engine *gin.Engine, authSvc *auth.Service, proxySvc *proxy.Service, features Features, hmmModels admin.HMMRosterSource) {
	mgmt := engine.Group("/admin/v1", middleware.WithTimeout(adminTimeout), middleware.WithAdminOnly(authSvc))
	mgmt.GET("/keys", admin.ListAPIKeysHandler(authSvc))
	subscriptionLoginsEnabled := features.SubscriptionLogins != nil && authSvc.CanSealSubscriptionLogins()
	mgmt.GET("/subscription-owners", admin.ListSubscriptionOwnersHandler(authSvc, subscriptionLoginsEnabled))
	mgmt.GET("/subscription-accounts", admin.ListSubscriptionSeatsHandler(authSvc))
	mgmt.DELETE("/subscription-accounts/:subscriberID/:accountID", admin.InternalDeleteSubscriptionAccountHandler(authSvc))
	if subscriptionLoginsEnabled {
		subscriptionLogins := engine.Group("/admin/v1", middleware.WithTimeout(subscriptionAccountTimeout), middleware.WithAdminOnly(authSvc))
		subscriptionLogins.POST("/subscription-logins/:subscriberID", admin.AdminStartSubscriptionLoginHandler(authSvc, features.SubscriptionLogins))
		subscriptionLogins.POST("/subscription-logins/:subscriberID/complete", admin.AdminCompleteSubscriptionLoginHandler(authSvc, features.SubscriptionLogins))
	}
	mgmt.POST("/keys", admin.IssueAPIKeyHandler(authSvc))
	mgmt.POST("/keys/:id/rotate", admin.RotateAPIKeyHandler(authSvc))
	mgmt.DELETE("/keys/:id", admin.DeleteAPIKeyHandler(authSvc))
	mgmt.GET("/provider-keys", admin.ListExternalKeysHandler(authSvc))
	mgmt.POST("/provider-keys", admin.UpsertExternalKeyHandler(authSvc, hmmModels))
	mgmt.PUT("/provider-keys/:id/model-aliases", admin.UpdateExternalKeyAliasesHandler(authSvc, hmmModels))
	mgmt.PUT("/provider-keys/:id/funding-priority", admin.UpdateExternalKeyFundingPriorityHandler(authSvc))
	// Discovery hits the provider endpoint itself; the group's adminTimeout
	// (10s) bounds that upstream GET /models call.
	mgmt.GET("/provider-keys/:id/models", admin.ListUpstreamModelsHandler(authSvc, proxySvc))
	mgmt.POST("/provider-keys/discover-models", admin.DiscoverModelsHandler(proxySvc))
	mgmt.DELETE("/provider-keys/:id", admin.DeleteExternalKeyHandler(authSvc))
	mgmt.GET("/config", admin.ConfigHandler)
	mgmt.GET("/onboarding", admin.OnboardingHandler(authSvc))
	mgmt.GET("/routing-preferences", admin.GetRoutingPreferencesHandler(authSvc))
	mgmt.PUT("/routing-preferences", admin.UpdateRoutingPreferencesHandler(authSvc))
	mgmt.GET("/content-capture", admin.GetContentCaptureHandler(authSvc, proxySvc))
	mgmt.PUT("/content-capture", admin.UpdateContentCaptureHandler(authSvc, proxySvc))
	mgmt.GET("/fast-mode-models", admin.GetFastModeModelsHandler(authSvc))
	mgmt.PUT("/fast-mode-models", admin.UpdateFastModeModelsHandler(authSvc))
	if hmmModels != nil {
		mgmt.GET("/excluded-models", admin.GetExcludedModelsHandler(authSvc, proxySvc))
		mgmt.PUT("/excluded-models", admin.UpdateExcludedModelsHandler(authSvc, proxySvc))
		mgmt.GET("/allowed-models", admin.GetAllowedModelsHandler(authSvc))
		mgmt.PUT("/allowed-models", admin.UpdateAllowedModelsHandler(authSvc, proxySvc))
		mgmt.GET("/excluded-providers", admin.GetExcludedProvidersHandler(authSvc, hmmModels, proxySvc))
		mgmt.PUT("/excluded-providers", admin.UpdateExcludedProvidersHandler(authSvc, hmmModels, proxySvc))
	}
}
