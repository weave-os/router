package proxy

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	randomrouter "weave-os/router/internal/router/random_router"

	"weave-os/router/internal/subscriptions"
)

func randomBenchmarkFrom(ctx context.Context) *randomrouter.RequestState {
	return randomrouter.RequestFrom(ctx)
}

func BeginRandomBenchmark(s *Service, ctx context.Context, digest string) (context.Context, *randomrouter.Config, error) {
	registered, found := s.strategies[router.StrategyRandomBenchmark]
	if !found || registered.router == nil {
		return ctx, nil, errors.New("random benchmark unavailable")
	}
	configuration, err := randomrouter.ParseConfig(flags.StringOr(ctx, flags.KeyRandomBenchmarkConfig, ""), s.clockNow())
	if err != nil || configuration == nil {
		return ctx, nil, errors.New("random benchmark not enabled for this installation")
	}
	profile := &randomrouter.RequestState{Config: *configuration, Policy: registered.router}
	config := &profile.Config
	key, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	org, _ := ctx.Value(ExternalIDContextKey{}).(string)
	identity, present := requestcontext.ServingIdentityFromContext(ctx)
	if key != config.APIKeyID || org != config.OrganizationID || s.installationID(ctx) != config.InstallationID || digest != config.PolicySHA256 || !present || identity.Target != string(policyregistry.TargetInternal) {
		return ctx, nil, errors.New("random benchmark identity or policy mismatch")
	}
	ctx = randomrouter.WithRequest(ctx, profile)
	ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderCodex: {}})
	owner := subscriptionOwnerFromContext(ctx)
	owner.RequiredAccountID = config.SubscriptionAccountID
	ctx = WithSubscriptionOwner(ctx, owner)
	ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
	ctx = requestcontext.WithIncludedSubscriptionOnly(ctx)
	ctx = context.WithValue(ctx, subscriptionAPIOnlyKey{}, false)
	ctx = context.WithValue(ctx, subscriptionOnlyAttemptKey{}, true)
	ctx = router.WithStrategy(ctx, router.StrategyRandomBenchmark)
	overrides, _ := flags.OverridesFromContext(ctx)
	overrides.Bools = maps.Clone(overrides.Bools)
	if overrides.Bools == nil {
		overrides.Bools = make(map[flags.Key]bool)
	}
	for _, key := range []flags.Key{flags.KeyPlannerEnabled, flags.KeyLoopEscalationEnabled, flags.KeyEffortEscalation, flags.KeySiblingFailover, flags.KeyCyberRefusalRetry, flags.KeyCyberRefusalRepin, flags.KeyAnthropicServerFallback, flags.KeyTransientRateLimit} {
		overrides.Bools[key] = false
	}
	overrides.Strings = maps.Clone(overrides.Strings)
	if overrides.Strings == nil {
		overrides.Strings = make(map[flags.Key]string)
	}
	overrides.Strings[flags.KeyEscalationActiveClassifier] = string(flags.EscalationClassifierNone)
	overrides.Strings[flags.KeyEscalationShadowClassifier] = string(flags.EscalationClassifierNone)
	overrides.Strings[flags.KeyEarlyStopRedo] = string(flags.EarlyStopRedoOff)
	ctx = flags.WithOverrides(ctx, overrides)
	return ctx, config, nil
}

func PrepareRandomBenchmark(s *Service, ctx context.Context) (context.Context, func(), error) {
	credentialCtx, lease, err := leaseRandomSubscription(s, ctx)
	if err != nil {
		return ctx, func() {}, err
	}
	client, err := s.clients.Client(providers.ProviderOpenAI)
	if err != nil {
		lease.Release()
		return ctx, func() {}, err
	}
	checker, ok := client.(interface {
		CheckIncludedSubscriptionModels(context.Context, []string) error
	})
	if !ok {
		lease.Release()
		return ctx, func() {}, ErrSubscriptionPoolUnavailable
	}
	if err := checker.CheckIncludedSubscriptionModels(credentialCtx, randomrouter.Models()); err != nil {
		lease.Release()
		return ctx, func() {}, err
	}
	execution := randomrouter.RequestFrom(ctx)
	execution.Lease = subscriptions.Lease{AccountID: lease.AccountID, OwnerID: lease.OwnerID, Tier: lease.Tier, AccessToken: lease.AccessToken, ProviderAccount: lease.ProviderAccount, State: lease.State, CooldownUntil: lease.CooldownUntil}
	execution.Credentials = CredentialsFromContext(credentialCtx)
	execution.Prepared = true
	return ctx, lease.Release, nil
}

func routeRandomBenchmark(ctx context.Context, request router.Request) (router.Decision, error) {
	profile := randomBenchmarkFrom(ctx)
	if profile == nil || !profile.Prepared {
		return router.Decision{}, randomrouter.ErrUnavailable
	}
	models := randomrouter.Models()
	request.OverflowAdmittedModels = nil
	request.ForceModel, request.ForceCluster = "", ""
	request.PromptText, request.ConversationMessages = "", nil
	decision, err := profile.Policy.Route(ctx, request)
	if err != nil {
		return router.Decision{}, err
	}
	if decision.Provider != providers.ProviderOpenAI || !slices.Contains(models, decision.Model) || decision.Metadata == nil || decision.Metadata.PolicyArtifactSHA256 != profile.Config.PolicySHA256 || decision.Metadata.Propensity != 1.0/3.0 {
		return router.Decision{}, randomrouter.ErrUnavailable
	}
	observability.FromContext(ctx).Info("Strict random benchmark selected", "model", decision.Model, "policy_sha256", profile.Config.PolicySHA256, "propensity", decision.Metadata.Propensity, "candidate_count", len(models), "subscription_account_id", profile.Config.SubscriptionAccountID)
	return decision, nil
}

func leaseRandomSubscription(s *Service, ctx context.Context) (context.Context, subscriptions.Lease, error) {
	profile := randomBenchmarkFrom(ctx)
	if profile == nil || !profile.Config.ExpiresAt.After(s.clockNow()) || s.managedSubscriptions == nil || !s.supportsSubscriptionTransport(providers.ProviderOpenAI) {
		return ctx, subscriptions.Lease{}, ErrSubscriptionPoolUnavailable
	}
	owner := subscriptionOwnerFromContext(ctx)
	owner.RequiredAccountID = profile.Config.SubscriptionAccountID
	lease, present, err := subscriptions.LeaseRequired(ctx, s.managedSubscriptions, owner, subscriptions.ProviderCodex, ClientIdentityFrom(ctx).SessionID)
	if err != nil || !present {
		lease.Release()
		return ctx, subscriptions.Lease{}, errors.Join(ErrSubscriptionPoolUnavailable, err)
	}
	creds := &Credentials{APIKey: []byte(lease.AccessToken), OAuth: true, Source: credSourceCodexSubscription, AccountID: []byte(lease.ProviderAccount), PrincipalID: "chatgpt-account:" + lease.ProviderAccount, SubscriptionAccountID: lease.AccountID}
	ctx = context.WithValue(ctx, CredentialsContextKey{}, creds)
	return ctx, lease, nil
}

func randomBenchmarkHeaders(headers http.Header) bool {
	if headers.Get("Session-Id") == "" {
		return false
	}
	for _, header := range []string{ForceModelHeader, "x-weave-force-cluster", "x-weave-policy-pin", "x-weave-model-passthrough"} {
		if headers.Get(header) != "" {
			return false
		}
	}
	return true
}
