package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"workweave/router/internal/observability"
	"workweave/router/internal/providers"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// ErrUnknownModel is returned when a requested model ID is not in the caller-supplied allowed set.
var ErrUnknownModel = errors.New("auth: unknown model id")

// ErrUnknownProvider is returned when a requested provider name is not in the caller-supplied allowed set.
var ErrUnknownProvider = errors.New("auth: unknown provider")

// ErrInvalidBaseURL is returned for a BYOK endpoint override that is not an absolute http(s) URL.
var ErrInvalidBaseURL = errors.New("auth: invalid base url")

// ErrBaseURLRequired is returned when a provider with no deployment endpoint is given no base URL.
var ErrBaseURLRequired = errors.New("auth: base url required for provider")

// ErrInvalidModelAlias is returned for a model alias map that is oversized or malformed.
var ErrInvalidModelAlias = errors.New("auth: invalid model alias")

// ErrInvalidIdentityHeader is returned for an identity-forwarding header that is
// unnamed, reserved, or in an unknown format.
var ErrInvalidIdentityHeader = errors.New("auth: invalid identity header")

type Clock func() time.Time

// InstallationChangeNotifier fans out installation-change events to peer replicas.
// Fire-and-forget: implementations must not block the caller.
type InstallationChangeNotifier interface {
	NotifyInstallationChanged(installationID string)
}

// NoOpInstallationChangeNotifier is the Null Object when no cross-replica fanout is configured.
type NoOpInstallationChangeNotifier struct{}

// NotifyInstallationChanged is a no-op.
func (NoOpInstallationChangeNotifier) NotifyInstallationChanged(string) {}

// Service authenticates incoming bearer tokens. Identity only; routing/dispatch lives in proxy.Service.
type Service struct {
	installations     InstallationRepository
	apiKeys           APIKeyRepository
	externalKeys      ExternalAPIKeyRepository
	users             UserRepository
	clusterModelLists ClusterModelListRepository
	cache             APIKeyCache
	userCache         UserCache
	notifier          InstallationChangeNotifier
	now               Clock
	encryptor         Encryptor

	// adminPassword and adminSessionKey are empty when admin login is disabled.
	adminPassword   string
	adminSessionKey []byte

	// adminLoginFailures throttles per-IP brute-force login attempts.
	adminLoginFailures *expirable.LRU[string, int]
	adminLoginMu       sync.Mutex
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
		installations: installations,
		apiKeys:       apiKeys,
		externalKeys:  externalKeys,
		users:         users,
		cache:         cache,
		userCache:     userCache,
		notifier:      NoOpInstallationChangeNotifier{},
		now:           now,
		encryptor:     NoOpEncryptor{},
	}
}

func (s *Service) WithEncryptor(e Encryptor) *Service {
	s.encryptor = e
	return s
}

// WithClusterModelLists wires the per-key per-cluster allowlist repo. When
// unset, VerifyAPIKey returns no cluster lists and routing keeps artifact
// defaults. Kept off NewService so existing callers/tests stay source-stable.
func (s *Service) WithClusterModelLists(repo ClusterModelListRepository) *Service {
	s.clusterModelLists = repo
	return s
}

// WithInstallationChangeNotifier wires a cross-replica fanout. Pass nil to disable.
func (s *Service) WithInstallationChangeNotifier(n InstallationChangeNotifier) *Service {
	if n == nil {
		s.notifier = NoOpInstallationChangeNotifier{}
		return s
	}
	s.notifier = n
	return s
}

// invalidateInstallation evicts the local cache and fans out to peer replicas.
// Always called after a successful DB commit so listeners observe the new state.
func (s *Service) invalidateInstallation(installationID string) {
	if installationID == "" {
		return
	}
	s.cache.InvalidateInstallation(installationID)
	s.notifier.NotifyInstallationChanged(installationID)
}

// IssueAPIKey creates a new routing (data-plane) API key and returns the raw token.
func (s *Service) IssueAPIKey(ctx context.Context, installationID string, name *string, createdBy *string) (*APIKey, string, error) {
	return s.IssueScopedAPIKey(ctx, installationID, ScopeRouting, name, createdBy)
}

// IssueScopedAPIKey creates a new router API key under the given scope and
// returns the raw token. The token prefix follows the scope, so the credential
// advertises its own authority.
func (s *Service) IssueScopedAPIKey(ctx context.Context, installationID string, scope APIKeyScope, name *string, createdBy *string) (*APIKey, string, error) {
	if !scope.Valid() {
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

// ListAPIKeys returns all active API keys for an installation.
func (s *Service) ListAPIKeys(ctx context.Context, installationID string) ([]*APIKey, error) {
	return s.apiKeys.ListForInstallation(ctx, installationID)
}

// RotateAPIKey soft-deletes the named key and issues a replacement under the
// same installation, carrying forward its name. Returns ErrAPIKeyNotFound if
// keyID isn't an active key owned by installationID, or if SoftDelete matches
// 0 rows: a concurrent rotate or delete already transitioned the key, so
// minting a successor would leave an untracked credential.
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
	if target == nil {
		return nil, "", ErrAPIKeyNotFound
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

// DeleteAPIKey soft-deletes an API key and invalidates the installation's
// cache entry on this replica and all peers, so the key doesn't stay usable
// for the remainder of the positive cache TTL (5 min). The rows-affected
// count is intentionally ignored: a 0-row no-op stays idempotent success.
func (s *Service) DeleteAPIKey(ctx context.Context, installationID, id string) error {
	if _, err := s.apiKeys.SoftDelete(ctx, installationID, id); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

// ListExternalAPIKeys returns all active provider API keys for an installation.
func (s *Service) ListExternalAPIKeys(ctx context.Context, installationID string) ([]*ExternalAPIKey, error) {
	return s.externalKeys.GetForInstallation(ctx, installationID)
}

// UpsertExternalAPIKeyParams carries one BYOK key's stored configuration plus
// AllowedModels, the caller-supplied set the aliases are validated against.
type UpsertExternalAPIKeyParams struct {
	Provider string
	RawKey   string
	Name     *string
	// BaseURL overrides the provider's deployment endpoint; nil keeps the default.
	BaseURL *string
	// ModelAliases rewrites outbound model IDs for this key's endpoint.
	ModelAliases map[string]string
	// AllowedModels is the valid catalog model ID set for alias validation; nil skips it.
	AllowedModels map[string]struct{}
	// IdentityHeader names the header the endpoint wants the caller's identity
	// in, rendered per IdentityHeaderFormat; both nil forwards nothing.
	IdentityHeader       *string
	IdentityHeaderFormat *string
	CreatedBy            *string
}

// UpsertExternalAPIKey replaces the provider's key for the installation.
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
	// Checked after normalization: a slash-only value normalizes away to nil.
	if normalizedBaseURL == nil && providers.RequiresBaseURL(provider) {
		return nil, fmt.Errorf("%w: %s", ErrBaseURLRequired, provider)
	}
	// Generate external ID first so it binds into the ciphertext as AAD.
	externalID := GenerateID("ekid")
	ciphertext, err := s.encryptor.Encrypt([]byte(rawKey), externalID, provider)
	if err != nil {
		return nil, err
	}
	if err := s.externalKeys.SoftDeleteByProvider(ctx, installationID, provider); err != nil {
		return nil, err
	}
	hash, prefix, suffix := APITokenFingerprint(rawKey)
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

		IdentityHeader:       identityHeader,
		IdentityHeaderFormat: identityFormat,
		CreatedBy:            params.CreatedBy,
	})
	if err != nil {
		return nil, err
	}
	s.invalidateInstallation(installationID)
	return key, nil
}

// SetExternalAPIKeyModelAliases replaces a BYOK key's alias map; nil allowed skips catalog validation.
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

// DeleteExternalAPIKey soft-deletes a specific provider API key.
func (s *Service) DeleteExternalAPIKey(ctx context.Context, installationID, id string) error {
	if err := s.externalKeys.SoftDelete(ctx, installationID, id); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

// SetInstallationExcludedModels replaces the per-installation model exclusion list.
// allowed is the set of valid model IDs; passing nil skips validation.
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
	// De-dupe while preserving order so the persisted list is stable.
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

// SetInstallationExcludedProviders replaces the per-installation provider exclusion list.
// allowed is the set of valid provider names; passing nil skips validation.
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
	// De-dupe while preserving order so the persisted list is stable.
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

// SetInstallationRoutingPreference persists the routing quality weight (a
// normalized fraction in [0, 1]). Passing nil clears it so the scorer reverts
// to its tuned per-cluster defaults. Invalidates the cache so the change
// applies on the next request instead of waiting out the TTL.
func (s *Service) SetInstallationRoutingPreference(ctx context.Context, externalID, installationID string, qualityWeight *float64) error {
	if err := s.installations.UpdateRoutingPreference(ctx, externalID, installationID, qualityWeight); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

// SetInstallationSubscriptionRoutingDisabled toggles subscription-aware
// routing. When true, the scorer's subscription subsidy bonus is suppressed
// so non-Claude models compete fairly. Invalidates the cache so the change
// applies on the next request instead of waiting out the TTL.
func (s *Service) SetInstallationSubscriptionRoutingDisabled(ctx context.Context, externalID, installationID string, disabled bool) error {
	if err := s.installations.UpdateSubscriptionRoutingDisabled(ctx, externalID, installationID, disabled); err != nil {
		return err
	}
	s.invalidateInstallation(installationID)
	return nil
}

// ErrInvalidCaptureMode is returned for a content-capture mode outside the
// off/hashed/full set.
var ErrInvalidCaptureMode = errors.New("auth: invalid content capture mode")

// SetInstallationContentCaptureMode persists the per-installation capture
// ceiling ("off"/"hashed"/"full"); nil clears the override. Validated in-process
// so a bad value returns ErrInvalidCaptureMode rather than a DB 500.
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

// VerifyAPIKey authenticates a raw bearer token for the data plane against the
// cache then Postgres, returning ErrInvalidPrefix/ErrInvalidToken on failure and
// ErrWrongKeyScope for a key that isn't routing-scoped. The returned
// ExternalAPIKey slice has Plaintext populated, or nil if none exist. The
// ClusterModelList slice carries the key's per-cluster ordered allowlists, or
// nil when none are configured.
func (s *Service) VerifyAPIKey(ctx context.Context, rawToken string) (*Installation, *APIKey, []*ExternalAPIKey, []ClusterModelList, error) {
	if !HasAPIKeyPrefix(rawToken) {
		return nil, nil, nil, nil, ErrInvalidPrefix
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
			s.fireMarkUsed(cached.APIKey.ID)
			return cached.Installation, cached.APIKey, cached.ExternalKeys, cached.ClusterModelLists, nil
		}
		// Malformed positive entry (nil APIKey): fall through to DB lookup.
	}

	apiKey, installation, err := s.apiKeys.GetActiveByHashWithInstallation(ctx, keyHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.cache.Set(keyHash, CachedKey{Negative: true})
			return nil, nil, nil, nil, ErrInvalidToken
		}
		return nil, nil, nil, nil, err
	}

	// Checked before the BYOK fetch: an analytics key must never cause provider
	// secrets to be decrypted, let alone reach a caller.
	if apiKey.Scope.Normalized() != ScopeRouting {
		return nil, nil, nil, nil, ErrWrongKeyScope
	}

	var externalKeys []*ExternalAPIKey
	if s.externalKeys != nil {
		externalKeys, err = s.externalKeys.GetForInstallation(ctx, apiKey.InstallationID)
		if err != nil {
			// Non-fatal: proceed without external keys.
			observability.Get().Warn("Failed to fetch external API keys", "installation_id", apiKey.InstallationID, "err", err)
		}
	}

	var clusterModelLists []ClusterModelList
	clusterModelListsFetchOK := true
	if s.clusterModelLists != nil {
		clusterModelLists, err = s.clusterModelLists.GetForAPIKey(ctx, apiKey.ID)
		if err != nil {
			// Non-fatal: serve this request with artifact-default routing, but
			// don't cache the empty result — a transient DB error would otherwise
			// disable per-key cluster restrictions for the full positive TTL.
			observability.Get().Warn("Failed to fetch cluster model lists", "api_key_id", apiKey.ID, "err", err)
			clusterModelLists = nil
			clusterModelListsFetchOK = false
		}
	}

	if clusterModelListsFetchOK {
		s.cache.Set(keyHash, CachedKey{APIKey: apiKey, Installation: installation, ExternalKeys: externalKeys, ClusterModelLists: clusterModelLists})
	}
	s.fireMarkUsed(apiKey.ID)
	return installation, apiKey, externalKeys, clusterModelLists, nil
}

// ResolveAndStashUser upserts a router user and stashes the ID on ctx. Email
// takes precedence as the lookup key; with only claudeAccountUUID, the row is
// keyed on account_uuid with NULL email. displayName is best-effort (empty
// leaves the existing value). Never fails an authenticated request — returns
// ctx unchanged on error.
func (s *Service) ResolveAndStashUser(ctx context.Context, installationID, email, claudeAccountUUID, displayName string) context.Context {
	log := observability.Get()
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
		return context.WithValue(ctx, UserIDContextKey{}, cached)
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
		observability.Get().Warn(
			"Failed to resolve router user",
			"installation_id", installationID,
			"err", err,
		)
		return ctx
	}
	s.userCache.Set(installationID, identityKey, user.ID)
	log.Debug("ResolveAndStashUser upsert ok", "installation_id", installationID, "user_id", user.ID)
	return context.WithValue(ctx, UserIDContextKey{}, user.ID)
}

func userIdentityKey(email, claudeAccountUUID string) string {
	if email != "" {
		return "email:" + email
	}
	return "account:" + claudeAccountUUID
}

// fireMarkUsed runs the last_used_at update off the request path. Uses context.Background because
// the parent ctx is often canceled (response written) before the UPDATE completes.
func (s *Service) fireMarkUsed(apiKeyID string) {
	log := observability.Get().With("api_key_id", apiKeyID)
	observability.SafeGo(log, 2*time.Second, "fireMarkUsed", func(ctx context.Context) {
		if err := s.apiKeys.MarkUsed(ctx, apiKeyID); err != nil {
			log.Warn("Failed to mark router api key used", "err", err)
		}
	})
}
