package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"
)

var ErrUnknownModel = errors.New("auth: unknown model id")

var ErrModelNotFastCapable = errors.New("auth: model has no fast tier")

var ErrUnknownProvider = errors.New("auth: unknown provider")

var ErrInvalidBaseURL = errors.New("auth: invalid base url")

var ErrBaseURLRequired = errors.New("auth: base url required for provider")

var ErrInvalidModelAlias = errors.New("auth: invalid model alias")

var ErrInvalidIdentityHeader = errors.New("auth: invalid identity header")

var ErrInvalidForwardedHeader = errors.New("auth: invalid forwarded header")

var ErrInvalidKeypairAuth = errors.New("auth: invalid keypair auth")

var ErrInvalidEntraAuth = errors.New("auth: invalid Microsoft Entra auth")

const (
	blindExperimentFetchTimeout  = 5 * time.Second
	blindExperimentFetchAttempts = 3
)

type InstallationChangeNotifier interface {
	NotifyInstallationChanged(installationID string)
}

type NoOpInstallationChangeNotifier struct{}

func (NoOpInstallationChangeNotifier) NotifyInstallationChanged(string) {}

type Service struct {
	installations              InstallationRepository
	apiKeys                    APIKeyRepository
	credentialSubjects         CredentialSubjectLookup
	externalKeys               ExternalAPIKeyRepository
	users                      UserRepository
	clusterModelLists          ClusterModelListRepository
	userClusterModelLists      UserClusterModelListRepository
	blindExperiments           BlindExperimentRepository
	routingPolicies            RoutingPolicyRepository
	routingPolicyCache         *RoutingPolicyCache
	cache                      APIKeyCache
	userCache                  UserCache
	userClusterCache           UserClusterListCache
	blindExperimentCache       BlindExperimentCache
	blindExperimentSessions    BlindExperimentSessionRepository
	blindExperimentSessionArms *lru.Cache[string, BlindExperimentArm]
	blindExperimentFetches     singleflight.Group
	subscriptionAccounts       SubscriptionAccountRepository
	codexEnrollmentVerifier    CodexEnrollmentVerifier
	notifier                   InstallationChangeNotifier
	now                        Clock
	externalKeyMarks           externalKeyMarks
	keyUsageMarks              usageMarks
	installationUsageMarks     usageMarks
	encryptor                  Encryptor
	keypairTokens              *KeypairTokenCache
	anthropicFederationTokens  AnthropicFederationTokenSource
	wifTokens                  WIFTokenSource

	entraTokens EntraTokenSource

	flagOverridesDisabled bool

	delegatedSpendCapsEnabled bool

	adminPassword   string
	adminSessionKey []byte

	adminLoginFailures *expirable.LRU[string, int]
	adminLoginMu       sync.Mutex

	onboarding OnboardingObserver
}

func (s *Service) WithSubscriptionAccounts(repo SubscriptionAccountRepository) *Service {
	s.subscriptionAccounts = repo
	return s
}

func (s *Service) WithCodexEnrollmentVerifier(verifier CodexEnrollmentVerifier) *Service {
	s.codexEnrollmentVerifier = verifier
	return s
}

func (s *Service) WithCredentialSubjectLookup(lookup CredentialSubjectLookup) *Service {
	s.credentialSubjects = lookup
	return s
}

func (s *Service) WithOnboardingObserver(observer OnboardingObserver) *Service {
	s.onboarding = observer
	return s
}

func (s *Service) CurrentTime() time.Time {
	return s.now()
}

func NewService(
	installations InstallationRepository,
	apiKeys APIKeyRepository,
	externalKeys ExternalAPIKeyRepository,
	users UserRepository,
	cache APIKeyCache,
	userCache UserCache,
	now Clock,
) *Service {
	if userCache == nil {
		userCache = NoOpUserCache{}
	}
	return &Service{
		installations:        installations,
		apiKeys:              apiKeys,
		externalKeys:         externalKeys,
		users:                users,
		cache:                cache,
		userCache:            userCache,
		userClusterCache:     NoOpUserClusterListCache{},
		blindExperimentCache: NoOpBlindExperimentCache{},
		routingPolicyCache:   NewRoutingPolicyCache(5 * time.Minute),
		notifier:             NoOpInstallationChangeNotifier{},
		now:                  now,
		encryptor:            NoOpEncryptor{},
		keypairTokens:        NewKeypairTokenCache(now),
	}
}

func (s *Service) WithEncryptor(e Encryptor) *Service {
	s.encryptor = e
	return s
}

func (s *Service) WithWIFTokenSource(src WIFTokenSource) *Service {
	s.wifTokens = src
	return s
}

func (s *Service) WithEntraTokenSource(src EntraTokenSource) *Service {
	s.entraTokens = src
	return s
}

func (s *Service) WithClusterModelLists(repo ClusterModelListRepository) *Service {
	s.clusterModelLists = repo
	return s
}

func (s *Service) WithUserClusterModelLists(repo UserClusterModelListRepository, cache UserClusterListCache) *Service {
	s.userClusterModelLists = repo
	if cache == nil {
		cache = NoOpUserClusterListCache{}
	}
	s.userClusterCache = cache
	return s
}

func (s *Service) WithBlindExperiments(repo BlindExperimentRepository, cache BlindExperimentCache) *Service {
	s.blindExperiments = repo
	if cache == nil {
		cache = NoOpBlindExperimentCache{}
	}
	s.blindExperimentCache = cache
	return s
}

func (s *Service) WithRoutingPolicies(repo RoutingPolicyRepository, cache *RoutingPolicyCache) *Service {
	s.routingPolicies = repo
	if cache != nil {
		s.routingPolicyCache = cache
	}
	return s
}

func (s *Service) WithRoutingPolicy(ctx context.Context, installationID string) (context.Context, error) {
	if s.routingPolicies == nil {
		return ctx, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		generation := s.routingPolicyCache.generation(installationID)
		policy, found := s.routingPolicyCache.policy(installationID, generation)
		if !found {
			var err error
			policy, err = s.routingPolicies.GetPolicy(ctx, installationID)
			if err != nil {
				return ctx, fmt.Errorf("load installation routing policy: %w: %w", err, ErrRoutingPolicyUnavailable)
			}
			if !s.routingPolicyCache.setPolicy(installationID, generation, policy) {
				continue
			}
		}
		if policy.Mode != RoutingPolicyInherit && policy.Mode != RoutingPolicyPassthrough && policy.Mode != RoutingPolicyAssigned {
			return ctx, fmt.Errorf("unrecognized installation routing mode %q: %w", policy.Mode, ErrRoutingPolicyUnavailable)
		}
		if s.routingPolicyCache.generation(installationID) == generation {
			ctx = context.WithValue(ctx, routingPolicyGenerationContextKey{}, generation)
			return WithRoutingPolicy(ctx, policy), nil
		}
	}
	return ctx, ErrRoutingPolicyUnavailable
}

func (s *Service) WithRoutingAssignment(ctx context.Context, installationID, routerUserID string) (context.Context, error) {
	policy := RoutingPolicyFrom(ctx)
	for attempt := 0; attempt < 3; attempt++ {
		generation := s.routingPolicyCache.generation(installationID)
		admittedGeneration, _ := ctx.Value(routingPolicyGenerationContextKey{}).(uint64)
		if admittedGeneration != generation {
			var err error
			ctx, err = s.WithRoutingPolicy(ctx, installationID)
			if err != nil {
				return ctx, err
			}
			policy = RoutingPolicyFrom(ctx)
			continue
		}
		if policy.Mode != RoutingPolicyAssigned || routerUserID == "" {
			return ctx, nil
		}
		routerOn, found := s.routingPolicyCache.assignment(installationID, routerUserID, policy.Revision, generation)
		if !found {
			var err error
			routerOn, err = s.routingPolicies.HasAssignment(ctx, installationID, routerUserID, policy.Revision)
			if err != nil {
				return ctx, fmt.Errorf("load installation routing assignment: %w: %w", err, ErrRoutingPolicyUnavailable)
			}
			if !s.routingPolicyCache.setAssignment(installationID, routerUserID, policy.Revision, generation, routerOn) {
				continue
			}
		}
		if s.routingPolicyCache.generation(installationID) == generation {
			return context.WithValue(ctx, routingDecisionContextKey{}, routerOn), nil
		}
	}
	return ctx, ErrRoutingPolicyUnavailable
}

func (s *Service) WithInstallationChangeNotifier(n InstallationChangeNotifier) *Service {
	if n == nil {
		s.notifier = NoOpInstallationChangeNotifier{}
		return s
	}
	s.notifier = n
	return s
}

func (s *Service) WithFlagOverridesDisabled(disabled bool) *Service {
	s.flagOverridesDisabled = disabled
	return s
}

func (s *Service) FlagOverridesDisabled() bool {
	return s.flagOverridesDisabled
}

func (s *Service) invalidateInstallation(installationID string) {
	if installationID == "" {
		return
	}
	s.cache.InvalidateInstallation(installationID)
	s.routingPolicyCache.InvalidateInstallation(installationID)
	s.notifier.NotifyInstallationChanged(installationID)
}

func (s *Service) InvalidateInstallation(installationID string) {
	s.invalidateInstallation(installationID)
}

func (s *Service) IssueAPIKey(ctx context.Context, installationID string, name *string, createdBy *string) (*APIKey, string, error) {
	return s.IssueScopedAPIKey(ctx, installationID, ScopeRouting, name, createdBy)
}

func (s *Service) IssueScopedAPIKey(ctx context.Context, installationID string, scope APIKeyScope, name *string, createdBy *string) (*APIKey, string, error) {
	if !scope.UserIssuable() {
		return nil, "", fmt.Errorf("%w: %q", ErrInvalidKeyScope, scope)
	}
	scope = scope.Normalized()
	rawToken := GenerateID(scope.TokenPrefix())
	keyHash, keyPrefix, keySuffix := APITokenFingerprint(rawToken)
	externalID := GenerateID("kid")
	key, err := s.apiKeys.Create(ctx, CreateAPIKeyParams{
		InstallationID: installationID,
		ExternalID:     externalID,
		Name:           name,
		KeyPrefix:      keyPrefix,
		KeyHash:        keyHash,
		KeySuffix:      keySuffix,
		Scope:          scope,
		CreatedBy:      createdBy,
	})
	if err != nil {
		return nil, "", err
	}
	return key, rawToken, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, installationID string) ([]*APIKey, error) {
	keys, err := s.apiKeys.ListForInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	visible := keys[:0]
	for _, key := range keys {
		if key.Scope.UserIssuable() {
			visible = append(visible, key)
		}
	}
	return visible, nil
}

func (s *Service) RotateAPIKey(ctx context.Context, installationID, keyID string, createdBy *string) (*APIKey, string, error) {
	existing, err := s.apiKeys.ListForInstallation(ctx, installationID)
	if err != nil {
		return nil, "", err
	}
	var target *APIKey
	for _, k := range existing {
		if k.ID == keyID {
			target = k
			break
		}
	}
	if target == nil || !target.Scope.UserIssuable() {
		return nil, "", ErrAPIKeyNotFound
	}
	if target.CredentialSubjectID != "" {
		return nil, "", ErrPersonalCredentialRequired
	}
	n, err := s.apiKeys.SoftDelete(ctx, installationID, target.ID)
	if err != nil {
		return nil, "", err
	}
	if n == 0 {
		return nil, "", ErrAPIKeyNotFound
	}
	key, raw, err := s.IssueScopedAPIKey(ctx, installationID, target.Scope, target.Name, createdBy)
	if err != nil {
		return nil, "", err
	}
	s.invalidateInstallation(installationID)
	return key, raw, nil
}

func (s *Service) DeleteAPIKey(ctx context.Context, installationID, id string) error {
	if _, err := s.apiKeys.SoftDelete(ctx, installationID, id); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

func (s *Service) ListExternalAPIKeys(ctx context.Context, installationID string) ([]*ExternalAPIKey, error) {
	return s.externalKeys.GetForInstallation(ctx, installationID)
}

type UpsertExternalAPIKeyParams struct {
	Provider               string
	RawKey                 string
	Name                   *string
	BaseURL                *string
	ModelAliases           map[string]string
	AllowedModels          map[string]struct{}
	IdentityHeader         *string
	IdentityHeaderFormat   *string
	ForwardedClientHeaders []string
	BaggageHeader          *string
	FederationConfig       *AnthropicFederationConfig
	AuthType               AuthType
	AuthAccount            *string
	AuthUser               *string
	FundingPriority        FundingPriority
	CreatedBy              *string
}

func (s *Service) UpsertExternalAPIKey(ctx context.Context, installationID string, params UpsertExternalAPIKeyParams) (*ExternalAPIKey, error) {
	provider, rawKey := params.Provider, params.RawKey
	normalizedBaseURL, err := NormalizeBaseURL(params.BaseURL)
	if err != nil {
		return nil, err
	}
	normalizedAliases, err := NormalizeModelAliases(params.ModelAliases, params.AllowedModels)
	if err != nil {
		return nil, err
	}
	identityHeader, identityFormat, err := NormalizeIdentityHeader(params.IdentityHeader, params.IdentityHeaderFormat)
	if err != nil {
		return nil, err
	}
	forwardedHeaders, err := NormalizeForwardedClientHeaders(params.ForwardedClientHeaders)
	if err != nil {
		return nil, err
	}
	baggageHeader, err := NormalizeBaggageHeader(params.BaggageHeader)
	if err != nil {
		return nil, err
	}
	baggageName := ""
	if baggageHeader != nil {
		baggageName = *baggageHeader
	}
	identityName := ""
	if identityHeader != nil {
		identityName = *identityHeader
	}
	if err := ValidateForwardingHeaderConfiguration(forwardedHeaders, baggageName, identityName); err != nil {
		return nil, err
	}
	authType, authAccount, authUser, err := NormalizeAuthType(params.AuthType, params.AuthAccount, params.AuthUser)
	if err != nil {
		return nil, err
	}
	federation, err := NormalizeAnthropicFederation(authType, provider, trimmedValue(normalizedBaseURL), params.FederationConfig)
	if err != nil {
		return nil, err
	}
	fundingPriority, err := s.upsertFundingPriority(ctx, installationID, provider, params.FundingPriority)
	if err != nil {
		return nil, err
	}
	if authType == AuthTypeAnthropicWIF && rawKey != "" {
		return nil, fmt.Errorf("%w: workload federation takes no key material", ErrInvalidAnthropicFederation)
	}
	if authType == AuthTypeWIF {
		if !providers.RequiresBaseURL(provider) {
			return nil, fmt.Errorf("%w: %s does not accept workload identity credentials", ErrInvalidKeypairAuth, provider)
		}
		if rawKey != "" {
			return nil, fmt.Errorf("%w: %s takes no key material", ErrInvalidKeypairAuth, AuthTypeWIF)
		}
	}
	if authType == AuthTypeAzureEntra {
		if !providers.RequiresBaseURL(provider) {
			return nil, fmt.Errorf("%w: %s does not accept Microsoft Entra credentials", ErrInvalidEntraAuth, provider)
		}
		if rawKey == "" {
			return nil, fmt.Errorf("%w: %s needs a client secret", ErrInvalidEntraAuth, AuthTypeAzureEntra)
		}
	}
	if authType == AuthTypeKeypairJWT {
		if !providers.RequiresBaseURL(provider) {
			return nil, fmt.Errorf("%w: %s does not accept key-pair credentials", ErrInvalidKeypairAuth, provider)
		}
		if _, err := ParseKeypairPrivateKey([]byte(rawKey)); err != nil {
			return nil, err
		}
	}
	if normalizedBaseURL == nil && providers.RequiresBaseURL(provider) {
		return nil, fmt.Errorf("%w: %s", ErrBaseURLRequired, provider)
	}
	externalID := GenerateID("ekid")
	ciphertext := []byte{}
	var hash, prefix, suffix string
	if authType != AuthTypeWIF && authType != AuthTypeAnthropicWIF {
		ciphertext, err = s.encryptor.Encrypt([]byte(rawKey), externalID, provider)
		if err != nil {
			return nil, err
		}
		hash, prefix, suffix = APITokenFingerprint(rawKey)
	}
	if err := s.externalKeys.SoftDeleteByProvider(ctx, installationID, provider); err != nil {
		return nil, err
	}
	key, err := s.externalKeys.Create(ctx, CreateExternalAPIKeyParams{
		InstallationID: installationID,
		ExternalID:     externalID,
		Provider:       provider,
		KeyCiphertext:  ciphertext,
		KeyPrefix:      prefix,
		KeySuffix:      suffix,
		KeyFingerprint: hash,
		Name:           params.Name,
		BaseURL:        normalizedBaseURL,
		ModelAliases:   normalizedAliases,

		IdentityHeader:         identityHeader,
		IdentityHeaderFormat:   identityFormat,
		ForwardedClientHeaders: forwardedHeaders,
		BaggageHeader:          baggageHeader,

		FederationConfig: federation,
		AuthType:         authType,
		AuthAccount:      authAccount,
		AuthUser:         authUser,
		FundingPriority:  fundingPriority,
		CreatedBy:        params.CreatedBy,
	})
	if err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return key, nil
}

func (s *Service) SetExternalAPIKeyModelAliases(ctx context.Context, installationID, id string, aliases map[string]string, allowed map[string]struct{}) (*ExternalAPIKey, error) {
	normalized, err := NormalizeModelAliases(aliases, allowed)
	if err != nil {
		return nil, err
	}
	key, err := s.externalKeys.UpdateModelAliases(ctx, installationID, id, normalized)
	if err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return key, nil
}

func (s *Service) upsertFundingPriority(ctx context.Context, installationID, provider string, requested FundingPriority) (FundingPriority, error) {
	if requested != "" {
		return NormalizeFundingPriority(requested)
	}
	activePriority, err := s.externalKeys.ActiveFundingPriority(ctx, installationID, provider)
	if err != nil {
		return "", err
	}
	return NormalizeFundingPriority(activePriority)
}

func (s *Service) SetExternalAPIKeyFundingPriority(ctx context.Context, installationID, id string, priority FundingPriority) (*ExternalAPIKey, error) {
	if priority == "" {
		return nil, ErrInvalidFundingPriority
	}
	normalized, err := NormalizeFundingPriority(priority)
	if err != nil {
		return nil, err
	}
	key, err := s.externalKeys.UpdateFundingPriority(ctx, installationID, id, normalized)
	if err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return key, nil
}

func (s *Service) DeleteExternalAPIKey(ctx context.Context, installationID, id string) error {
	if err := s.externalKeys.SoftDelete(ctx, installationID, id); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

func (s *Service) SetInstallationExcludedModels(ctx context.Context, externalID, installationID string, models []string, allowed map[string]struct{}) ([]string, error) {
	if models == nil {
		models = []string{}
	}
	if allowed != nil {
		for _, m := range models {
			if _, ok := allowed[m]; !ok {
				return nil, fmt.Errorf("%w: %q", ErrUnknownModel, m)
			}
		}
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, m := range models {
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	if err := s.installations.UpdateExcludedModels(ctx, externalID, installationID, out); err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return out, nil
}

func (s *Service) SetInstallationFastModeModels(ctx context.Context, externalID, installationID string, models []string, fastCapable map[string]struct{}) ([]string, error) {
	if models == nil {
		models = []string{}
	}
	if fastCapable != nil {
		for _, m := range models {
			if _, ok := fastCapable[m]; !ok {
				return nil, fmt.Errorf("%w: %q", ErrModelNotFastCapable, m)
			}
		}
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, m := range models {
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	if err := s.installations.UpdateFastModeModels(ctx, externalID, installationID, out); err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return out, nil
}

func (s *Service) SetInstallationAllowedModels(ctx context.Context, externalID, installationID string, models []string, allowed map[string]struct{}) ([]string, error) {
	if models == nil {
		models = []string{}
	}
	if allowed != nil && len(models) > 0 {
		for _, m := range models {
			if _, ok := allowed[m]; !ok {
				return nil, fmt.Errorf("%w: %q", ErrUnknownModel, m)
			}
		}
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, m := range models {
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	if err := s.installations.UpdateAllowedModels(ctx, externalID, installationID, out); err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return out, nil
}

func (s *Service) SetInstallationExcludedProviders(ctx context.Context, externalID, installationID string, providerNames []string, allowed map[string]struct{}) ([]string, error) {
	if providerNames == nil {
		providerNames = []string{}
	}
	if allowed != nil {
		for _, p := range providerNames {
			if _, ok := allowed[p]; !ok {
				return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, p)
			}
		}
	}
	seen := make(map[string]struct{}, len(providerNames))
	out := make([]string, 0, len(providerNames))
	for _, p := range providerNames {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if err := s.installations.UpdateExcludedProviders(ctx, externalID, installationID, out); err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return out, nil
}

func (s *Service) SetInstallationRoutingPreference(ctx context.Context, externalID, installationID string, qualityWeight *float64) error {
	if err := s.installations.UpdateRoutingPreference(ctx, externalID, installationID, qualityWeight); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

func (s *Service) SetInstallationSubscriptionRoutingDisabled(ctx context.Context, externalID, installationID string, disabled bool) error {
	if err := s.installations.UpdateSubscriptionRoutingDisabled(ctx, externalID, installationID, disabled); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

func (s *Service) SetInstallationHideTerminalSurfaces(ctx context.Context, externalID, installationID string, hide bool) error {
	if err := s.installations.UpdateHideTerminalSurfaces(ctx, externalID, installationID, hide); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

func (s *Service) SetInstallationShowModelSelectionReasoning(ctx context.Context, externalID, installationID string, show bool) error {
	if err := s.installations.UpdateShowModelSelectionReasoning(ctx, externalID, installationID, show); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

var ErrInvalidCaptureMode = errors.New("auth: invalid content capture mode")

func (s *Service) SetInstallationContentCaptureMode(ctx context.Context, externalID, installationID string, mode *string) error {
	if mode != nil {
		switch *mode {
		case "off", "hashed", "full":
		default:
			return fmt.Errorf("%w: %q", ErrInvalidCaptureMode, *mode)
		}
	}
	if err := s.installations.UpdateContentCaptureMode(ctx, externalID, installationID, mode); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

var ErrFlagNotOverridable = errors.New("auth: flag is not overridable per organization")

func (s *Service) SetInstallationFlagOverrides(ctx context.Context, externalID, installationID string, overrides flags.Overrides) error {
	for _, key := range overrides.Keys() {
		def, ok := flags.Lookup(key)
		if !ok || !def.OrgOverridable {
			return fmt.Errorf("%w: %q", ErrFlagNotOverridable, key)
		}
	}
	if err := flags.ValidateOverrides(overrides); err != nil {
		return err
	}
	if err := s.installations.UpdateFlagOverrides(ctx, externalID, installationID, overrides); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

func (s *Service) VerifyAPIKey(ctx context.Context, rawToken string) (*Installation, *APIKey, []*ExternalAPIKey, []ClusterModelList, error) {
	return s.verifyAPIKey(ctx, rawToken, true)
}

func (s *Service) VerifyPlatformAPIKey(ctx context.Context, rawToken string) (*Installation, *APIKey, []ClusterModelList, error) {
	installation, key, _, lists, err := s.verifyAPIKey(ctx, rawToken, false)
	return installation, key, lists, err
}

func (s *Service) verifyAPIKey(ctx context.Context, rawToken string, includeUpstreamKeys bool) (*Installation, *APIKey, []*ExternalAPIKey, []ClusterModelList, error) {
	if !HasAPIKeyPrefix(rawToken) {
		return nil, nil, nil, nil, ErrInvalidPrefix
	}
	if IsSessionToken(rawToken) {
		return nil, nil, nil, nil, ErrInvalidToken
	}

	keyHash := HashAPIKeySHA256(rawToken)

	if cached, ok := s.cache.Get(keyHash); ok {
		if cached.Negative {
			return nil, nil, nil, nil, ErrInvalidToken
		}
		if cached.APIKey != nil {
			if cached.APIKey.Scope.Normalized() != ScopeRouting {
				return nil, nil, nil, nil, ErrWrongKeyScope
			}
			s.fireMarkUsed(ctx, cached.APIKey, cached.Installation)
			s.fireMarkFirstRequestServed(ctx, cached.APIKey.InstallationID, cached.Installation)
			if !includeUpstreamKeys {
				return cached.Installation, cached.APIKey, nil, cached.ClusterModelLists, nil
			}
			return cached.Installation, cached.APIKey, s.resolveUpstreamSecrets(ctx, cached.ExternalKeys), cached.ClusterModelLists, nil
		}
	}

	apiKey, installation, err := s.apiKeys.GetActiveByHashWithInstallation(ctx, keyHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.cache.Set(keyHash, CachedKey{Negative: true})
			return nil, nil, nil, nil, ErrInvalidToken
		}
		return nil, nil, nil, nil, err
	}

	if apiKey.Scope.Normalized() != ScopeRouting {
		return nil, nil, nil, nil, ErrWrongKeyScope
	}

	var externalKeys []*ExternalAPIKey
	cacheable := true
	if includeUpstreamKeys && s.externalKeys != nil {
		externalKeys, err = s.externalKeys.GetForInstallation(ctx, apiKey.InstallationID)
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to fetch external API keys", "installation_id", apiKey.InstallationID, "err", err)
			cacheable = false
		}
	}

	var clusterModelLists []ClusterModelList
	if s.clusterModelLists != nil {
		clusterModelLists, err = s.clusterModelLists.GetForAPIKey(ctx, apiKey.ID)
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to fetch cluster model lists", "api_key_id", apiKey.ID, "err", err)
			clusterModelLists = nil
			cacheable = false
		}
	}

	if cacheable && includeUpstreamKeys {
		s.cache.Set(keyHash, CachedKey{APIKey: apiKey, Installation: installation, ExternalKeys: externalKeys, ClusterModelLists: clusterModelLists})
	}
	s.fireMarkUsed(ctx, apiKey, installation)
	s.fireMarkFirstRequestServed(ctx, apiKey.InstallationID, installation)
	return installation, apiKey, s.resolveUpstreamSecrets(ctx, externalKeys), clusterModelLists, nil
}

func (s *Service) ResolveAndStashUser(ctx context.Context, installationID, email, claudeAccountUUID, displayName string) context.Context {
	log := observability.FromContext(ctx)
	if s.users == nil || installationID == "" {
		log.Info("ResolveAndStashUser bailout", "reason", "nil_users_or_empty_inst", "users_nil", s.users == nil, "inst_empty", installationID == "")
		return ctx
	}
	if email == "" && claudeAccountUUID == "" {
		log.Info("ResolveAndStashUser bailout", "reason", "no_identity_signal", "installation_id", installationID)
		return ctx
	}

	identityKey := userIdentityKey(email, claudeAccountUUID)
	if cached, ok := s.userCache.Get(installationID, identityKey); ok {
		log.Debug("ResolveAndStashUser cache hit", "installation_id", installationID, "user_id", cached)
		return s.withUserSettings(ctx, installationID, cached)
	}

	var namePtr *string
	if displayName != "" {
		namePtr = &displayName
	}

	log.Debug("ResolveAndStashUser upsert", "installation_id", installationID, "email_present", email != "", "account_present", claudeAccountUUID != "", "name_present", namePtr != nil)
	var user *User
	var err error
	if email != "" {
		var accountPtr *string
		if claudeAccountUUID != "" {
			accountPtr = &claudeAccountUUID
		}
		user, err = s.users.UpsertByEmail(ctx, UpsertUserParams{
			InstallationID:    installationID,
			Email:             email,
			ClaudeAccountUUID: accountPtr,
			DisplayName:       namePtr,
		})
	} else {
		user, err = s.users.UpsertByAccountUUID(ctx, UpsertUserByAccountUUIDParams{
			InstallationID:    installationID,
			ClaudeAccountUUID: claudeAccountUUID,
			DisplayName:       namePtr,
		})
	}
	if err != nil {
		observability.FromContext(ctx).Warn(
			"Failed to resolve router user",
			"installation_id", installationID,
			"err", err,
		)
		return ctx
	}
	s.userCache.Set(installationID, identityKey, user.ID)
	log.Debug("ResolveAndStashUser upsert ok", "installation_id", installationID, "user_id", user.ID)
	return s.withUserSettings(ctx, installationID, user.ID)
}

func (s *Service) ResolveAndStashKeyOwner(ctx context.Context, installationID, credentialSubjectID, displayName string) context.Context {
	var optionalName *string
	if displayName != "" {
		optionalName = &displayName
	}
	return s.resolveAndStashScopedUser(ctx, installationID, "subject:"+credentialSubjectID, func() (*User, error) {
		return s.users.UpsertByCredentialSubject(ctx, UpsertUserByCredentialSubjectParams{
			InstallationID:      installationID,
			CredentialSubjectID: credentialSubjectID,
			DisplayName:         optionalName,
		})
	})
}

func (s *Service) withUserSettings(ctx context.Context, installationID, routerUserID string) context.Context {
	ctx = s.withUserClusterLists(ctx, installationID, routerUserID)
	if RoutingPolicyFrom(ctx).Mode != "" && RoutingPolicyFrom(ctx).Mode != RoutingPolicyInherit {
		return ctx
	}
	return s.withBlindExperiment(ctx, installationID, routerUserID)
}

func (s *Service) withUserClusterLists(ctx context.Context, installationID, routerUserID string) context.Context {
	ctx = context.WithValue(ctx, UserIDContextKey{}, routerUserID)
	if s.userClusterModelLists == nil || routerUserID == "" {
		return ctx
	}
	lists, ok := s.userClusterCache.Get(routerUserID)
	if !ok {
		fetched, err := s.userClusterModelLists.GetForUser(ctx, routerUserID)
		if err != nil {
			observability.FromContext(ctx).Warn("Failed to fetch user cluster model lists", "router_user_id", routerUserID, "err", err)
			return ctx
		}
		lists = fetched
		s.userClusterCache.Set(installationID, routerUserID, lists)
	}
	if len(lists) == 0 {
		return ctx
	}
	overrides := make(map[string][]string, len(lists))
	for _, l := range lists {
		if len(l.Models) == 0 {
			continue
		}
		overrides[l.ClusterLabel] = l.Models
	}
	if len(overrides) == 0 {
		return ctx
	}
	return context.WithValue(ctx, UserClusterModelListsContextKey{}, overrides)
}

func (s *Service) withBlindExperiment(ctx context.Context, installationID, routerUserID string) context.Context {
	if s.blindExperiments == nil || routerUserID == "" {
		return ctx
	}
	var (
		state                BlindExperimentState
		hasFetched, resolved bool
		generation           uint64
	)
	for attempt := 0; attempt < blindExperimentFetchAttempts && !resolved; attempt++ {
		generation = s.blindExperimentCache.InstallationGeneration(installationID)
		if cached, found := s.blindExperimentCache.GetAtGeneration(installationID, routerUserID, generation); found {
			state, resolved = cached, true
			break
		}
		fetched, ok := s.fetchBlindExperiment(ctx, installationID, routerUserID, generation)
		if !ok {
			break
		}
		state, hasFetched = fetched, true
		if !s.blindExperimentCache.Enabled() {
			resolved = true
			break
		}
		if current, found := s.blindExperimentCache.GetAtGeneration(installationID, routerUserID, generation); found {
			state, resolved = current, true
		}
	}
	if !resolved {
		if !hasFetched || ctx.Err() != nil {
			return ctx
		}
		observability.FromContext(ctx).Warn("Blind router experiment assignment could not be confirmed after invalidation; using the last read uncached", "installation_id", installationID, "router_user_id", routerUserID, "fetch_generation", generation, "current_generation", s.blindExperimentCache.InstallationGeneration(installationID))
	}
	state = state.AtTime(s.now())
	if !state.Active && state.CohortExperimentID == "" {
		return ctx
	}
	return context.WithValue(ctx, BlindExperimentContextKey{}, state)
}

func (s *Service) fetchBlindExperiment(ctx context.Context, installationID, routerUserID string, generation uint64) (BlindExperimentState, bool) {
	fetchKey := installationID + "\x00" + routerUserID + "\x00" + strconv.FormatUint(generation, 10)
	resultCh := s.blindExperimentFetches.DoChan(fetchKey, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), blindExperimentFetchTimeout)
		defer cancel()
		if cached, found := s.blindExperimentCache.GetAtGeneration(installationID, routerUserID, generation); found {
			return cached, nil
		}
		record, fetchErr := s.blindExperiments.GetForUser(fetchCtx, installationID, routerUserID)
		if fetchErr != nil {
			s.blindExperimentCache.SetErrorAtGeneration(installationID, routerUserID, generation)
			return nil, fetchErr
		}
		resolved := resolveBlindExperiment(record, routerUserID)
		s.blindExperimentCache.SetAtGeneration(installationID, routerUserID, resolved, generation)
		return resolved, nil
	})
	var result singleflight.Result
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		return BlindExperimentState{}, false
	}
	state, ok := result.Val.(BlindExperimentState)
	if !ok {
		observability.FromContext(ctx).Warn("Failed to fetch blind router experiment assignment", "router_user_id", routerUserID, "err", result.Err, "value_type", fmt.Sprintf("%T", result.Val))
	}
	return state, ok
}

func userIdentityKey(email, claudeAccountUUID string) string {
	if email != "" {
		return "email:" + email
	}
	return "account:" + claudeAccountUUID
}

func (s *Service) fireMarkUsed(ctx context.Context, apiKey *APIKey, installation *Installation) {
	if apiKey == nil {
		return
	}
	log := observability.Get().With("api_key_id", apiKey.ID)
	claim := s.keyUsageMarks.claimKey(apiKey, s.now())
	if claim == nil {
		return
	}
	observability.SafeGoContext(ctx, log, 2*time.Second, "fireMarkUsed", func(ctx context.Context) {
		firstUse, err := s.apiKeys.MarkUsed(ctx, apiKey.ID)
		if err != nil {
			s.keyUsageMarks.release(apiKey.ID, claim)
			log.Warn("Failed to mark router api key used", "err", err)
			return
		}
		if firstUse && s.onboarding != nil && installation != nil && apiKey.Scope.Normalized() == ScopeRouting {
			s.onboarding.APIKeyFirstUsed(APIKeyFirstUsedEvent{
				InstallationExternalID: installation.ExternalID,
				CredentialSubjectID:    apiKey.CredentialSubjectID,
				APIKeyID:               apiKey.ID,
				Harness:                apiKey.Harness,
				OccurredAt:             s.now(),
			})
		}
	})
}
