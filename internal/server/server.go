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
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/telemetry/capture"
	"weave-os/router/internal/telemetry/turnrecord"
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

func Register(engine *gin.Engine, authSvc *auth.Service, proxySvc *proxy.Service, hmmModels admin.HMMRosterSource, mode DeploymentMode, readinessChecker admin.HealthChecker, hmmRosterSources map[router.Strategy]policy.RosterSource, analyticsSvc *analytics.Service) {
	RegisterWithFeatures(engine, authSvc, proxySvc, hmmModels, mode, nil, readinessChecker, hmmRosterSources, analyticsSvc, Features{ErrorVocab: ingress.ErrorVocabulary{Classify: proxy.ClassifyDispatchError, TerminalMessage: proxy.TerminalErrorMessage}})
}

const deploymentDefaultStrategy = router.StrategyHMMEmbedding

func ParseDefaultStrategy(raw string) (router.Strategy, error) {
	strategy := router.Strategy(strings.ToLower(strings.TrimSpace(raw)))
	if strategy == "" {
		return deploymentDefaultStrategy, nil
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
	RosterCatalogID          func(string) string
	TaskDomainPreview        taskdomain.Previewer
	SeatUsage                admin.SeatUsageReader
	SubscriptionCoverage     credentials.SubscriptionCoverage
	CustomerFunding          credentials.CustomerFunding
	DefaultStrategy          router.Strategy
	InternalServiceToken     string
	Admin                    admin.Config
	ErrorVocab               ingress.ErrorVocabulary
	ContentCaptureMode       capture.Mode
	TelemetryQueries         turnrecord.Queries
	FeedbackLinks            *feedback.Links
}

func (f Features) defaultStrategy() router.Strategy {
	if f.DefaultStrategy == "" {
		return deploymentDefaultStrategy
	}
	return f.DefaultStrategy
}

func RegisterWithFeatures(engine *gin.Engine, authSvc *auth.Service, proxySvc *proxy.Service, hmmModels admin.HMMRosterSource, mode DeploymentMode, billingSvc *billing.Service, readinessChecker admin.HealthChecker, hmmRosterSources map[router.Strategy]policy.RosterSource, analyticsSvc *analytics.Service, features Features) {
	engine.Use(func(c *gin.Context) {
		c.Header("Access-Control-Expose-Headers", strings.Join([]string{
			requestcontext.HeaderRouterDecision,
			requestcontext.HeaderRouterProvider,
			requestcontext.HeaderRouterModel,
			requestcontext.HeaderRouterContextWindow,
			requestcontext.HeaderRouterFallbackFrom,
			requestcontext.HeaderRouterFallbackAttempt,
			requestcontext.HeaderRouterCostUSD,
			requestcontext.HeaderRouterCostInputUSD,
			requestcontext.HeaderRouterCostOutputUSD,
			requestcontext.HeaderRouterCacheReadTokens,
			requestcontext.HeaderRouterCacheCreationTokens,
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
		subscriberAllowanceMiddleware = []gin.HandlerFunc{middleware.WithSubscriberAllowance(features.SubscriberAllowance, features.SubscriptionCoverage)}
		subscriberProductScopeMiddleware = []gin.HandlerFunc{middleware.WithSubscriberProductScope(features.SubscriberAllowance)}
	}
	var servingAdmissionMiddleware []gin.HandlerFunc
	if features.ServingAdmission != nil {
		servingAdmissionMiddleware = []gin.HandlerFunc{middleware.WithServingAdmission(features.ServingAdmission)}
		engine.POST(policyregistry.WorkerValidationPath, middleware.WithTimeout(30*time.Second), middleware.WithInternalServiceAuth(features.InternalServiceToken), middleware.ServingValidationHandler(features.ServingAdmission))
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
	defaultStrategy := middleware.NormalizeRouterStrategyDefault(features.defaultStrategy(), registeredStrategies...)
	discovery.GET(
		"/v1/router/policies",
		middleware.WithTimeout(healthTimeout),
		admin.PolicyCatalogHandler(proxySvc, defaultStrategy),
	)

	if hmmModels != nil {
		engine.GET("/v1/router/models", middleware.WithTimeout(catalogModelsTimeout), admin.CatalogModelsHandler(hmmModels))
	}

	registerStrategyDiscovery(discovery, hmmRosterSources, features)

	if internalToken := features.InternalServiceToken; internalToken != "" {
		internalGroup := engine.Group("/internal/v1", middleware.WithTimeout(adminTimeout), middleware.WithInternalServiceAuth(internalToken))
		if features.TestPlans != nil {
			internalGroup.GET("/test-plans/identities", admin.InternalTestPlanIdentitiesHandler(features.TestPlans))
			internalGroup.POST("/test-plans/preview", admin.InternalTestPlanPreviewHandler(features.TestPlans))
			internalGroup.POST("/test-plans/prepare", admin.InternalTestPlanPrepareHandler(features.TestPlans))
			internalGroup.DELETE("/test-plans/launches/:id", admin.InternalTestPlanRevokeHandler(features.TestPlans))
		}
		internalGroup.POST("/provider-keys/models", admin.InternalListUpstreamModelsHandler(authSvc, proxy.DispatchEngine(proxySvc)))
		internalGroup.POST("/session-issuers", admin.InternalRegisterSessionIssuerHandler(authSvc))
		internalGroup.DELETE("/session-issuers/:installationID/:kid", admin.InternalRevokeSessionIssuerHandler(authSvc))
		registerInternalSubscriptions(engine, internalGroup, authSvc, features, internalToken)

		internalGroup.GET("/inference-policies", admin.InternalInferencePoliciesHandler(proxy.DispatchEngine(proxySvc)))
		internalGroup.GET("/inference-policies/deployment", admin.InternalInferenceDeploymentHandler(proxy.DispatchEngine(proxySvc)))
		internalGroup.POST("/inference-policies/resolve", admin.InternalInferenceResolveHandler(proxy.DispatchEngine(proxySvc)))
		if features.TaskDomainPreview != nil {
			internalGroup.POST("/task-domain/preview", admin.InternalTaskDomainPreviewHandler(features.TaskDomainPreview))
		}
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
		authPublic.POST("/login", admin.LoginHandler(authSvc, features.Admin))
		authPublic.POST("/logout", admin.LogoutHandler(features.Admin))
		authPublic.GET("/me", admin.MeHandler(authSvc, features.Admin))

		metrics := engine.Group("/admin/v1", middleware.WithTimeout(adminTimeout), middleware.WithAdminOrAuth(authSvc, byokRequiresOptIn))
		metrics.GET("/metrics/summary", admin.MetricsSummaryHandler(features.TelemetryQueries))
		metrics.GET("/metrics/timeseries", admin.MetricsTimeseriesHandler(features.TelemetryQueries))
		metrics.GET("/metrics/details", admin.MetricsDetailsHandler(features.TelemetryQueries))
		metrics.GET("/metrics/model-breakdown", admin.MetricsModelBreakdownHandler(features.TelemetryQueries))
		metrics.GET("/metrics/series", admin.MetricsSeriesHandler(features.TelemetryQueries))
		metrics.GET("/metrics/report-summary", admin.MetricsReportSummaryHandler(features.TelemetryQueries))
		metrics.GET("/metrics/people", admin.MetricsPeopleHandler(features.TelemetryQueries))
		metrics.GET("/metrics/drill-down", admin.MetricsDrillDownHandler(features.TelemetryQueries))
		metrics.GET("/metrics/individuals", admin.MetricsIndividualsHandler(features.TelemetryQueries))
		metrics.GET("/subscription-pool", admin.SubscriptionPoolHandler(features.TelemetryQueries, features.SubscriptionPoolsEnabled, time.Now))

		mgmt := engine.Group("/admin/v1", middleware.WithTimeout(adminTimeout), middleware.WithAdminOnly(authSvc))
		mgmt.GET("/keys", admin.ListAPIKeysHandler(authSvc))
		mgmt.POST("/keys", admin.IssueAPIKeyHandler(authSvc))
		mgmt.POST("/keys/:id/rotate", admin.RotateAPIKeyHandler(authSvc))
		mgmt.DELETE("/keys/:id", admin.DeleteAPIKeyHandler(authSvc))
		mgmt.GET("/provider-keys", admin.ListExternalKeysHandler(authSvc))
		mgmt.POST("/provider-keys", admin.UpsertExternalKeyHandler(authSvc, hmmModels, features.Admin))
		mgmt.PUT("/provider-keys/:id/model-aliases", admin.UpdateExternalKeyAliasesHandler(authSvc, hmmModels))
		mgmt.PUT("/provider-keys/:id/funding-priority", admin.UpdateExternalKeyFundingPriorityHandler(authSvc))
		mgmt.GET("/provider-keys/:id/models", admin.ListUpstreamModelsHandler(authSvc, proxy.DispatchEngine(proxySvc)))
		mgmt.POST("/provider-keys/discover-models", admin.DiscoverModelsHandler(proxy.DispatchEngine(proxySvc)))
		mgmt.DELETE("/provider-keys/:id", admin.DeleteExternalKeyHandler(authSvc))
		mgmt.GET("/config", admin.ConfigHandler(features.Admin))
		mgmt.GET("/onboarding", admin.OnboardingHandler(authSvc))
		mgmt.GET("/routing-preferences", admin.GetRoutingPreferencesHandler(authSvc))
		mgmt.PUT("/routing-preferences", admin.UpdateRoutingPreferencesHandler(authSvc))
		mgmt.GET("/content-capture", admin.GetContentCaptureHandler(authSvc, features.ContentCaptureMode))
		mgmt.PUT("/content-capture", admin.UpdateContentCaptureHandler(authSvc, features.ContentCaptureMode))
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
			middleware.WithBalanceCheck(billingSvc, billing.MinBalanceMicros, features.CustomerFunding),
			middleware.WithAPIKeySpendCap(billingSvc, features.CustomerFunding),
			middleware.WithDelegatedSpendCap(billingSvc, features.CustomerFunding),
			middleware.WithOrgMonthlySpendCap(billingSvc, features.CustomerFunding),
			middleware.WithBillingSpanEnd(),
		)
	}
	messagesMiddleware = append(messagesMiddleware,
		middleware.WithRouterStrategyDefault(defaultStrategy, strategyAvailability, registeredStrategies...),
		middleware.WithPolicyDebugOverride(), middleware.WithFlagOverridesHeader(authSvc),
		middleware.WithAllowedModelsOverride(proxySvc),
		middleware.WithRoutingKnobsOverride(),
		middleware.WithForceEffortOverride(),
	)
	messagesMiddleware = append(messagesMiddleware, policyPinMiddleware...)
	messagesMiddleware = append(messagesMiddleware, middleware.WithClassifierThread(proxySvc, strategyAvailability))
	messagesGroup := engine.Group("", messagesMiddleware...)
	messagesGroup.POST("/v1/messages", anthropicapi.MessagesHandler(proxySvc, authSvc, features.ErrorVocab))
	messagesGroup.POST("/v1/route/handoff", anthropicapi.PrepareHandoffHandler(proxySvc, authSvc, features.ErrorVocab))

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
			middleware.WithBalanceCheck(billingSvc, billing.MinBalanceMicros, features.CustomerFunding),
			middleware.WithAPIKeySpendCap(billingSvc, features.CustomerFunding),
			middleware.WithDelegatedSpendCap(billingSvc, features.CustomerFunding),
			middleware.WithOrgMonthlySpendCap(billingSvc, features.CustomerFunding),
			middleware.WithBillingSpanEnd(),
		)
	}
	chatCompletionMiddleware = append(chatCompletionMiddleware,
		middleware.WithRouterStrategyDefault(defaultStrategy, strategyAvailability, registeredStrategies...),
		middleware.WithPolicyDebugOverride(), middleware.WithFlagOverridesHeader(authSvc),
		middleware.WithAllowedModelsOverride(proxySvc),
		middleware.WithRoutingKnobsOverride(),
		middleware.WithForceEffortOverride(),
	)
	chatCompletionMiddleware = append(chatCompletionMiddleware, middleware.WithClassifierThread(proxySvc, strategyAvailability))
	chatCompletionWithoutPolicyPin := append([]gin.HandlerFunc(nil), chatCompletionMiddleware...)
	chatCompletionMiddleware = append(chatCompletionMiddleware, policyPinMiddleware...)
	chatCompletionGroup := engine.Group("", chatCompletionMiddleware...)
	chatCompletionGroup.POST("/v1/chat/completions", openaiapi.ChatCompletionHandler(proxySvc, authSvc, features.ErrorVocab))
	chatCompletionWithoutPolicyPinGroup := engine.Group("", chatCompletionWithoutPolicyPin...)
	chatCompletionWithoutPolicyPinGroup.POST("/v1/responses", openaiapi.ResponsesHandler(proxySvc, authSvc, features.ErrorVocab))
	chatCompletionWithoutPolicyPinGroup.POST("/v1beta/models/:modelAction", geminiapi.GenerateContentHandler(proxySvc, authSvc, features.ErrorVocab))

	registerClientPassthroughRoutes(engine, authSvc, proxySvc, byokRequiresOptIn, servingAdmissionMiddleware, features)

	sessionCostGroup := engine.Group("",
		middleware.WithTimeout(sessionCostTimeout),
		middleware.WithReadKey(authSvc),
		middleware.WithInstallationRateLimit(middleware.SessionCostRequestsPerMinute, time.Now),
	)
	sessionCostGroup.GET("/v1/sessions/:session_id/cost", admin.SessionCostHandler(features.TelemetryQueries))

	routeMiddleware := []gin.HandlerFunc{
		middleware.WithTimeout(routeTimeout),
		middleware.WithAuth(authSvc, byokRequiresOptIn, features.ServingAdmission),
	}
	routeMiddleware = append(routeMiddleware, servingAdmissionMiddleware...)
	routeMiddleware = append(routeMiddleware, subscriberProductScopeMiddleware...)
	if billingSvc != nil {
		routeMiddleware = append(routeMiddleware,
			middleware.WithBillingSpan(),
			middleware.WithBalanceCheck(billingSvc, billing.MinBalanceMicros, features.CustomerFunding),
			middleware.WithAPIKeySpendCap(billingSvc, features.CustomerFunding),
			middleware.WithDelegatedSpendCap(billingSvc, features.CustomerFunding),
			middleware.WithOrgMonthlySpendCap(billingSvc, features.CustomerFunding),
			middleware.WithBillingSpanEnd(),
		)
	}
	routeMiddleware = append(routeMiddleware,
		middleware.WithRouterStrategyDefault(defaultStrategy, strategyAvailability, registeredStrategies...),
		middleware.WithPolicyDebugOverride(), middleware.WithFlagOverridesHeader(authSvc),
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
		middleware.WithPolicyDebugOverride(), middleware.WithFlagOverridesHeader(authSvc),
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

	if features.FeedbackLinks.Enabled() {
		feedbackGroup := engine.Group("/v1/feedback", middleware.WithTimeout(feedbackTimeout))
		feedbackGroup.GET("/link/:token", feedbackapi.GetContextHandler(features.FeedbackLinks))
		feedbackGroup.POST("/link", feedbackapi.SubmitHandler(features.FeedbackLinks))
		feedbackGroup.GET("/rate", feedbackapi.RateHandler(features.FeedbackLinks))
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

func registerStrategyDiscovery(discovery gin.IRoutes, hmmRosterSources map[router.Strategy]policy.RosterSource, features Features) {
	if features.RoutingDistribution != nil {
		discovery.GET("/v1/router/routing-distribution", middleware.WithTimeout(healthTimeout), admin.RoutingDistributionHandler(features.RoutingDistribution))
	}

	if len(hmmRosterSources) > 0 && features.RosterCatalogID != nil {
		discovery.GET("/v1/router/hmm-roster", middleware.WithTimeout(readinessTimeout), admin.HMMRosterHandler(hmmRosterSources, features.RosterCatalogID))
	}
}
