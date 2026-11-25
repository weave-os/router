package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"weave-os/router/cmd/logconfig"
	"weave-os/router/internal/analytics"
	"weave-os/router/internal/anthropicfederation"
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/config"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/entra"
	"weave-os/router/internal/escalationmodal"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/gcsregistry"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/observability/apm"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/postgres/pgtls"
	"weave-os/router/internal/postgres/poolconfig"
	servingpostgres "weave-os/router/internal/postgres/serving"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/providers/cortexagents"
	googleProvider "weave-os/router/internal/providers/google"
	providerHTTP "weave-os/router/internal/providers/httputil"
	openaiProvider "weave-os/router/internal/providers/openai"
	openaiCompatProvider "weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	routerpubsub "weave-os/router/internal/pubsub"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/decisionmodel"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalation/floor"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/server"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/websearch"
	"weave-os/router/internal/wif"

	_ "time/tzdata"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func init() {
	logconfig.Configure()
}

func main() {
	logger := observability.Get()
	egressProbe, err := newStartupEgressProbe(config.GetOr("ROUTER_STARTUP_EGRESS_ORIGINS", ""))
	if err != nil {
		logger.Error("Invalid startup egress configuration; refusing to boot", "err", err)
		panic(err)
	}
	apm.Init(apmConfigFromEnv())
	if err := router.ValidateCatalogReasoningCapabilities(); err != nil {
		logger.Error("Refusing to boot with invalid model reasoning capabilities", "err", err)
		panic(err)
	}

	cfg, err := pgxpool.ParseConfig(config.PostgresDSN())
	if err != nil {
		logger.Error("Failed to parse postgres DSN", "err", err)
		panic(err)
	}
	poolconfig.ConfigureTimeouts(cfg)
	clientTLS, err := pgtls.Configure(cfg, postgresTLSConfigFromEnv())
	if err != nil {
		logger.Error("Failed to configure postgres client TLS", "err", err)
		panic(err)
	}
	if clientTLS {
		logger.Info("Postgres connections authenticate with a client certificate")
	}
	dbTracer, err := postgres.NewPGXTracer(cfg.ConnConfig.Database)
	if err != nil {
		logger.Error("Failed to construct postgres tracer", "err", err)
		panic(err)
	}
	cfg.ConnConfig.Tracer = dbTracer
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO router, public")
		return err
	}
	cfg.MaxConns = 6
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		logger.Error("Failed to construct postgres pool", "err", err)
		panic(err)
	}
	defer pool.Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	pingErr := pool.Ping(pingCtx)
	pingCancel()
	if pingErr != nil {
		logger.Warn("Postgres ping at boot failed; startup probe requires database connectivity", "err", pingErr)
	}

	deploymentMode := server.DeploymentMode(config.GetOr("ROUTER_DEPLOYMENT_MODE", string(server.DeploymentModeSelfHosted)))
	switch deploymentMode {
	case server.DeploymentModeSelfHosted, server.DeploymentModeManaged:
	default:
		err := fmt.Errorf("invalid ROUTER_DEPLOYMENT_MODE %q (expected %q or %q)", deploymentMode, server.DeploymentModeSelfHosted, server.DeploymentModeManaged)
		logger.Error("Refusing to boot with invalid deployment mode", "err", err)
		panic(err)
	}
	logger.Info("Router deployment mode", "mode", deploymentMode)
	allowCodexCredits, creditsErr := config.CodexExtraCreditsAllowed()
	if creditsErr != nil {
		panic(creditsErr)
	}
	if allowCodexCredits && deploymentMode != server.DeploymentModeSelfHosted {
		panic("ROUTER_CODEX_ALLOW_EXTRA_CREDITS is supported only in selfhosted mode")
	}

	if err := validateManagedServingBoot(deploymentMode, osEnvLookup); err != nil {
		logger.Error("Managed serving configuration is incomplete; refusing to boot", "mode", deploymentMode, "err", err)
		panic(err)
	}

	translationCompatibilityMode, err := config.TranslationCompatibilityMode()
	if err != nil {
		logger.Error("Invalid translation compatibility mode", "err", err)
		panic(err)
	}
	logger.Info("Translation compatibility configured", "mode", translationCompatibilityMode)

	keysetJSON := config.GetOr("EXTERNAL_KEY_ENCRYPTION_KEY", "")
	var encryptor auth.Encryptor
	if keysetJSON == "" {
		logger.Warn("EXTERNAL_KEY_ENCRYPTION_KEY not set; BYOK secrets will be stored unencrypted at rest. Set EXTERNAL_KEY_ENCRYPTION_KEY to a Tink AES-256-GCM keyset to enable encryption.")
		encryptor = auth.NoOpEncryptor{}
	} else {
		encryptor, err = auth.NewTinkEncryptor(keysetJSON)
		if err != nil {
			logger.Error("Failed to create Tink encryptor from keyset", "err", err)
			panic(err)
		}
	}

	repo := postgres.NewRepository(pool, encryptor)
	discoveryHTTPClient, err := providerHTTP.NewModelDiscoveryClient(config.GetOr("ROUTER_MODEL_DISCOVERY_PRIVATE_ORIGINS", ""))
	if err != nil {
		logger.Error("Invalid model discovery private-origin configuration", "err", err)
		panic(err)
	}

	providerMap := make(map[string]providers.Client)
	envKeyedProviders := make(map[string]struct{})
	anthropicPassthroughEligible := false
	openaiPassthroughEligible := false

	var billingSvc *billing.Service
	if deploymentMode == server.DeploymentModeManaged {
		byokFeeRate := parseEnvFloat("BYOK_FEE_RATE", billing.DefaultByokFeeRate)
		billingRepo := postgres.NewBillingRepo(pool)
		bootCtx, bootCancel := context.WithTimeout(context.Background(), 5*time.Second)
		tablesExist, billingCheckErr := billingRepo.BillingTablesExist(bootCtx)
		bootCancel()
		switch {
		case billingCheckErr != nil:
			logger.Warn("Boot billing health check errored; defaulting to billing-enabled in managed mode", "err", billingCheckErr)
			billingSvc = billing.NewService(billingRepo).WithByokFeeRate(byokFeeRate)
		case !tablesExist:
			logger.Warn("Billing tables missing from router schema; staying in BYOK-only mode. Apply db migration 0006_credit_billing to enable billing.")
		default:
			billingSvc = billing.NewService(billingRepo).WithByokFeeRate(byokFeeRate)
			logger.Info("Router billing enabled", "min_balance_usd_micros", billing.MinBalanceMicros, "byok_fee_rate", byokFeeRate)
		}
	}

	if billingSvc != nil {
		billingSvc.WithInternalTestPrepaid(servingpostgres.NewInternalTestBook(pool))
		billingSvc.WithDelegatedSpend(postgres.NewBillingRepo(pool))
	}
	byokOnly := deploymentMode == server.DeploymentModeManaged && billingSvc == nil

	anthropicKey := ""
	if !byokOnly {
		anthropicKey = config.GetOr("ANTHROPIC_API_KEY", "")
	}
	anthropicClient := anthropic.NewClient(anthropicKey, anthropic.DefaultBaseURL, anthropic.WithModelListHTTPClient(discoveryHTTPClient))
	providerMap[providers.ProviderAnthropic] = anthropicClient
	switch {
	case byokOnly:
		logger.Info("Anthropic provider enabled (BYOK only)", "base_url", anthropic.DefaultBaseURL)
	case anthropicKey != "":
		envKeyedProviders[providers.ProviderAnthropic] = struct{}{}
		logger.Info("Anthropic provider enabled (router key)", "base_url", anthropic.DefaultBaseURL)
	default:
		anthropicPassthroughEligible = true
		logger.Info("Anthropic provider enabled (client auth passthrough)", "base_url", anthropic.DefaultBaseURL)
	}

	{
		openaiBaseURL := config.GetOr("OPENAI_BASE_URL", openaiProvider.DefaultBaseURL)
		openaiKey := ""
		if !byokOnly {
			openaiKey = config.GetOr("OPENAI_API_KEY", "")
		}
		openaiClient := openaiProvider.NewClientWithModelIDMap(
			openaiKey,
			openaiBaseURL,
			upstreamIDsForProvider(providers.ProviderOpenAI),
		)
		openaiClient.SetCodexExtraCreditsAllowed(allowCodexCredits)
		logger.Info("Codex subscription credit policy configured", "allow_extra_credits", allowCodexCredits)
		if deploymentMode == server.DeploymentModeSelfHosted {
			if codexBaseURL := config.GetOr("ROUTER_CODEX_BASE_URL", ""); codexBaseURL != "" {
				openaiClient.SetCodexBaseURL(codexBaseURL)
			}
		}
		providerMap[providers.ProviderOpenAI] = openaiClient
		switch {
		case byokOnly:
			logger.Info("OpenAI provider enabled (BYOK only)", "base_url", openaiBaseURL)
		case openaiKey != "":
			envKeyedProviders[providers.ProviderOpenAI] = struct{}{}
			logger.Info("OpenAI provider enabled", "base_url", openaiBaseURL)
		default:
			openaiPassthroughEligible = true
			logger.Info("OpenAI provider enabled (client auth passthrough)", "base_url", openaiBaseURL)
		}
	}

	{
		openRouterBaseURL := config.GetOr("OPENROUTER_BASE_URL", openaiCompatProvider.DefaultBaseURL)
		openRouterPlatformEnabled := deploymentMode == server.DeploymentModeSelfHosted ||
			config.GetOr("ROUTER_OPENROUTER_PLATFORM_ENABLED", "false") == "true"
		openRouterKey := ""
		if !byokOnly && openRouterPlatformEnabled {
			openRouterKey = config.GetOr("OPENROUTER_API_KEY", "")
		}
		openRouterModelIDMap, err := openRouterModelIDs(os.Getenv("ROUTER_MODEL_ID_MAP"))
		if err != nil {
			panic(fmt.Sprintf("ROUTER_MODEL_ID_MAP: %v", err))
		}
		providerMap[providers.ProviderOpenRouter] = openaiCompatProvider.NewClientWithModelIDMap(
			openRouterKey,
			openRouterBaseURL,
			openRouterModelIDMap,
			openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient),
		)
		switch {
		case byokOnly:
			logger.Info("OpenRouter provider enabled (BYOK only)", "base_url", openRouterBaseURL)
		case openRouterKey != "":
			envKeyedProviders[providers.ProviderOpenRouter] = struct{}{}
			logger.Info("OpenRouter provider enabled", "base_url", openRouterBaseURL)
		case !openRouterPlatformEnabled:
			logger.Info("OpenRouter provider registered (BYOK only — managed deploys don't use OpenRouter as a platform source; set ROUTER_OPENROUTER_PLATFORM_ENABLED=true to opt in)", "base_url", openRouterBaseURL)
		default:
			logger.Info("OpenRouter provider registered (BYOK only — set OPENROUTER_API_KEY for deployment-level use)", "base_url", openRouterBaseURL)
		}
	}

	{
		fireworksBaseURL := config.GetOr("FIREWORKS_BASE_URL", openaiCompatProvider.FireworksBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderFireworks, "Fireworks", "FIREWORKS_API_KEY", fireworksBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClientWithModelIDMap(key, baseURL, upstreamIDsForProvider(providers.ProviderFireworks), openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			})
	}

	{
		deepInfraBaseURL := config.GetOr("DEEPINFRA_BASE_URL", openaiCompatProvider.DeepInfraBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderDeepInfra, "DeepInfra", "DEEPINFRA_API_KEY", deepInfraBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClientWithModelIDMap(key, baseURL, upstreamIDsForProvider(providers.ProviderDeepInfra), openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			})
	}

	{
		makoraBaseURL := config.GetOr("MAKORA_BASE_URL", openaiCompatProvider.MakoraBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderMakora, "Makora", "MAKORA_API_KEY", makoraBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClientWithModelIDMap(key, baseURL, upstreamIDsForProvider(providers.ProviderMakora), openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			})
	}

	{
		togetherBaseURL := config.GetOr("TOGETHER_BASE_URL", openaiCompatProvider.TogetherBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderTogether, "Together", "TOGETHER_API_KEY", togetherBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClientWithModelIDMap(key, baseURL, upstreamIDsForProvider(providers.ProviderTogether), openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			})
	}

	{
		minimaxRegion := config.GetOr("MINIMAX_REGION", "global")
		minimaxBaseURL := config.GetOr("MINIMAX_BASE_URL", openaiCompatProvider.MiniMaxBaseURL(minimaxRegion))
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderMiniMax, "MiniMax", "MINIMAX_API_KEY", minimaxBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClientWithModelIDMap(key, baseURL, upstreamIDsForProvider(providers.ProviderMiniMax), openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			})
	}

	{
		xaiBaseURL := config.GetOr("XAI_BASE_URL", openaiCompatProvider.XAIBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderXAI, "XAI", "XAI_API_KEY", xaiBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClient(key, baseURL, openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			})
	}

	{
		metaBaseURL := config.GetOr("META_BASE_URL", openaiCompatProvider.MetaBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderMeta, "Meta", "META_API_KEY", metaBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClient(key, baseURL, openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			})
	}

	{
		waferBaseURL := config.GetOr("WAFER_BASE_URL", openaiCompatProvider.WaferBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderWafer, "Wafer", "WAFER_API_KEY", waferBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClientWithModelIDMap(key, baseURL, upstreamIDsForProvider(providers.ProviderWafer), openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient)).
					WithProtectedHeaders(http.Header{"Wafer-ZDR": []string{"required"}})
			})
	}

	{
		waferAnthropicKey := ""
		if !byokOnly {
			waferAnthropicKey = config.GetOr(providers.APIKeyEnvVar(providers.ProviderWaferAnthropic), "")
		}
		waferMessagesBaseURL := anthropic.WaferMessagesBaseURL
		providerMap[providers.ProviderWaferAnthropic] = anthropic.NewClient(
			waferAnthropicKey, waferMessagesBaseURL,
			anthropic.WithAuthScheme(anthropic.AuthBearer),
			anthropic.WithModelListHTTPClient(discoveryHTTPClient)).
			WithProtectedHeaders(http.Header{"Wafer-ZDR": []string{"required"}}).
			WithModelIDMap(upstreamIDsForProvider(providers.ProviderWaferAnthropic))
		if waferAnthropicKey != "" {
			envKeyedProviders[providers.ProviderWaferAnthropic] = struct{}{}
			logger.Info("Wafer Anthropic provider enabled", "base_url", waferMessagesBaseURL)
		} else {
			logger.Info("Wafer Anthropic provider registered (BYOK only — set WAFER_API_KEY for deployment-level use)", "base_url", waferMessagesBaseURL)
		}
	}

	{
		bedrockRegion := config.GetOr("AWS_REGION", "us-east-1")
		bedrockBaseURL := config.GetOr("BEDROCK_BASE_URL", openaiCompatProvider.BedrockMantleBaseURL(bedrockRegion))
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderBedrock, "Bedrock", "AWS_BEARER_TOKEN_BEDROCK", bedrockBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return openaiCompatProvider.NewClientWithModelIDMap(key, baseURL, upstreamIDsForProvider(providers.ProviderBedrock), openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
			},
			"region", bedrockRegion)
	}

	{
		gatewayBaseURL := config.GetOr("ANTHROPIC_GATEWAY_BASE_URL", "")
		gatewayToken := ""
		if !byokOnly && gatewayBaseURL != "" {
			gatewayToken = config.GetOr(providers.APIKeyEnvVar(providers.ProviderAnthropicGateway), "")
		}
		providerMap[providers.ProviderAnthropicGateway] = anthropic.NewClient(
			gatewayToken, gatewayBaseURL,
			anthropic.WithAuthScheme(anthropic.AuthBearer),
			anthropic.WithModelListHTTPClient(discoveryHTTPClient))
		if gatewayToken != "" {
			envKeyedProviders[providers.ProviderAnthropicGateway] = struct{}{}
			logger.Info("Anthropic gateway provider enabled", "base_url", gatewayBaseURL)
		} else {
			logger.Info("Anthropic gateway provider registered (BYOK only — set ANTHROPIC_GATEWAY_TOKEN and ANTHROPIC_GATEWAY_BASE_URL for deployment-level use)")
		}
	}

	{
		gatewayBaseURL := config.GetOr("OPENAI_GATEWAY_BASE_URL", "")
		gatewayToken := ""
		if !byokOnly && gatewayBaseURL != "" {
			gatewayToken = config.GetOr(providers.APIKeyEnvVar(providers.ProviderOpenAIGateway), "")
		}
		providerMap[providers.ProviderOpenAIGateway] = openaiCompatProvider.NewGatewayClient(gatewayToken, gatewayBaseURL, openaiCompatProvider.WithModelListHTTPClient(discoveryHTTPClient))
		if gatewayToken != "" {
			envKeyedProviders[providers.ProviderOpenAIGateway] = struct{}{}
			logger.Info("OpenAI gateway provider enabled", "base_url", gatewayBaseURL)
		} else {
			logger.Info("OpenAI gateway provider registered (BYOK only — set OPENAI_GATEWAY_TOKEN and OPENAI_GATEWAY_BASE_URL for deployment-level use)")
		}
	}

	{
		googleBaseURL := config.GetOr("GOOGLE_BASE_URL", googleProvider.NativeBaseURL)
		registerDeploymentKeyedProvider(providerMap, envKeyedProviders, logger,
			providers.ProviderGoogle, "Google (Gemini) native", "GOOGLE_API_KEY", googleBaseURL, byokOnly,
			func(key, baseURL string) providers.Client {
				return googleProvider.NewNativeClient(key, baseURL)
			})
	}

	availableProviders := make(map[string]struct{}, len(providerMap))
	for name := range providerMap {
		availableProviders[name] = struct{}{}
	}
	inferenceExecutor, err := dispatch.NewExecutor(dispatch.NewClients(providerMap),
		dispatch.WithAttemptSink(proxy.NewAttemptSink(repo.Telemetry, logger)))
	if err != nil {
		panic(fmt.Sprintf("inference executor: %v", err))
	}

	registeredProviders := make([]string, 0, len(providerMap))
	for name := range providerMap {
		registeredProviders = append(registeredProviders, name)
	}
	if err := providers.ValidateDispatchable(registeredProviders); err != nil {
		logger.Error("Registered provider missing a translation family; refusing to boot", "err", err)
		panic(err)
	}

	admissionDecisions, err := buildAdmissionDecisions(pool)
	if err != nil {
		panic(err)
	}
	cache := auth.NewLRUAPIKeyCache(10000, 50000, 5*time.Minute, 60*time.Second)
	userCache := auth.NewLRUUserCache(50000, 10*time.Minute)
	userClusterCache := auth.NewLRUUserClusterListCache(50000, 5*time.Minute)
	blindExperimentCache := auth.NewLRUBlindExperimentCache(50000, 5*time.Minute, time.Now)
	routingPolicyCache := auth.NewRoutingPolicyCache(5 * time.Minute)

	pubsubProjectID := config.MustGet("PUBSUB_PROJECT_ID")
	pubsubTopicID := config.MustGet("PUBSUB_TOPIC_ROUTER_INVALIDATION")
	pubsubSubscriptionPrefix := config.MustGet("PUBSUB_SUBSCRIPTION_ROUTER_INVALIDATION")
	pubsubClient, err := gcppubsub.NewClient(context.Background(), pubsubProjectID)
	if err != nil {
		logger.Error("Failed to create Pub/Sub client", "err", err)
		panic(err)
	}
	defer func() { _ = pubsubClient.Close() }()

	publisher := pubsubClient.Publisher(pubsubTopicID)
	notifier := routerpubsub.NewInvalidationNotifier(publisher)
	defer notifier.Stop()

	if billingSvc != nil {
		if autopayTopicID := config.GetOr("PUBSUB_TOPIC_ROUTER_AUTOPAY", ""); autopayTopicID != "" {
			autopayNotifier := routerpubsub.NewAutopayNotifier(pubsubClient.Publisher(autopayTopicID))
			defer autopayNotifier.Stop()
			billingSvc = billingSvc.WithAutopayNotifier(autopayNotifier)
			logger.Info("Autopay recharge signalling enabled", "topic", autopayTopicID)
		}
	}

	flagOverridesDisabled := config.GetOr("ROUTER_FLAG_OVERRIDES_DISABLED", "false") == "true"
	if flagOverridesDisabled {
		logger.Info("Per-organization flag overrides disabled deployment-wide (ROUTER_FLAG_OVERRIDES_DISABLED=true)")
	}

	authSvc := auth.NewService(repo.Installations, repo.APIKeys, repo.ExternalAPIKeys, repo.Users, cache, userCache, time.Now).
		WithCredentialSubjectLookup(postgres.NewCredentialSubjectRepo(pool)).
		WithEncryptor(encryptor).
		WithInstallationChangeNotifier(notifier).
		WithClusterModelLists(repo.ClusterModelLists).
		WithUserClusterModelLists(repo.UserClusterModelLists, userClusterCache).
		WithBlindExperiments(repo.BlindExperiments, blindExperimentCache).
		WithBlindExperimentSessions(repo.BlindExperimentSessions).
		WithRoutingPolicies(repo.RoutingPolicies, routingPolicyCache).
		WithWIFTokenSource(buildWIFTokenSource(logger)).
		WithAnthropicFederationTokenSource(buildAnthropicFederationTokenSource(deploymentMode)).
		WithEntraTokenSource(buildEntraTokenSource(logger)).
		WithFlagOverridesDisabled(flagOverridesDisabled).
		WithSessionSpendCaps(billingSvc != nil)
	subscriptionPoolsEnabled := config.GetOr("ROUTER_SUBSCRIPTION_POOLS_ENABLED", "false") == "true"
	subscriptionRuntime, subscriptionLogins := buildSubscriptionRuntime(subscriptionPoolsEnabled, authSvc, repo.SubscriptionAccounts, subscriptionQuotaProber(providerMap[providers.ProviderOpenAI]), logger)

	subCtx, subCancel := context.WithTimeout(context.Background(), 30*time.Second)
	subscriptionName, deleteSubscription, err := routerpubsub.CreateReplicaSubscription(
		subCtx, pubsubClient, pubsubProjectID, pubsubTopicID, pubsubSubscriptionPrefix,
	)
	subCancel()
	if err != nil {
		logger.Error("Failed to create per-replica invalidation subscription", "err", err)
		panic(err)
	}
	defer deleteSubscription()
	logger.Info("Created per-replica invalidation subscription", "subscription", subscriptionName)

	invalidators := []auth.InstallationInvalidator{cache, userClusterCache, blindExperimentCache, routingPolicyCache}
	if admissionDecisions != nil {
		invalidators = append(invalidators, admissionDecisions)
	}
	listener := routerpubsub.NewInvalidationListener(pubsubClient.Subscriber(subscriptionName), invalidators...)
	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	defer func() {
		listenerCancel()
		listener.Wait()
	}()
	safeGo(logger, "invalidation-listener", func() { listener.Run(listenerCtx) })

	if deploymentMode == server.DeploymentModeSelfHosted {
		adminPassword := config.GetOr("ROUTER_ADMIN_PASSWORD", "")
		if adminPassword == "" {
			logger.Warn("ROUTER_ADMIN_PASSWORD not set; dashboard administration is disabled. Set ROUTER_ADMIN_PASSWORD to enable it.")
		}
		authSvc.WithAdminPassword(adminPassword)
	}
	escapeNormalize := config.GetOr("ROUTER_DEEPSEEK_ESCAPE_NORMALIZE", "false") == "true"
	if escapeNormalize {
		logger.Info("Edit-tool escape-sequence repair enabled (ROUTER_DEEPSEEK_ESCAPE_NORMALIZE=true)")
	}
	emitter, err := buildOtelEmitter(string(deploymentMode))
	if err != nil {
		logger.Error("Failed to create OTel emitter", "err", err)
		panic(err)
	}
	var telemetryEmitter telemetry.Emitter
	if emitter != nil {
		telemetryEmitter = emitter
		authSvc.WithOnboardingObserver(otel.NewOnboardingObserver(emitter))
	}

	var pinStore sessionpin.Store
	if config.GetOr("ROUTER_SESSION_PIN_ENABLED", "true") == "true" {
		pinStore = sessionpin.GatedByFlag(postgres.NewSessionPinRepo(pool, hmm.SessionPinColumns()))
		safeGo(logger, "session-pin-sweep", func() { runSessionPinSweep(context.Background(), pinStore) })
		logger.Info("Session pin store enabled (sliding 1h TTL, hourly sweep)")
	}

	sessionTurnClock := postgres.NewSessionTurnClockRepo(pool)
	safeGo(logger, "session-turn-clock-sweep", func() { runSessionTurnClockSweep(context.Background(), sessionTurnClock) })

	hardPinExplore := config.GetOr("ROUTER_HARD_PIN_EXPLORE", "true") == "true"
	hardPinOverrideConfigured := strings.TrimSpace(config.GetOr("ROUTER_HARD_PIN_MODEL", "")) != ""
	hardPinProvider, hardPinModel, err := resolveHardPinModel()
	if err != nil {
		logger.Error("Invalid hard-pin model configuration; refusing to boot", "err", err)
		panic(err)
	}
	if hardPinExplore {
		logger.Info("Explore sub-agent hard-pin enabled", "provider", hardPinProvider, "model", hardPinModel)
	}
	logger.Info("Hard-pin model resolved", "provider", hardPinProvider, "model", hardPinModel, "byok_only", byokOnly)

	hardPinResolver := selectHardPinResolver(logger, hardPinOverrideConfigured, byokOnly, hardPinModel)

	subAgentProvider := config.GetOr("ROUTER_SUBAGENT_PROVIDER", "")
	subAgentModel := config.GetOr("ROUTER_SUBAGENT_MODEL", "")
	switch {
	case subAgentModel != "" && subAgentProvider == "":
		err := errors.New("ROUTER_SUBAGENT_MODEL requires ROUTER_SUBAGENT_PROVIDER")
		logger.Error("Invalid sub-agent routing override; refusing to boot", "err", err)
		panic(err)
	case subAgentProvider != "" && subAgentModel == "":
		err := errors.New("ROUTER_SUBAGENT_PROVIDER requires ROUTER_SUBAGENT_MODEL")
		logger.Error("Invalid sub-agent routing override; refusing to boot", "err", err)
		panic(err)
	case subAgentModel != "":
		logger.Info("Sub-agent routing override enabled", "provider", subAgentProvider, "model", subAgentModel)
	}

	deploymentEligible := make(map[string]struct{}, len(envKeyedProviders))
	for p := range envKeyedProviders {
		deploymentEligible[p] = struct{}{}
	}

	passthroughEligible := make(map[string]struct{}, 2)
	if anthropicPassthroughEligible {
		passthroughEligible[providers.ProviderAnthropic] = struct{}{}
	}
	if openaiPassthroughEligible {
		passthroughEligible[providers.ProviderOpenAI] = struct{}{}
	}

	continuityConfig := continuityConfigFromEnv()
	cyberRefusalRepin := config.GetOr("ROUTER_CYBER_REFUSAL_REPIN", "true") == "true"
	cyberRefusalRetry := config.GetOr("ROUTER_CYBER_REFUSAL_RETRY", "true") == "true"
	cyberRefusalFallbackModel := config.GetOr("ROUTER_CYBER_REFUSAL_FALLBACK_MODEL", "claude-sonnet-5")
	anthropicServerSideFallback := config.GetOr("ROUTER_ANTHROPIC_SERVER_SIDE_FALLBACK", "true") == "true"
	scopedSearchRequirement := config.GetOr("ROUTER_SCOPED_SEARCH_REQUIREMENT", "true") == "true"
	searchRequirementDecayTurns := parseEnvInt("ROUTER_SEARCH_REQUIREMENT_DECAY_TURNS", ingress.DefaultSearchRequirementDecayTurns)
	effortEscalation := config.GetOr("ROUTER_EFFORT_ESCALATION", "false") == "true"
	siblingFailover := config.GetOr("ROUTER_SIBLING_FAILOVER", "true") == "true"
	openAIResponsesBroad := config.GetOr("ROUTER_OPENAI_RESPONSES_BROAD", "true") == "true"
	allowedModelsHeader := config.GetOr("ROUTER_ALLOWED_MODELS_HEADER", "false") == "true"
	committedStreamArmDemotion := config.GetOr("ROUTER_COMMITTED_STREAM_ARM_DEMOTION", "false") == "true"
	rescuedFailureArmDemotion := config.GetOr("ROUTER_RESCUED_FAILURE_ARM_DEMOTION", "false") == "true"
	transientRateLimit := config.GetOr("ROUTER_TRANSIENT_RATE_LIMIT", "false") == "true"
	rateLimitCooldownSeconds := parseEnvInt("ROUTER_RATE_LIMIT_COOLDOWN_SECONDS", proxy.DefaultRateLimitCooldownSeconds)
	providerOverloaded := config.GetOr("ROUTER_PROVIDER_OVERLOADED", "true") == "true"
	nativeAnthropicResponseSignals := config.GetOr("ROUTER_NATIVE_ANTHROPIC_RESPONSE_SIGNALS", "true") == "true"
	nativeOpenAIResponseSignals := config.GetOr("ROUTER_NATIVE_OPENAI_RESPONSE_SIGNALS", "true") == "true"
	ccOrchToolsCrossVendor := config.GetOr("ROUTER_CC_ORCH_TOOLS_CROSSVENDOR", "true") == "true"
	ccTaskToolsCrossVendor := config.GetOr("ROUTER_CC_TASK_TOOLS_CROSSVENDOR", "false") == "true"
	ccAutonomySystemAppend := config.GetOr("ROUTER_CC_AUTONOMY_SYSTEM_APPEND", "false") == "true"
	earlyStopRedo, err := flags.ParseEarlyStopRedoMode(config.GetOr("ROUTER_EARLY_STOP_REDO", string(flags.EarlyStopRedoOff)))
	if err != nil {
		logger.Error("Invalid ROUTER_EARLY_STOP_REDO; refusing to boot", "err", err)
		panic(err)
	}
	ccWorkspaceSystemAppend := config.GetOr("ROUTER_CC_WORKSPACE_SYSTEM_APPEND", "false") == "true"
	bandSwapEnabled := config.GetOr("ROUTER_BAND_SWAP", "false") == "true"
	turnSignalCaptureEnabled := config.GetOr("ROUTER_TURN_SIGNAL_CAPTURE_ENABLED", "true") == "true"

	prefixTrimFreeSwitch := config.GetOr("ROUTER_PREFIX_TRIM_FREE_SWITCH", "true") == "true"
	hmmConfig, err := hmmConfigFromEnv()
	if err != nil {
		logger.Error("Invalid HMM configuration; refusing to boot", "err", err)
		panic(err)
	}
	policyDeadlineFallback := config.GetOr("ROUTER_POLICY_DEADLINE_FALLBACK", "false") == "true"
	policyDeadlineDefaultModel := config.GetOr("ROUTER_POLICY_DEADLINE_DEFAULT_MODEL", "")
	handoverProviderName := config.GetOr("ROUTER_HANDOVER_PROVIDER", providers.ProviderAnthropic)
	handoverModel := config.GetOr("ROUTER_HANDOVER_MODEL", policy.HandoverSummaryDefaultModel)
	handoverTimeout := parseEnvDurationMs("ROUTER_HANDOVER_TIMEOUT_MS", proxy.DefaultHandoverTimeout)
	compactionModel, err := resolveCompactionModel(handoverProviderName)
	if err != nil {
		logger.Error("Invalid compaction model configuration; refusing to boot", "err", err)
		panic(err)
	}

	subAgentPolicyProvider := subAgentProvider
	subAgentPolicyModel := subAgentModel
	if subAgentPolicyModel == "" {
		subAgentPolicyProvider = hardPinProvider
		subAgentPolicyModel = hardPinModel
	}
	clientCompactionProvider, clientCompactionModel := providers.ProviderAnthropic, compactionModel
	if hardPinOverrideConfigured {
		clientCompactionProvider, clientCompactionModel = hardPinProvider, hardPinModel
	}
	deploymentTargets := []policy.PurposeTargetOverride{
		{Purpose: policy.PurposeHandoverSummary, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: handoverModel, Provider: handoverProviderName}},
		{Purpose: policy.PurposePrecompactionSummary, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: compactionModel, Provider: handoverProviderName}},
		{Purpose: policy.PurposeTitleGeneration, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: hardPinModel, Provider: hardPinProvider}},
		{Purpose: policy.PurposeClassifier, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: hardPinModel, Provider: hardPinProvider}},
		{Purpose: policy.PurposeProbe, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: hardPinModel, Provider: hardPinProvider}},
		{Purpose: policy.PurposeSubAgentDispatch, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: subAgentPolicyModel, Provider: subAgentPolicyProvider}},
		{Purpose: policy.PurposeClientCompaction, Target: policy.TargetOverride{Source: policy.OverrideSourceDeployment, CatalogID: clientCompactionModel, Provider: clientCompactionProvider}},
	}
	inferenceDeployment := policy.DeploymentPolicyConfig{
		AvailableProviders: availableProviders,
		TargetOverrides:    deploymentTargets,
	}
	escalationJudgeEnabled, err := strconv.ParseBool(config.GetOr("ROUTER_ESCALATION_JUDGE_ENABLED", "false"))
	if err != nil {
		logger.Error("Invalid escalation judge enablement", "err", err)
		panic(err)
	}
	escalationJudgeKey := strings.TrimSpace(os.Getenv("FIREWORKS_API_KEY"))
	escalationJudgeActiveEnabled, err := strconv.ParseBool(config.GetOr("ROUTER_ESCALATION_JUDGE_ACTIVE_ENABLED", "false"))
	if err != nil {
		logger.Error("Invalid escalation judge active rollout enablement", "err", err)
		panic(err)
	}
	escalationShadowDefault := flags.EscalationClassifier(config.GetOr("ROUTER_ESCALATION_SHADOW_CLASSIFIER_DEFAULT", string(flags.EscalationClassifierNone)))
	if escalationShadowDefault != flags.EscalationClassifierNone && !flags.IsLLMEscalationClassifier(escalationShadowDefault) {
		panic("ROUTER_ESCALATION_SHADOW_CLASSIFIER_DEFAULT must be none, llm_escalation or switchyard_llm_v1")
	}
	if escalationJudgeActiveEnabled && !escalationJudgeEnabled {
		panic("ROUTER_ESCALATION_JUDGE_ACTIVE_ENABLED requires ROUTER_ESCALATION_JUDGE_ENABLED")
	}
	if escalationJudgeEnabled {
		if escalationJudgeKey == "" {
			panic("FIREWORKS_API_KEY is required when the escalation judge is enabled")
		}
		inferenceDeployment.EnabledOptionalPurposes = map[policy.Purpose]bool{policy.PurposeEscalationJudge: true}
	}
	if err := policy.DefaultRegistry().ValidateDeployment(inferenceDeployment); err != nil {
		logger.Error("Invalid inference policy deployment; refusing to boot", "err", err)
		panic(err)
	}

	routingTargets := catalog.RoutingTargetSet(availableProviders)
	logger.Info("Catalog routing targets resolved", "catalog_routing_targets", len(routingTargets))

	auxiliaryTargets := policy.DefaultRegistry().FixedCatalogTargetSet(availableProviders)
	for id := range routingTargets {
		auxiliaryTargets[id] = struct{}{}
	}
	inferencePlans, err := policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
		auxiliaryTargets, availableProviders, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
	if err != nil {
		panic(fmt.Sprintf("inference plan resolver: %v", err))
	}
	var summarizer handover.Summarizer
	if _, ok := providerMap[handoverProviderName]; ok {
		summarizer = proxy.NewProviderSummarizer(inferencePlans, inferenceExecutor, handoverProviderName, handoverModel, handoverTimeout)
		logger.Info("Handover summarizer wired", "provider", handoverProviderName, "model", handoverModel, "timeout_ms", handoverTimeout.Milliseconds())
	} else {
		logger.Info("Handover summarizer disabled (provider not registered); switch turns will preserve full history instead", "requested_provider", handoverProviderName)
	}

	captureMode := contentCaptureModeFromEnv()
	captureMaxBytes := parseEnvInt("WV_CAPTURE_MAX_BYTES", 1<<20)
	logger.Info("Router content capture configured", "mode", captureMode.String(), "max_bytes", captureMaxBytes)

	feedbackLinks := feedbackLinksFromEnv(repo.Feedback, telemetryEmitter, logger)

	var escalationObserver escalation.Observer
	var hmmRouter router.Router
	var hmmEmbeddingRouter router.Router
	var hmmBetaRouter router.Router
	var hmmQwenRouter router.Router
	var hmmCapabilities policy.Capabilities
	var hmmReadinessChecker admin.HealthChecker
	var policyStatus admin.PolicyStatusSource
	var hmmRosterSources map[router.Strategy]policy.RosterSource
	var hmmRosterModels admin.HMMRosterSource
	var routingDistribution admin.DistributionRosterSource
	var hmmBetaCapabilities policy.Capabilities
	var servingAdmission *middleware.ServingAdmissionConfig
	testPlansEnabled := strings.EqualFold(config.GetOr("ROUTER_TEST_PLANS_ENABLED", "false"), "true")
	policyEnvironmentRaw := strings.TrimSpace(config.GetOr("ROUTER_POLICY_ENVIRONMENT", ""))
	policyReleaseDir := strings.TrimSpace(config.GetOr("ROUTER_POLICY_RELEASE_DIR", ""))
	taskRuntime, taskErr := loadTaskDomainRuntime(pool)
	if taskErr != nil {
		logger.Error("Task classifier configuration invalid; refusing to boot", "err", taskErr)
		panic(taskErr)
	}
	if taskRuntime != nil {
		taskSweepCtx, cancelTaskSweep := context.WithCancel(context.Background())
		defer cancelTaskSweep()
		safeGo(logger, "task-domain-sweep", func() { taskRuntime.sweep(taskSweepCtx) })
	}
	if servingTargetConfigured() {
		prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), 60*time.Second)
		admission, baseline, closeRegistry, err := buildManagedServingRuntime(prepareCtx, deploymentMode, availableProviders, taskRuntime)
		cancelPrepare()
		if err != nil {
			logger.Error("Managed worker preparation failed; refusing to boot", "target", config.GetOr("ROUTER_SERVING_TARGET", ""), "registry_uri", config.GetOr("ROUTER_SERVING_REGISTRY_URI", ""), "err", err)
			panic(err)
		}
		defer closeRegistry()
		servingAdmission = admission
		servingAdmission.Decisions = admissionDecisions
		if testPlansEnabled {
			servingAdmission.TestPlans = &policyregistry.TestPlanTools{Repository: servingpostgres.NewTestPlanRepo(pool), Store: admission.Store, Clock: time.Now}
		}
		servingAdmission.TestBudgetEnabled = testPlansEnabled && billingSvc != nil
		servingAdmission.Attribution = servingpostgres.NewRequestAttributionRepo(pool)
		servingAdmission.Experiments = authSvc
		admittedRouter := policyregistry.NewAdmittedRouter(router.StrategyHMM, baseline)
		hmmRouter = admittedRouter
		hmmEmbeddingRouter = policyregistry.NewAdmittedRouter(router.StrategyHMMEmbedding, baseline)
		if admission.Identity.Target == policyregistry.TargetInternal {
			hmmBetaRouter = policyregistry.NewAdmittedRouter(router.StrategyHMMBeta, baseline)
			hmmQwenRouter = policyregistry.NewAdmittedRouter(router.StrategyHMMQwen, baseline)
		}
		hmmCapabilities = admittedRouter.CurrentCapabilities()
		escalationObserver = admittedRouter
		rosterSource := policyregistry.AdmittedRosterSource{}
		hmmRosterSources = map[router.Strategy]policy.RosterSource{
			router.StrategyHMM: rosterSource, router.StrategyHMMEmbedding: rosterSource,
		}
		if admission.Identity.Target == policyregistry.TargetInternal {
			hmmRosterSources[router.StrategyHMMBeta] = rosterSource
			hmmRosterSources[router.StrategyHMMQwen] = rosterSource
		}
		if topic := strings.TrimSpace(config.GetOr("PUBSUB_TOPIC_ROUTER_POLICY_INVALIDATION", "")); topic != "" {
			subscriptionCtx, cancelSubscription := context.WithTimeout(context.Background(), 30*time.Second)
			subscriptionName, cleanup, err := routerpubsub.CreateReplicaSubscription(subscriptionCtx, pubsubClient, pubsubProjectID, topic, config.GetOr("PUBSUB_SUBSCRIPTION_ROUTER_POLICY_INVALIDATION", topic))
			cancelSubscription()
			if err != nil {
				logger.Warn("Admission release invalidation unavailable; TTL remains authoritative", "err", err)
			} else {
				defer cleanup()
				releaseListener := routerpubsub.NewPolicyReleaseListener(pubsubClient.Subscriber(subscriptionName), admissionDecisions.InvalidateAll)
				releaseCtx, cancelRelease := context.WithCancel(context.Background())
				defer func() { cancelRelease(); releaseListener.Wait() }()
				safeGo(logger, "admission-release-invalidation", func() { releaseListener.Run(releaseCtx) })
			}
		}
		logger.Info("Managed serving admission enabled", "target", admission.Identity.Target, "revision", admission.Identity.Revision)
	} else if policyEnvironmentRaw != "" || policyReleaseDir != "" {
		var policyRegistry policyregistry.Loader
		var policyEnvironment policyregistry.Environment
		policyLane := policyregistry.LaneStable
		defaultSidecarAuth := policySidecarAuthGoogleIDToken
		if policyReleaseDir != "" {
			directoryRegistry, registryErr := policyregistry.NewDirectoryRegistry(policyReleaseDir, config.GetOr("ROUTER_HMM_SIDECAR_URL", ""))
			if registryErr != nil {
				logger.Error("Local router policy release is invalid; refusing to boot", "dir", policyReleaseDir, "err", registryErr)
				panic(registryErr)
			}
			policyRegistry = directoryRegistry
			policyEnvironment = directoryRegistry.Environment()
			policyLane = directoryRegistry.Lane()
			defaultSidecarAuth = policySidecarAuthNone
		} else {
			gcsRegistry, registryErr := gcsregistry.NewGCSRegistry(context.Background(), strings.TrimSpace(config.GetOr("WEAVE_REGISTRY_URI", "gs://weave_ml/weave_registry")))
			if registryErr != nil {
				logger.Error("Router policy registry failed to initialize; refusing to boot", "err", registryErr)
				panic(registryErr)
			}
			defer func() { _ = gcsRegistry.Close() }()
			policyRegistry = gcsRegistry
			policyEnvironment = policyregistry.Environment(policyEnvironmentRaw)
		}
		hmmTimeout := parseEnvDurationMs("ROUTER_HMM_SIDECAR_TIMEOUT_MS", policyclient.DefaultTimeout)
		hmmAttemptTimeout := parseEnvAttemptTimeoutMs(policyclient.DeriveAttemptTimeout(hmmTimeout))
		hmmAuthMode := config.GetOr("ROUTER_HMM_SIDECAR_AUTH", defaultSidecarAuth)
		stableManager, managerErr := policyregistry.NewManager(
			policyRegistry,
			policyEnvironment,
			policyLane,
			hmmPolicySnapshotBuilder(availableProviders, []router.Strategy{router.StrategyHMM, router.StrategyHMMEmbedding}, hmmAuthMode, hmmTimeout, hmmAttemptTimeout),
			logger,
		)
		if managerErr != nil {
			logger.Error("Stable router policy manager is invalid; refusing to boot", "err", managerErr)
			panic(managerErr)
		}
		refreshCtx, cancelRefresh := context.WithTimeout(context.Background(), 30*time.Second)
		refreshErr := stableManager.Refresh(refreshCtx)
		cancelRefresh()
		if refreshErr != nil {
			logger.Error("No valid stable router policy snapshot; refusing to boot", "err", refreshErr)
			panic(refreshErr)
		}
		managerCtx, cancelManagers := context.WithCancel(context.Background())
		defer cancelManagers()
		safeGo(logger, "stable-policy-manager", func() { stableManager.Run(managerCtx) })

		stableDynamicRouter := policyregistry.NewDynamicRouter(stableManager, router.StrategyHMM)
		hmmRouter = stableDynamicRouter
		hmmEmbeddingRouter = policyregistry.NewDynamicRouter(stableManager, router.StrategyHMMEmbedding)
		hmmCapabilities = stableDynamicRouter.CurrentCapabilities()
		hmmReadinessChecker = stableManager
		policyStatus = stableManager
		hmmRosterSources = map[router.Strategy]policy.RosterSource{
			router.StrategyHMM:          stableManager,
			router.StrategyHMMEmbedding: stableManager,
		}
		hmmRosterModels = newHMMRosterSource(stableManager, hmmTimeout)
		routingDistribution = hmmRoutingDistribution(stableManager)
		escalationObserver = stableDynamicRouter

		logger.Info("Local-dev HMM policy release wired", "registry_uri", policyRegistry.RootURI(), "environment", policyEnvironment, "candidate_models", len(routingTargets))
	} else {
		logger.Info("HMM policy routers disabled (ROUTER_SERVING_TARGET, ROUTER_POLICY_ENVIRONMENT and ROUTER_POLICY_RELEASE_DIR unset); HMM strategies will return 503")
	}

	configuredPolicySpecs, err := configuredPolicySidecarsFromEnv(routingTargets, availableProviders, logger)
	if err != nil {
		logger.Error("Generic policy sidecar configuration is invalid", "err", err)
		panic(err)
	}

	publishFlagRegistry(logger, repo.FlagDefinitions, withRoutingSwitchDefaults(map[flags.Key]string{
		flags.KeyEscalationActiveClassifier:           string(flags.EscalationClassifierNone),
		flags.KeyEscalationShadowClassifier:           string(escalationShadowDefault),
		flags.KeyEscalationCadence:                    strconv.Itoa(llmescalation.DefaultCadence),
		flags.KeyEscalationEpoch:                      "0",
		flags.KeyEscalationXGBoostEnabled:             boolDefault(false),
		flags.KeyEscalationXGBoostShadowEnabled:       boolDefault(false),
		flags.KeyEscalationXGBoostShadowMarkerEnabled: boolDefault(false),
		flags.KeyEscalationXGBoostEpoch:               "0",
		flags.KeySubscriptionPlanAwareRouting:         boolDefault(false),
		flags.KeyClaudeEnterpriseOnly:                 boolDefault(true),
		flags.KeySubscriptionExhaustedOpenRouterOnly:  boolDefault(false),
		flags.KeyPlanUserClusterLists:                 boolDefault(false),
		flags.KeyTurnSignalCapture:                    boolDefault(turnSignalCaptureEnabled),
		flags.KeyPlannerEnabled:                       boolDefault(continuityConfig.PlannerEnabled),
		flags.KeyScoreToolResultTurns:                 boolDefault(continuityConfig.ScoreToolResultTurns),
		flags.KeyHMMToolResultHold:                    boolDefault(false),
		flags.KeyPrefixTrimFreeSwitch:                 boolDefault(prefixTrimFreeSwitch),
		flags.KeyAuthoritativeUpgradeGate:             boolDefault(hmmConfig.UpgradeGate),
		flags.KeyAuthoritativeUpgradePolicy:           string(hmmConfig.UpgradePolicy),
		flags.KeyAuthoritativeUpgradeVotes:            strconv.Itoa(hmmConfig.UpgradeVotes),
		flags.KeyAuthoritativeDowngradeGate:           boolDefault(hmmConfig.DowngradeGate),
		flags.KeyAuthoritativeUpgradeSlidingThreshold: boolDefault(hmmConfig.UpgradeSlidingThreshold),
		flags.KeyHMMDowngradeHysteresisTurns:          strconv.Itoa(hmmConfig.DowngradeHysteresisTurns),
		flags.KeyHMMDowngradeHysteresisShadowTurns:    strconv.Itoa(hmmConfig.DowngradeHysteresisShadowTurns),
		flags.KeyAuthorityCacheShadow:                 boolDefault(hmmConfig.AuthorityCacheShadow),
		flags.KeySwitchDecideShadow:                   boolDefault(true),
		flags.KeyAgreementShadowEpoch:                 "0",
		flags.KeySiblingFailover:                      boolDefault(siblingFailover),
		flags.KeyOpenAIResponsesBroad:                 boolDefault(openAIResponsesBroad),
		flags.KeyAllowedModelsHeader:                  boolDefault(allowedModelsHeader),
		flags.KeyCCTaskToolsCrossVendor:               boolDefault(ccTaskToolsCrossVendor),
		flags.KeyCCAutonomySystemAppend:               boolDefault(ccAutonomySystemAppend),
		flags.KeyEarlyStopRedo:                        string(earlyStopRedo),
		flags.KeyCCWorkspaceSystemAppend:              boolDefault(ccWorkspaceSystemAppend),
		flags.KeyCommittedStreamArmDemotion:           boolDefault(committedStreamArmDemotion),
		flags.KeyRescuedFailureArmDemotion:            boolDefault(rescuedFailureArmDemotion),
		flags.KeyTransientRateLimit:                   boolDefault(transientRateLimit),
		flags.KeyRateLimitCooldownSeconds:             strconv.Itoa(rateLimitCooldownSeconds),
		flags.KeyProviderOverloaded:                   boolDefault(providerOverloaded),
		flags.KeyNativeAnthropicResponseSignals:       boolDefault(nativeAnthropicResponseSignals),
		flags.KeyNativeOpenAIResponseSignals:          boolDefault(nativeOpenAIResponseSignals),
		flags.KeyEffortEscalation:                     boolDefault(effortEscalation),
		flags.KeyCyberRefusalRepin:                    boolDefault(cyberRefusalRepin),
		flags.KeyCyberRefusalRetry:                    boolDefault(cyberRefusalRetry),
		flags.KeyCyberRefusalFallback:                 cyberRefusalFallbackModel,
		flags.KeyAnthropicServerFallback:              boolDefault(anthropicServerSideFallback),
	}, hardPinExplore, ccOrchToolsCrossVendor, escapeNormalize, continuityConfig.Planner))

	escalationStore := postgres.NewEscalationRepo(pool)
	escalationDashboardStore := postgres.NewEscalationDashboardRepo(pool)
	safeGo(logger, "escalation-state-sweep", func() { runEscalationSweep(context.Background(), escalationStore) })
	llmEscalationStore := postgres.NewLLMEscalationRepo(pool)
	safeGo(logger, "llm-escalation-state-sweep", func() { runEscalationSweep(context.Background(), llmEscalationStore) })
	escalationJudge := newSwitchyardEscalationJudge(escalationJudgeEnabled, inferencePlans, inferenceExecutor, escalationJudgeKey)
	qwenEscalationURL := strings.TrimSpace(os.Getenv("ROUTER_LLM_ESCALATION_URL"))
	qwenEscalationKey := strings.TrimSpace(os.Getenv("ROUTER_LLM_ESCALATION_API_KEY"))
	var qwenEscalationJudge llmescalation.Judge
	if qwenEscalationURL != "" || qwenEscalationKey != "" {
		judge, judgeErr := escalationmodal.NewJudge(qwenEscalationURL, qwenEscalationKey, nil)
		if judgeErr != nil {
			panic(judgeErr)
		}
		qwenEscalationJudge = judge
	}
	if (escalationShadowDefault == flags.EscalationClassifierSwitchyard && !escalationJudgeEnabled) ||
		(escalationShadowDefault == flags.EscalationClassifierLLM && qwenEscalationJudge == nil) {
		panic("ROUTER_ESCALATION_SHADOW_CLASSIFIER_DEFAULT names a classifier whose judge is not configured")
	}
	servedModels := proxyRoutableModels(routingTargets, availableProviders, hmmRouter != nil)
	classifierSessions, classifierSpecs, err := configureAtomicClassifier(pool, availableProviders)
	if err != nil {
		logger.Error("Failed to configure atomic classifier", "err", err)
		panic(err)
	}
	excludedModels := excludedModelsFromEnv()
	inferenceDeployment.RoutableModels = servedModels

	ingressFormats := ingressFormatsFor(allowedModelsHeader, scopedSearchRequirement, searchRequirementDecayTurns, cortexWebSearch(logger))
	ingressFormats.OpenAIResponsesBroad, ingressFormats.EscapeNormalize = openAIResponsesBroad, escapeNormalize
	ingressFormats.CCOrchToolsCrossVendor, ingressFormats.CCTaskToolsCrossVendor = ccOrchToolsCrossVendor, ccTaskToolsCrossVendor
	ingressFormats.CCAutonomySystemAppend, ingressFormats.CCWorkspaceSystemAppend = ccAutonomySystemAppend, ccWorkspaceSystemAppend
	seatUsage := postgres.NewSeatUsageRepo(pool)
	usageObserver := newUsageObserver(logger, seatUsage)
	if subscriptionRuntime != nil {
		subscriptionRuntime.WithCandidateSignal(func(accountID string, _ subscriptions.Provider, now time.Time) subscriptions.CandidateSignal {
			snapshot, observed := usageObserver.Snapshot(usageObserver.Key([]byte("subscription-account:" + accountID)))
			if !observed {
				return subscriptions.CandidateSignal{}
			}
			return subscriptionCandidateSignal(snapshot, now)
		})
	}
	proxySvc := proxy.WithDeps(proxy.WithStrategyPorts(proxy.NewService(nil, dispatch.NewClients(providerMap), telemetryEmitter, pinStore, hardPinExplore, hardPinProvider, hardPinModel, repo.Telemetry), hmm.ServingPorts()), proxy.Deps{
		Telemetry: proxy.TelemetryDeps{Clock: time.Now, SessionTurnClock: sessionTurnClock, ContentCapture: captureMode, ContentCaptureMaxBytes: captureMaxBytes, Billing: billingSvc, Flags: &proxy.TelemetryFlags{
			TurnSignalCapture: turnSignalCaptureEnabled, NativeAnthropicResponseSignals: nativeAnthropicResponseSignals, NativeOpenAIResponseSignals: nativeOpenAIResponseSignals}},
		Conversation: conversation.Config{Summarizer: summarizer, RouterFeedbackStore: repo.Telemetry,
			FeedbackLinks: feedbackLinks, PiHandoffSecret: config.GetOr("ROUTER_PI_HANDOFF_SECRET", "")},
		Routing: &proxy.RoutingDeps{PrefixTrimFreeSwitch: &prefixTrimFreeSwitch, EffortEscalation: effortEscalation, AvailableModels: servedModels, DefaultBaselineModel: resolveDefaultBaselineModel(), ExcludedModels: excludedModels,
			HardPinResolver: hardPinResolver, ExplicitUtilityHardPin: hardPinOverrideConfigured, SubAgentProvider: subAgentProvider, SubAgentModel: subAgentModel,
			Deadline: routingpolicy.PolicyDeadline{Fallback: policyDeadlineFallback, DefaultModel: policyDeadlineDefaultModel}, GlobalAutomaticExclusions: repo.GlobalAutomaticExclusions,
			Plans: inferencePlans, Continuity: &continuityConfig, HMM: &hmmConfig, BandSwap: bandSwapEnabled, ClassifierSessions: classifierSessions, RecipeShadow: recipeShadowFor(pool, servingAdmission, availableProviders),
			Strategies: append(append([]policy.StrategySpec{
				{Strategy: router.StrategyHMM, Router: hmmRouter, Unavailable: hmm.ErrHMMUnavailable, Capabilities: hmmCapabilities},
				{Strategy: router.StrategyHMMEmbedding, Router: hmmEmbeddingRouter, Unavailable: hmm.ErrHMMUnavailable, Capabilities: hmmCapabilities},
				{Strategy: router.StrategyHMMBeta, Router: hmmBetaRouter, Unavailable: hmm.ErrHMMUnavailable, Capabilities: hmmBetaCapabilities},
				{Strategy: router.StrategyHMMQwen, Router: hmmQwenRouter, Unavailable: hmm.ErrHMMUnavailable, Capabilities: hmmCapabilities},
				{Strategy: router.StrategyDecisionModel, Router: buildDecisionModelRouter(logger), Unavailable: decisionmodel.ErrUnavailable, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}},
			}, classifierSpecs...), configuredPolicySpecs...),
			Escalation: &proxy.RoutingEscalation{Store: escalationStore, Observer: escalationObserver, LLMStore: llmEscalationStore, LLMJudge: escalationJudge, QwenJudge: qwenEscalationJudge, ShadowDefault: escalationShadowDefault}},
		Ingress: &ingressFormats, Escalation: proxy.EscalationDeps{SwitchyardActive: escalationJudgeActiveEnabled}, BYOKOnly: byokOnly, DeploymentKeyedProviders: deploymentEligible,
		Credentials: &credentials.Brokerage{ExternalKeyUsage: authSvc, UsageObserver: usageObserver, ClaudePlans: subscriptions.NewPlanResolver(anthropicClient, time.Now), CodexExtraCredits: allowCodexCredits,
			ManagedSubscriptions: managedSubscriptionLeaser(subscriptionRuntime), PassthroughEligible: passthroughEligible, ExcludedProvidersOverride: excludedProvidersFromEnv(logger)},
		Dispatch: &dispatch.Config{
			Rescue: dispatch.RescueConfig{SiblingFailover: siblingFailover, EarlyStopRedo: earlyStopRedo, CyberRefusalRepin: cyberRefusalRepin, CyberRefusalRetry: cyberRefusalRetry,
				CyberRefusalFallbackModel: cyberRefusalFallbackModel, AnthropicServerSideFallback: anthropicServerSideFallback},
			ArmDemotion: dispatch.ArmDemotionPolicy{CommittedStream: committedStreamArmDemotion, RescuedFailure: rescuedFailureArmDemotion},
			Throttling: dispatch.Throttling{Overload: dispatch.NewOverloadBreaker(providerOverloaded, breaker.New(breaker.Defaults(), nil)),
				RateLimit: dispatch.NewRateLimitPolicy(transientRateLimit, rateLimitCooldownSeconds)},
			Executor: inferenceExecutor, Plans: inferencePlans, Deployment: inferenceDeployment,
			TranslationCompatibilityMode: dispatch.TranslationCompatibilityMode(translationCompatibilityMode), TranslationMetrics: otel.NewTranslationMetrics()},
	})
	for _, spec := range configuredPolicySpecs {
		logger.Info("Generic policy sidecar wired", "strategy", spec.Strategy, "candidate_models", len(routingTargets))
	}
	logger.Info("Effort escalation configured", "enabled", effortEscalation)
	logger.Info("Cross-vendor Claude Code orchestration tools configured", "enabled", ccOrchToolsCrossVendor, "task_tools_enabled", ccTaskToolsCrossVendor)
	logger.Info("Turn signal capture configured", "enabled", turnSignalCaptureEnabled)
	logger.Info("Planner configured", "enabled", continuityConfig.PlannerEnabled, "threshold_usd", continuityConfig.Planner.ThresholdUSD, "expected_remaining_turns", continuityConfig.Planner.ExpectedRemainingTurns, "tier_upgrade_enabled", continuityConfig.Planner.TierUpgradeEnabled, "corrected_economics", continuityConfig.Planner.CorrectedEconomics, "prefix_trim_free_switch", prefixTrimFreeSwitch, "routing_targets_count", len(routingTargets))
	logger.Info("Tool-result scoring configured", "enabled", continuityConfig.ScoreToolResultTurns)

	if continuityConfig.Planner.TierUpgradeEnabled && len(routingTargets) > 0 {
		deployed := make([]string, 0, len(routingTargets))
		for m := range routingTargets {
			deployed = append(deployed, m)
		}
		if err := catalog.ValidateDeployed(deployed); err != nil {
			logger.Error("Capability tier table incomplete; refusing to start with tier guard enabled", "err", err)
			panic(err)
		}
	}

	if excludedModels != nil {
		logger.Info("Model exclusion override active", "excluded_models", excludedModels)
	}

	trafficCapture, err := newTrafficCaptureFromEnvironment()
	if err != nil {
		logger.Error("Unable to initialize local HTTP traffic capture", "err", err)
		panic(err)
	}
	if trafficCapture != nil {
		logger.Warn("Local HTTP traffic capture enabled; bodies may contain sensitive conversation content", "file", trafficCapture.file.Name(), "sensitive_headers_included", trafficCapture.includeSensitive)
		defer func() {
			if err := trafficCapture.Close(); err != nil {
				logger.Error("Unable to close local HTTP traffic capture", "err", err)
			}
		}()
	}

	engine := gin.New()
	engine.UnescapePathValues = true
	engine.UseRawPath = true
	engine.Use(
		apm.Middleware(),
		observability.Middleware(),
		observability.AccessLog(),
	)

	analyticsSvc := analytics.NewService(repo.Analytics, time.Now)
	defaultStrategy, defaultStrategyErr := defaultStrategyFromEnv()
	if defaultStrategyErr != nil {
		logger.Error("ROUTER_DEFAULT_STRATEGY is invalid; refusing to boot", "err", defaultStrategyErr)
		panic(defaultStrategyErr)
	}
	readinessChecker := newReadinessChecker(pool, hmmReadinessChecker, proxy.NewRoutingCatalog(proxySvc), defaultStrategy)
	if defaultStrategyErr := readinessChecker.checkDefaultStrategy(); defaultStrategyErr != nil {
		logger.Error("ROUTER_DEFAULT_STRATEGY has no router configured; refusing to boot", "err", defaultStrategyErr)
		panic(defaultStrategyErr)
	}
	policyPinEnabled := strings.EqualFold(config.GetOr("ROUTER_POLICY_PIN_ENABLED", "false"), "true")
	if policyPinEnabled {
		logger.Info("Policy pin header enabled", "header", middleware.PolicyPinOverrideHeader)
	}
	subscriberAllowanceSvc, billingSvc := newSubscriberAllowance(logger, pool, billingSvc)
	var testPlans *policyregistry.TestPlanTools
	if testPlansEnabled {
		if servingAdmission == nil || billingSvc == nil {
			panic("internal test plan preparation requires managed serving and isolated prepaid billing")
		}
		testPlans = &policyregistry.TestPlanTools{Repository: servingpostgres.NewTestPlanRepo(pool), Store: servingAdmission.Store, Clock: time.Now}
	}
	initializedOrigins := make(map[string]struct{})
	if os.Getenv("PUBSUB_EMULATOR_HOST") == "" {
		initializedOrigins["https://pubsub.googleapis.com"] = struct{}{}
	}
	if servingTargetConfigured() || policyEnvironmentRaw != "" {
		initializedOrigins["https://storage.googleapis.com"] = struct{}{}
	}
	processCtx, stopProcess := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopProcess()
	waitForKeepWarm := startSubscriptionKeepWarm(processCtx, subscriptionRuntime, logger)
	egressProbe.prepare(processCtx, logger, providerMap, envKeyedProviders, initializedOrigins)
	logger.Info("Router startup initialization complete")
	serverFeatures := withContentCaptureMode(withRepositoryQueries(withVersionDetails(withCredentialPorts(withEnvServerConfig(withHMMAdminPorts(server.Features{
		SeatUsage:                seatUsage,
		SubscriptionLogins:       subscriptionLogins,
		SubscriptionRefresh:      subscriptionRuntime,
		StartupDatabasePing:      pool.Ping,
		TestPlans:                testPlans,
		PolicyPinEnabled:         policyPinEnabled,
		ServingAdmission:         servingAdmission,
		SubscriberAllowance:      subscriberAllowanceSvc,
		SubscriptionPoolsEnabled: subscriptionPoolsEnabled,
		PolicyStatus:             policyStatus,
		FeedbackLinks:            feedbackLinks,
	}, servingAdmission, routingDistribution), defaultStrategy)), pool), repo), captureMode)
	serverFeatures.EscalationAdmin = &floor.Admin{Dashboard: escalationDashboardStore, Sessions: llmEscalationStore, Configuration: llmEscalationStore, InvalidateSelection: authSvc.InvalidateInstallation, SwitchyardJudge: escalationJudge, QwenJudge: qwenEscalationJudge, SwitchyardActive: escalationJudgeActiveEnabled, Clock: time.Now}
	if trafficCapture != nil {
		serverFeatures.TrafficCapture = trafficCapture
	}
	server.RegisterWithFeatures(engine, authSvc, serverServices(proxySvc, ingressFormats), deployedModelsSource(hmmRosterModels), deploymentMode, billingSvc, readinessChecker, hmmRosterSources, analyticsSvc, serverFeatures)

	port := config.GetOr("PORT", "8080")
	address := ":" + port
	if trafficCapture != nil {
		captureListenHost := config.GetOr("ROUTER_HTTP_CAPTURE_LISTEN_HOST", "127.0.0.1")
		address = net.JoinHostPort(captureListenHost, port)
	}
	srv := &http.Server{
		Addr:              address,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	providerNames := make([]string, 0, len(providerMap))
	for name := range providerMap {
		providerNames = append(providerNames, name)
	}
	logger.Info("Starting Weave router", "providers", providerNames, "addr", srv.Addr)

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		logger.Error("Server exited with error", "err", err)
		apmFailCtx, apmFailCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer apmFailCancel()
		apm.ShutdownWithContext(apmFailCtx)
		return
	case <-processCtx.Done():
		logger.Info("Received shutdown signal; draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	drainServer(shutdownCtx, srv, logger, waitForKeepWarm)
	emitterCtx, emitterCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer emitterCancel()
	if err := emitter.Shutdown(emitterCtx); err != nil {
		logger.Warn("OTel emitter shutdown incomplete", "err", err)
	}
	apmCtx, apmCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer apmCancel()
	apm.ShutdownWithContext(apmCtx)
}

func subscriptionCandidateSignal(snapshot usage.Snapshot, now time.Time) subscriptions.CandidateSignal {
	if snapshot.OverageInUse {
		signal := subscriptions.CandidateSignal{Observed: true, Billable: true, UsedFraction: 1}
		if snapshot.UnifiedResetAt.After(now) {
			signal.ResetAt = snapshot.UnifiedResetAt
		}
		signal.ObservedAt = snapshot.ObservedAt
		return signal
	}

	signal := subscriptions.CandidateSignal{Exhausted: snapshot.ExhaustedAsOf(now), ObservedAt: snapshot.ObservedAt}
	for _, window := range []usage.Window{snapshot.Primary, snapshot.Secondary} {
		if math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 || window.UsedPercent > 1 {
			return subscriptions.CandidateSignal{}
		}
		if window.WindowMinutes <= 0 && window.UsedPercent <= 0 {
			continue
		}
		resetAt := window.ResetAt
		if resetAt.IsZero() && window.WindowMinutes > 0 {
			observedAt := window.ObservedAt
			if observedAt.IsZero() {
				observedAt = snapshot.ObservedAt
			}
			resetAt = observedAt.Add(time.Duration(window.WindowMinutes) * time.Minute)
		}
		if !resetAt.IsZero() && !resetAt.After(now) {
			continue
		}
		if !signal.Observed || window.UsedPercent > signal.UsedFraction {
			signal.Observed = true
			signal.UsedFraction = window.UsedPercent
		}
		if resetAt.After(now) && (signal.ResetAt.IsZero() || resetAt.Before(signal.ResetAt)) {
			signal.ResetAt = resetAt
		}
	}
	return signal
}

func buildEntraTokenSource(logger *slog.Logger) auth.EntraTokenSource {
	logger.Debug("Microsoft Entra client credentials token source initialized")
	entraClient := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return entra.NewClientCredentialsSource(entraClient, time.Now)
}

func buildWIFTokenSource(logger *slog.Logger) auth.WIFTokenSource {
	provider := strings.ToUpper(strings.TrimSpace(config.GetOr("ROUTER_WIF_PROVIDER", "")))
	audience := config.GetOr("ROUTER_WIF_AUDIENCE", auth.WIFAudience)
	switch provider {
	case "":
		return nil
	case auth.WIFProviderGCP:
		logger.Info("Workload identity federation enabled", "provider", provider, "audience", audience)
		return wif.NewGoogleTokenSource(audience)
	case auth.WIFProviderOIDC:
		path := config.GetOr("ROUTER_WIF_OIDC_TOKEN_FILE", "")
		if path == "" {
			err := fmt.Errorf("ROUTER_WIF_PROVIDER=%s requires ROUTER_WIF_OIDC_TOKEN_FILE", provider)
			logger.Error("Refusing to boot with incomplete workload identity configuration", "err", err)
			panic(err)
		}
		logger.Info("Workload identity federation enabled", "provider", provider, "token_file", path)
		return wif.NewFileTokenSource(path)
	default:
		err := fmt.Errorf("invalid ROUTER_WIF_PROVIDER %q (expected %q or %q)", provider, auth.WIFProviderGCP, auth.WIFProviderOIDC)
		logger.Error("Refusing to boot with invalid workload identity provider", "err", err)
		panic(err)
	}
}

func parseEnvInt(key string, fallback int) int {
	return parseEnvIntAtLeast(key, fallback, 1)
}

func parseEnvNonNegativeInt(key string, fallback int) int {
	return parseEnvIntAtLeast(key, fallback, 0)
}

func parseEnvIntAtLeast(key string, fallback, minimum int) int {
	raw := config.GetOr(key, "")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < minimum {
		observability.Get().Warn("Invalid env var; using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return n
}

func proxyRoutableModels(generic, providers map[string]struct{}, hmmWired bool) map[string]struct{} {
	if !hmmWired {
		return generic
	}
	out := catalog.HMMRoutingTargetSet(providers)
	if len(generic) == 0 {
		return out
	}
	for m := range generic {
		out[m] = struct{}{}
	}
	return out
}

func parseEnvFloat(key string, fallback float64) float64 {
	raw := config.GetOr(key, "")
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		observability.Get().Warn("Invalid env var; using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return v
}

func sseKeepaliveInterval() time.Duration {
	const defaultSec = 15
	raw := config.GetOr("ROUTER_SSE_KEEPALIVE_INTERVAL_SECONDS", "")
	if raw == "" {
		return defaultSec * time.Second
	}
	sec, err := strconv.Atoi(raw)
	if err != nil || sec < 0 {
		observability.Get().Warn("Invalid env var; using default", "key", "ROUTER_SSE_KEEPALIVE_INTERVAL_SECONDS", "value", raw, "default", defaultSec)
		return defaultSec * time.Second
	}
	return time.Duration(sec) * time.Second
}

func parseEnvDurationMs(key string, fallback time.Duration) time.Duration {
	raw := config.GetOr(key, "")
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		observability.Get().Warn("Invalid env var; using default", "key", key, "value", raw, "default_ms", fallback.Milliseconds())
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func parseEnvAttemptTimeoutMs(fallback time.Duration) time.Duration {
	if ms, err := strconv.Atoi(config.GetOr(hmmAttemptTimeoutEnv, "")); err == nil && ms == 0 {
		return 0
	}
	return parseEnvDurationMs(hmmAttemptTimeoutEnv, fallback)
}

func safeGo(logger *slog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("Background goroutine panicked", "goroutine", name, "panic", r)
			}
		}()
		fn()
	}()
}

func cortexWebSearch(logger *slog.Logger) websearch.Executor {
	if config.GetOr("ROUTER_CORTEX_WEB_SEARCH", "true") != "true" {
		logger.Info("Cortex Agents web-search executor disabled (ROUTER_CORTEX_WEB_SEARCH)")
		return nil
	}
	role := config.GetOr("SNOWFLAKE_AGENT_ROLE", "")
	hostSuffix := config.GetOr("SNOWFLAKE_AGENT_HOST_SUFFIX", "")
	timeout := parseEnvDurationMs("SNOWFLAKE_AGENT_TIMEOUT_MS", 0)
	logger.Info("Cortex Agents web-search executor enabled",
		"snowflake_role", role, "host_suffix_override", hostSuffix,
		"timeout_ms", timeout.Milliseconds())
	opts := []cortexagents.Option{cortexagents.WithRole(role), cortexagents.WithTimeout(timeout)}
	if hostSuffix != "" {
		opts = append(opts, cortexagents.WithHostSuffix(hostSuffix))
	}
	return cortexagents.NewClient(config.GetOr("ANTHROPIC_GATEWAY_BASE_URL", ""), opts...)
}

func resolveDefaultBaselineModel() string {
	v, ok := os.LookupEnv("ROUTER_DEFAULT_BASELINE_MODEL")
	if !ok {
		return "claude-sonnet-4-5"
	}
	return strings.TrimSpace(v)
}

func resolveCompactionModel(summarizerProvider string) (string, error) {
	m := strings.TrimSpace(config.GetOr("ROUTER_COMPACTION_MODEL", policy.PrecompactionDefaultModel))
	if m == "" {
		return policy.PrecompactionDefaultModel, nil
	}
	if _, ok := catalog.ResolveBinding(m, map[string]struct{}{summarizerProvider: {}}); !ok {
		return "", fmt.Errorf("ROUTER_COMPACTION_MODEL %q has no %s catalog binding", m, summarizerProvider)
	}
	return m, nil
}

func resolveHardPinModel() (provider, model string, resolutionErr error) {
	configuredModel := strings.TrimSpace(config.GetOr("ROUTER_HARD_PIN_MODEL", ""))
	configuredProvider := strings.TrimSpace(config.GetOr("ROUTER_HARD_PIN_PROVIDER", ""))
	if configuredModel != "" {
		if configuredProvider == "" {
			configuredProvider = policy.UtilityHardPinDefaultProvider
		}
		return configuredProvider, configuredModel, nil
	}
	if configuredProvider != "" {
		return "", "", errors.New("ROUTER_HARD_PIN_PROVIDER requires ROUTER_HARD_PIN_MODEL")
	}
	return policy.UtilityHardPinDefaultProvider, policy.UtilityHardPinDefaultModel, nil
}

func registerDeploymentKeyedProvider(
	providerMap map[string]providers.Client,
	envKeyedProviders map[string]struct{},
	logger *slog.Logger,
	name, displayName, keyEnvVar, baseURL string,
	byokOnly bool,
	newClient func(key, baseURL string) providers.Client,
	extraLogAttrs ...any,
) {
	key := ""
	if !byokOnly {
		key = config.GetOr(keyEnvVar, "")
	}
	providerMap[name] = newClient(key, baseURL)
	switch {
	case byokOnly:
		logger.Info(displayName+" provider enabled (BYOK only)", "base_url", baseURL)
	case key != "":
		envKeyedProviders[name] = struct{}{}
		logger.Info(displayName+" provider enabled", append([]any{"base_url", baseURL}, extraLogAttrs...)...)
	default:
		logger.Info(displayName+" provider registered (BYOK only — set "+keyEnvVar+" for deployment-level use)", "base_url", baseURL)
	}
}

func openRouterModelIDs(raw string) (map[string]string, error) {
	overrides, err := config.ParseModelIDMap(raw)
	if err != nil {
		return nil, err
	}
	ids := upstreamIDsForProvider(providers.ProviderOpenRouter)
	if ids == nil {
		return overrides, nil
	}
	maps.Copy(ids, overrides)
	return ids, nil
}

func upstreamIDsForProvider(provider string) map[string]string {
	out := make(map[string]string)
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			if b.Provider == provider && b.UpstreamID != "" {
				out[m.ID] = b.UpstreamID
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type escalationStateSweeper interface {
	SweepExpired(context.Context) error
}

func buildAnthropicFederationTokenSource(deploymentMode server.DeploymentMode) auth.AnthropicFederationTokenSource {
	tokenFile := config.GetOr("ANTHROPIC_IDENTITY_TOKEN_FILE", "")
	identityToken := wif.GoogleIdentityToken
	if tokenFile != "" {
		identityToken = func(ctx context.Context, _ string) (string, error) {
			return wif.ReadAnthropicIdentityToken(ctx, tokenFile)
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	source := anthropicfederation.NewSource(client, identityToken, time.Now)
	if deploymentMode != server.DeploymentModeManaged {
		return source
	}
	bindingsJSON, err := base64.StdEncoding.DecodeString(config.GetOr("ROUTER_ANTHROPIC_FEDERATION_BINDINGS_B64", "e30="))
	if err != nil {
		panic("invalid base64 managed Anthropic federation bindings")
	}
	bindings, err := anthropicfederation.ParseInstallationBindings(string(bindingsJSON))
	if err != nil {
		panic(fmt.Errorf("invalid managed Anthropic federation configuration: %w", err))
	}
	return source.RequireInstallationBindings(bindings)
}
