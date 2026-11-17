package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/api/admin"
	analyticsapi "weave-os/router/internal/api/analytics"
	anthropicapi "weave-os/router/internal/api/anthropic"
	classifierapi "weave-os/router/internal/api/classifier"
	feedbackapi "weave-os/router/internal/api/feedback"
	geminiapi "weave-os/router/internal/api/gemini"
	openaiapi "weave-os/router/internal/api/openai"
	subscriptionsapi "weave-os/router/internal/api/subscriptions"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/trafficcapture"

	"github.com/gin-gonic/gin"
)

const (
	healthTimeout    = 1 * time.Second
	readinessTimeout = 2 * time.Second
	startupTimeout   = 2 * time.Second
	validateTimeout  = 5 * time.Second

	messagesTimeout            = 600 * time.Second
	chatCompletionTimeout      = 600 * time.Second
	passthroughTimeout         = 10 * time.Second
	routeTimeout               = 5 * time.Second
	adminTimeout               = 10 * time.Second
	subscriptionAccountTimeout = 30 * time.Second
	catalogModelsTimeout       = policyclient.DefaultTimeout * 2
	modelPreparationTimeout    = 10 * time.Second
	feedbackTimeout            = 5 * time.Second
	sessionCostTimeout         = 5 * time.Second
	analyticsTimeout           = 60 * time.Second
)

type DeploymentMode string

const (
	DeploymentModeSelfHosted DeploymentMode = "selfhosted"
	DeploymentModeManaged    DeploymentMode = "managed"
)

func Register(engine *gin.Engine, authSvc *auth.Service, proxySvc *proxy.Service, hmmModels admin.HMMRosterSource, mode DeploymentMode, billingSvc *billing.Service, readinessChecker admin.HealthChecker, hmmRosterSources map[router.Strategy]policy.RosterSource, analyticsSvc *analytics.Service) {
	RegisterWithFeatures(engine, authSvc, proxySvc, hmmModels, mode, billingSvc, readinessChecker, hmmRosterSources, analyticsSvc, Features{})
}

func DefaultStrategyFromEnv() (router.Strategy, error) {
	return ParseDefaultStrategy(os.Getenv("ROUTER_DEFAULT_STRATEGY"))
}

func ParseDefaultStrategy(raw string) (router.Strategy, error) {
	strategy := router.Strategy(strings.ToLower(strings.TrimSpace(raw)))
	switch {
	case strategy == "":
		return router.StrategyHMMEmbedding, nil
	case router.IsRetiredStrategy(strategy):
		return "", fmt.Errorf("ROUTER_DEFAULT_STRATEGY=%q is no longer supported: the cluster scorer is retired; unset it to use %q or set another registered strategy", raw, router.StrategyHMMEmbedding)
	}
	if router.IsBetaStrategy(strategy) || strategy == router.StrategyLLMClassifier {
		return "", fmt.Errorf("ROUTER_DEFAULT_STRATEGY=%q is not a valid deployment default: beta and classifier lanes are opt-in per installation", raw)
	}
	return strategy, nil
}

type Features struct {
	StartupDatabasePing      func(context.Context) error
	TrafficCapture           trafficcapture.Recorder
	PolicyPinEnabled         bool
	ServingAdmission         *middleware.ServingAdmissionConfig
	TestPlans                *policyregistry.TestPlanTools
	SubscriberAllowance      *entitlement.Service
	SubscriptionPoolsEnabled bool
	SubscriptionLogins       admin.SubscriptionLoginFlow
	SubscriptionRefresh      subscriptions.AccountRefresher
	PolicyStatus             admin.PolicyStatusSource
	RoutingDistribution      admin.DistributionRosterSource
}

func RegisterWithFeatures(engine *gin.Engine, authSvc *auth.Service, proxySvc *proxy.Service, hmmModels admin.HMMRosterSource, mode DeploymentMode, billingSvc *billing.Service, readinessChecker admin.HealthChecker, hmmRosterSources map[router.Strategy]policy.RosterSource, analyticsSvc *analytics.Service, features Features) {
	engine.Use(func(c *gin.Context) {
		c.Header("Access-Control-Expose-Headers", strings.Join([]string{
			proxy.HeaderRouterDecision,
			proxy.HeaderRouterProvider,
			proxy.HeaderRouterModel,
			proxy.HeaderRouterContextWindow,
			proxy.HeaderRouterFallbackFrom,
			proxy.HeaderRouterFallbackAttempt,
			proxy.HeaderRouterCostUSD,
			proxy.HeaderRouterCostInputUSD,
			proxy.HeaderRouterCostOutputUSD,
			proxy.HeaderRouterCacheReadTokens,
			proxy.HeaderRouterCacheCreationTokens,
		}, ", "))
		c.Next()
	})
	if features.TrafficCapture != nil {
		engine.Use(middleware.WithTrafficCapture(features.TrafficCapture))
	}
	engine.Use(gin.Recovery())
	byokRequiresOptIn := mode == DeploymentModeManaged
	var policyPinMiddleware []gin.HandlerFunc
	if features.PolicyPinEnabled {
		policyPinMiddleware = []gin.HandlerFunc{middleware.WithPolicyPinOverride()}
	}
	var subscriberAllowanceMiddleware []gin.HandlerFunc
	var subscriberProductScopeMiddleware []gin.HandlerFunc
	if features.SubscriberAllowance != nil {
		subscriberAllowanceMiddleware = []gin.HandlerFunc{middleware.WithSubscriberAllowance(features.SubscriberAllowance)}
		subscriberProductScopeMiddleware = []gin.HandlerFunc{middleware.WithSubscriberProductScope(features.SubscriberAllowance)}
	}
	var servingAdmissionMiddleware []gin.HandlerFunc
	if features.ServingAdmission != nil {
		servingAdmissionMiddleware = []gin.HandlerFunc{middleware.WithServingAdmission(features.ServingAdmission)}
		engine.POST(policyregistry.WorkerValidationPath, middleware.WithTimeout(30*time.Second), middleware.WithInternalServiceAuth(strings.TrimSpace(os.Getenv("ROUTER_INTERNAL_SERVICE_TOKEN"))), middleware.ServingValidationHandler(features.ServingAdmission))
	}
	var discoveryMiddleware []gin.HandlerFunc
	if features.ServingAdmission != nil {
		discoveryMiddleware = append(discoveryMiddleware, middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission))
		discoveryMiddleware = append(discoveryMiddleware, servingAdmissionMiddleware...)
	}
	discovery := engine.Group("", discoveryMiddleware...)
	if features.ServingAdmission != nil && mode == DeploymentModeManaged {
		discovery.POST("/v1/router/models/:model/prepare", middleware.WithTimeout(modelPreparationTimeout), classifierapi.PrepareModelHandler(proxySvc))
		discovery.GET("/v1/router/models/:model/prepare/:operation", middleware.WithTimeout(modelPreparationTimeout), classifierapi.PrepareModelHandler(proxySvc))
	}
	if features.ServingAdmission != nil {
		discovery.GET("/v1/test-plan/validate", middleware.WithTimeout(catalogModelsTimeout), admin.TestPlanValidationHandler)
	}

	engine.GET("/livez", middleware.WithTimeout(healthTimeout), admin.HealthHandler)
	engine.GET("/health", middleware.WithTimeout(healthTimeout), admin.HealthHandler)
	engine.GET("/readyz", middleware.WithTimeout(readinessTimeout), admin.ReadinessHandler(readinessChecker))
	engine.GET("/startupz", middleware.WithTimeout(startupTimeout), admin.StartupHandler(features.StartupDatabasePing))

	engine.GET("/v1/version", middleware.WithTimeout(healthTimeout), admin.VersionHandler(features.PolicyStatus))
	var registeredStrategies []router.Strategy
	var strategyAvailability middleware.StrategyAvailability
	if proxySvc != nil {
		registeredStrategies = proxySvc.RegisteredStrategies()
		strategyAvailability = proxySvc.PolicyStrategyAvailable
	}
	envDefaultStrategy, err := DefaultStrategyFromEnv()
	if err != nil {
		panic(err)
	}
	defaultStrategy := middleware.NormalizeRouterStrategyDefault(envDefaultStrategy, registeredStrategies...)
	discovery.GET(
		"/v1/router/policies",
		middleware.WithTimeout(healthTimeout),
		admin.PolicyCatalogHandler(proxySvc, defaultStrategy),
	)

	if hmmModels != nil {
		engine.GET("/v1/router/models", middleware.WithTimeout(catalogModelsTimeout), admin.CatalogModelsHandler(hmmModels))
	}

	if features.ServingAdmission != nil {
		discovery.GET("/v1/router/routing-distribution", middleware.WithTimeout(healthTimeout), admin.RoutingDistributionHandler(policyregistry.AdmittedRosterSource{}))
	} else if features.RoutingDistribution != nil {
		discovery.GET("/v1/router/routing-distribution", middleware.WithTimeout(healthTimeout), admin.RoutingDistributionHandler(features.RoutingDistribution))
	}

	if len(hmmRosterSources) > 0 {
		discovery.GET("/v1/router/hmm-roster", middleware.WithTimeout(readinessTimeout), admin.HMMRosterHandler(hmmRosterSources))
	}

	if internalToken := strings.TrimSpace(os.Getenv("ROUTER_INTERNAL_SERVICE_TOKEN")); internalToken != "" {
		internalGroup := engine.Group("/internal/v1", middleware.WithTimeout(adminTimeout), middleware.WithInternalServiceAuth(internalToken))
		if features.TestPlans != nil {
			internalGroup.GET("/test-plans/identities", admin.InternalTestPlanIdentitiesHandler(features.TestPlans))
			internalGroup.POST("/test-plans/preview", admin.InternalTestPlanPreviewHandler(features.TestPlans))
			internalGroup.POST("/test-plans/prepare", admin.InternalTestPlanPrepareHandler(features.TestPlans))
			internalGroup.DELETE("/test-plans/launches/:id", admin.InternalTestPlanRevokeHandler(features.TestPlans))
		}
		internalGroup.POST("/provider-keys/models", admin.InternalListUpstreamModelsHandler(authSvc, proxySvc))
		internalGroup.POST("/session-issuers", admin.InternalRegisterSessionIssuerHandler(authSvc))
		internalGroup.DELETE("/session-issuers/:installationID/:kid", admin.InternalRevokeSessionIssuerHandler(authSvc))
		registerInternalSubscriptions(engine, internalGroup, authSvc, features, internalToken)

		internalGroup.GET("/inference-policies", admin.InternalInferencePoliciesHandler(proxySvc))
		internalGroup.GET("/inference-policies/deployment", admin.InternalInferenceDeploymentHandler(proxySvc))
		internalGroup.POST("/inference-policies/resolve", admin.InternalInferenceResolveHandler(proxySvc))
		internalGroup.POST("/task-domain/preview", admin.InternalTaskDomainPreviewHandler())
		internalGroup.GET("/escalation/sessions", admin.InternalLLMEscalationSessionsHandler(proxySvc))
		internalGroup.GET("/escalation/dashboard", admin.InternalEscalationDashboardHandler(proxySvc))
		internalGroup.GET("/escalation/sessions/:scope", admin.InternalLLMEscalationSessionHandler(proxySvc))
		internalGroup.GET("/escalation/config/:installationID", admin.InternalEscalationConfigurationHandler(proxySvc))
		internalGroup.PUT("/escalation/config/:installationID", admin.InternalUpdateEscalationConfigurationHandler(proxySvc))
	}

	adminAuthed := engine.Group("", middleware.WithTimeout(validateTimeout), middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission))
	threadHandlers := []gin.HandlerFunc{middleware.WithTimeout(validateTimeout), middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission)}
	threadHandlers = append(threadHandlers, servingAdmissionMiddleware...)
	threadHandlers = append(threadHandlers, classifierapi.StartThreadHandler(proxySvc))
	engine.POST("/v1/router/threads", threadHandlers...)
	adminAuthed.Use(servingAdmissionMiddleware...)
	adminAuthed.GET("/validate", admin.ValidateHandler)
	adminAuthed.POST("/v1/client-events", admin.ClientEventHandler(authSvc))
	if authSvc.SubscriptionAccountsEnabled() {
		subscriptionGroup := engine.Group("/v1", middleware.WithTimeout(subscriptionAccountTimeout), middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission))
		subscriptionGroup.Use(servingAdmissionMiddleware...)
		subscriptionsapi.Register(subscriptionGroup, authSvc)
	}

	if mode == DeploymentModeSelfHosted {
		engine.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/ui") })
		registerUIStatic(engine, "./assets/ui")

		authPublic := engine.Group("/admin/v1/auth", middleware.WithTimeout(adminTimeout))
		authPublic.POST("/login", admin.LoginHandler(authSvc))
		authPublic.POST("/logout", admin.LogoutHandler())
		authPublic.GET("/me", admin.MeHandler(authSvc))

		metrics := engine.Group("/admin/v1", middleware.WithTimeout(adminTimeout), middleware.WithAdminOrAuth(authSvc, byokRequiresOptIn))
		metrics.GET("/metrics/summary", admin.MetricsSummaryHandler(proxySvc))
		metrics.GET("/metrics/timeseries", admin.MetricsTimeseriesHandler(proxySvc))
		metrics.GET("/metrics/details", admin.MetricsDetailsHandler(proxySvc))
		metrics.GET("/metrics/model-breakdown", admin.MetricsModelBreakdownHandler(proxySvc))
		metrics.GET("/metrics/series", admin.MetricsSeriesHandler(proxySvc))
		metrics.GET("/metrics/report-summary", admin.MetricsReportSummaryHandler(proxySvc))
		metrics.GET("/metrics/people", admin.MetricsPeopleHandler(proxySvc))
		metrics.GET("/metrics/drill-down", admin.MetricsDrillDownHandler(proxySvc))
		metrics.GET("/metrics/individuals", admin.MetricsIndividualsHandler(proxySvc))
		metrics.GET("/subscription-pool", admin.SubscriptionPoolHandler(proxySvc, features.SubscriptionPoolsEnabled, time.Now))

		mgmt := engine.Group("/admin/v1", middleware.WithTimeout(adminTimeout), middleware.WithAdminOnly(authSvc))
		mgmt.GET("/keys", admin.ListAPIKeysHandler(authSvc))
		mgmt.POST("/keys", admin.IssueAPIKeyHandler(authSvc))
		mgmt.POST("/keys/:id/rotate", admin.RotateAPIKeyHandler(authSvc))
		mgmt.DELETE("/keys/:id", admin.DeleteAPIKeyHandler(authSvc))
		mgmt.GET("/provider-keys", admin.ListExternalKeysHandler(authSvc))
		mgmt.POST("/provider-keys", admin.UpsertExternalKeyHandler(authSvc, hmmModels))
		mgmt.PUT("/provider-keys/:id/model-aliases", admin.UpdateExternalKeyAliasesHandler(authSvc, hmmModels))
		mgmt.PUT("/provider-keys/:id/funding-priority", admin.UpdateExternalKeyFundingPriorityHandler(authSvc))
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

	messagesMiddleware := []gin.HandlerFunc{
		middleware.WithTimingEntry(),
		middleware.WithTimeout(messagesTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	}
	messagesMiddleware = append(messagesMiddleware, servingAdmissionMiddleware...)
	messagesMiddleware = append(messagesMiddleware, middleware.WithAgentShadowEvaluation())
	messagesMiddleware = append(messagesMiddleware, subscriberAllowanceMiddleware...)
	if billingSvc != nil {
		messagesMiddleware = append(messagesMiddleware,
			middleware.WithBillingSpan(),
			middleware.WithBalanceCheck(billingSvc, billing.MinBalanceMicros),
			middleware.WithAPIKeySpendCap(billingSvc),
			middleware.WithDelegatedSpendCap(billingSvc),
			middleware.WithOrgMonthlySpendCap(billingSvc),
			middleware.WithBillingSpanEnd(),
		)
	}
	messagesMiddleware = append(messagesMiddleware,
		middleware.WithRouterStrategyDefault(defaultStrategy, strategyAvailability, registeredStrategies...),
		middleware.WithPolicyDebugOverride(),
		middleware.WithAllowedModelsOverride(proxySvc),
		middleware.WithRoutingKnobsOverride(),
		middleware.WithForceEffortOverride(),
	)
	messagesMiddleware = append(messagesMiddleware, policyPinMiddleware...)
	messagesMiddleware = append(messagesMiddleware, middleware.WithClassifierThread(proxySvc, strategyAvailability))
	messagesGroup := engine.Group("", messagesMiddleware...)
	messagesGroup.POST("/v1/messages", anthropicapi.MessagesHandler(proxySvc, authSvc))
	messagesGroup.POST("/v1/route/handoff", anthropicapi.PrepareHandoffHandler(proxySvc, authSvc))

	chatCompletionMiddleware := []gin.HandlerFunc{
		middleware.WithTimingEntry(),
		middleware.WithTimeout(chatCompletionTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	}
	chatCompletionMiddleware = append(chatCompletionMiddleware, servingAdmissionMiddleware...)
	chatCompletionMiddleware = append(chatCompletionMiddleware, subscriberAllowanceMiddleware...)
	if billingSvc != nil {
		chatCompletionMiddleware = append(chatCompletionMiddleware,
			middleware.WithBillingSpan(),
			middleware.WithBalanceCheck(billingSvc, billing.MinBalanceMicros),
			middleware.WithAPIKeySpendCap(billingSvc),
			middleware.WithDelegatedSpendCap(billingSvc),
			middleware.WithOrgMonthlySpendCap(billingSvc),
			middleware.WithBillingSpanEnd(),
		)
	}
	chatCompletionMiddleware = append(chatCompletionMiddleware,
		middleware.WithRouterStrategyDefault(defaultStrategy, strategyAvailability, registeredStrategies...),
		middleware.WithPolicyDebugOverride(),
		middleware.WithAllowedModelsOverride(proxySvc),
		middleware.WithRoutingKnobsOverride(),
		middleware.WithForceEffortOverride(),
	)
	chatCompletionMiddleware = append(chatCompletionMiddleware, middleware.WithClassifierThread(proxySvc, strategyAvailability))
	chatCompletionWithoutPolicyPin := append([]gin.HandlerFunc(nil), chatCompletionMiddleware...)
	chatCompletionMiddleware = append(chatCompletionMiddleware, policyPinMiddleware...)
	chatCompletionGroup := engine.Group("", chatCompletionMiddleware...)
	chatCompletionGroup.POST("/v1/chat/completions", openaiapi.ChatCompletionHandler(proxySvc, authSvc))
	chatCompletionWithoutPolicyPinGroup := engine.Group("", chatCompletionWithoutPolicyPin...)
	chatCompletionWithoutPolicyPinGroup.POST("/v1/responses", openaiapi.ResponsesHandler(proxySvc, authSvc))
	chatCompletionWithoutPolicyPinGroup.POST("/v1beta/models/:modelAction", geminiapi.GenerateContentHandler(proxySvc, authSvc))

	registerClientPassthroughRoutes(engine, authSvc, proxySvc, byokRequiresOptIn, features.ServingAdmission, servingAdmissionMiddleware)

	sessionCostGroup := engine.Group("",
		middleware.WithTimeout(sessionCostTimeout),
		middleware.WithReadKey(authSvc),
		middleware.WithInstallationRateLimit(middleware.SessionCostRequestsPerMinute, time.Now),
	)
	sessionCostGroup.GET("/v1/sessions/:session_id/cost", admin.SessionCostHandler(proxySvc))

	routeMiddleware := []gin.HandlerFunc{
		middleware.WithTimeout(routeTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	}
	routeMiddleware = append(routeMiddleware, servingAdmissionMiddleware...)
	routeMiddleware = append(routeMiddleware, subscriberProductScopeMiddleware...)
	if billingSvc != nil {
		routeMiddleware = append(routeMiddleware,
			middleware.WithBillingSpan(),
			middleware.WithBalanceCheck(billingSvc, billing.MinBalanceMicros),
			middleware.WithAPIKeySpendCap(billingSvc),
			middleware.WithDelegatedSpendCap(billingSvc),
			middleware.WithOrgMonthlySpendCap(billingSvc),
			middleware.WithBillingSpanEnd(),
		)
	}
	routeMiddleware = append(routeMiddleware,
		middleware.WithRouterStrategyDefault(defaultStrategy, strategyAvailability, registeredStrategies...),
		middleware.WithPolicyDebugOverride(),
		middleware.WithAllowedModelsOverride(proxySvc),
		middleware.WithRoutingKnobsOverride(),
		middleware.WithForceEffortOverride(),
	)
	routeMiddleware = append(routeMiddleware, policyPinMiddleware...)
	routeMiddleware = append(routeMiddleware, middleware.WithClassifierThread(proxySvc, strategyAvailability))
	routeGroup := engine.Group("", routeMiddleware...)
	routeGroup.POST("/v1/route", anthropicapi.RouteHandler(proxySvc))

	previewMiddleware := []gin.HandlerFunc{
		middleware.WithTimingEntry(),
		middleware.WithTimeout(routeTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	}
	previewMiddleware = append(previewMiddleware, servingAdmissionMiddleware...)
	previewMiddleware = append(previewMiddleware, subscriberProductScopeMiddleware...)
	previewMiddleware = append(previewMiddleware,
		middleware.WithRouterStrategyDefault(defaultStrategy, strategyAvailability, registeredStrategies...),
		middleware.WithPolicyDebugOverride(),
		middleware.WithAllowedModelsOverride(proxySvc),
		middleware.WithRoutingKnobsOverride(),
	)
	previewMiddleware = append(previewMiddleware, policyPinMiddleware...)
	previewMiddleware = append(previewMiddleware, middleware.WithClassifierThread(proxySvc, strategyAvailability))
	previewGroup := engine.Group("", previewMiddleware...)
	previewGroup.POST("/v1/route/preview", anthropicapi.PreviewRouteHandler(proxySvc))

	if analyticsSvc != nil {
		analyticsGroup := engine.Group("/v1/analytics",
			middleware.WithTimeout(analyticsTimeout),
			middleware.WithAnalyticsKey(authSvc),
			middleware.WithAnalyticsRateLimit(middleware.AnalyticsRequestsPerMinute),
		)
		analyticsGroup.GET("/routing-decisions", analyticsapi.RoutingDecisionsHandler(analyticsSvc))
		analyticsGroup.GET("/models", analyticsapi.ModelsHandler())
		analyticsGroup.GET("/schema", analyticsapi.SchemaHandler())
	}

	if proxySvc.FeedbackEnabled() {
		feedbackGroup := engine.Group("/v1/feedback", middleware.WithTimeout(feedbackTimeout))
		feedbackGroup.GET("/link/:token", feedbackapi.GetContextHandler(proxySvc))
		feedbackGroup.POST("/link", feedbackapi.SubmitHandler(proxySvc))
		feedbackGroup.GET("/rate", feedbackapi.RateHandler(proxySvc))
		feedbackGroup.GET("/assets/wooly-wave.png", feedbackapi.WoolyWaveHandler())
		feedbackGroup.GET("/assets/weave.svg", feedbackapi.WeaveLogoHandler())
	}
}

func registerUIStatic(engine *gin.Engine, root string) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	handler := func(c *gin.Context) {
		raw := c.Param("filepath")
		raw = strings.TrimPrefix(raw, "/")

		if strings.HasSuffix(raw, "/") && raw != "" {
			target := "/ui/" + strings.TrimSuffix(raw, "/")
			c.Redirect(http.StatusPermanentRedirect, target)
			return
		}

		if raw == "" || raw == "index" {
			http.ServeFile(c.Writer, c.Request, filepath.Join(absRoot, "index.html"))
			return
		}

		cleaned := filepath.Clean("/" + raw)
		fullPath := filepath.Join(absRoot, cleaned)
		if !strings.HasPrefix(fullPath, absRoot+string(filepath.Separator)) && fullPath != absRoot {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		if info, statErr := os.Stat(fullPath); statErr == nil && !info.IsDir() {
			http.ServeFile(c.Writer, c.Request, fullPath)
			return
		}
		htmlPath := fullPath + ".html"
		if info, statErr := os.Stat(htmlPath); statErr == nil && !info.IsDir() {
			http.ServeFile(c.Writer, c.Request, htmlPath)
			return
		}
		c.AbortWithStatus(http.StatusNotFound)
	}
	engine.GET("/ui", handler)
	engine.HEAD("/ui", handler)
	engine.GET("/ui/*filepath", handler)
	engine.HEAD("/ui/*filepath", handler)
}
