package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/cluster"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAPIKeyRepository is a local in-memory auth.APIKeyRepository (the one in
// package auth_test isn't exported). Only implements methods used by these handlers.
type fakeAPIKeyRepository struct {
	mu     sync.Mutex
	keys   []*auth.APIKey
	nextID int
}

func (f *fakeAPIKeyRepository) Create(_ context.Context, params auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	key := &auth.APIKey{
		ID:             fmt.Sprintf("key-%d", f.nextID),
		InstallationID: params.InstallationID,
		ExternalID:     params.ExternalID,
		Name:           params.Name,
		KeyPrefix:      params.KeyPrefix,
		KeyHash:        params.KeyHash,
		KeySuffix:      params.KeySuffix,
		Scope:          params.Scope,
		CreatedBy:      params.CreatedBy,
		CreatedAt:      time.Now(),
	}
	f.keys = append(f.keys, key)
	return key, nil
}

func (f *fakeAPIKeyRepository) GetActiveByHashWithInstallation(context.Context, string) (*auth.APIKey, *auth.Installation, error) {
	return nil, nil, fmt.Errorf("not used by these tests")
}

func (f *fakeAPIKeyRepository) ListForInstallation(_ context.Context, installationID string) ([]*auth.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*auth.APIKey, 0, len(f.keys))
	for _, k := range f.keys {
		if k.InstallationID == installationID && k.DeletedAt == nil {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *fakeAPIKeyRepository) MarkUsed(context.Context, string) error { return nil }

func (f *fakeAPIKeyRepository) SoftDelete(_ context.Context, installationID, id string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.ID == id && k.InstallationID == installationID && k.DeletedAt == nil {
			now := time.Now()
			k.DeletedAt = &now
			return 1, nil
		}
	}
	return 0, nil
}

func (f *fakeAPIKeyRepository) softDeletedSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, k := range f.keys {
		if k.DeletedAt != nil {
			out = append(out, k.ID)
		}
	}
	return out
}

const testInstallationID = "inst-1"

// apiKeysEngine mirrors upsertKeyEngine with the key-lifecycle routes.
func apiKeysEngine(svc *auth.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	inject := func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: testInstallationID})
	}
	engine.GET("/admin/v1/keys", inject, admin.ListAPIKeysHandler(svc))
	engine.POST("/admin/v1/keys", inject, admin.IssueAPIKeyHandler(svc))
	engine.POST("/admin/v1/keys/:id/rotate", inject, admin.RotateAPIKeyHandler(svc))
	engine.GET("/admin/v1/provider-keys", inject, admin.ListExternalKeysHandler(svc))
	return engine
}

func newAuthServiceForKeyTests(apiKeys auth.APIKeyRepository, externalKeys auth.ExternalAPIKeyRepository) *auth.Service {
	return auth.NewService(nil, apiKeys, externalKeys, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) })
}

func TestListAPIKeysHandler_ReturnsKeysForInstallation(t *testing.T) {
	repo := &fakeAPIKeyRepository{}
	svc := newAuthServiceForKeyTests(repo, nil)
	_, _, err := svc.IssueAPIKey(context.Background(), testInstallationID, nil, nil)
	require.NoError(t, err)
	// A key on a different installation must not leak into this installation's list.
	_, _, err = svc.IssueAPIKey(context.Background(), "other-inst", nil, nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/keys", nil)
	apiKeysEngine(svc).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Keys, 1, "only the requesting installation's keys must be returned")
}

func TestIssueAPIKeyHandler_ReturnsTokenMatchingFingerprint(t *testing.T) {
	repo := &fakeAPIKeyRepository{}
	svc := newAuthServiceForKeyTests(repo, nil)

	body, _ := json.Marshal(map[string]string{"name": "ci-key"})
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	apiKeysEngine(svc).ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var resp struct {
		Key struct {
			ID        string  `json:"id"`
			Name      *string `json:"name"`
			KeyPrefix string  `json:"key_prefix"`
			KeySuffix string  `json:"key_suffix"`
		} `json:"key"`
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Token)
	require.NotNil(t, resp.Key.Name)
	assert.Equal(t, "ci-key", *resp.Key.Name)

	_, wantPrefix, wantSuffix := auth.APITokenFingerprint(resp.Token)
	assert.Equal(t, wantPrefix, resp.Key.KeyPrefix,
		"the returned key_prefix must match the fingerprint derived from the raw token")
	assert.Equal(t, wantSuffix, resp.Key.KeySuffix,
		"the returned key_suffix must match the fingerprint derived from the raw token")
	assert.True(t, auth.HasAPIKeyPrefix(resp.Token),
		"issued router keys must carry the rk_ prefix")
}

func TestIssueAPIKeyHandler_ScopeSelectsCredentialKind(t *testing.T) {
	tests := []struct {
		name       string
		bodyScope  string
		wantScope  string
		wantPrefix string
	}{
		{"defaults to routing", "", "routing", auth.APIKeyPrefix + "_"},
		{"analytics", "analytics_read", "analytics_read", auth.AnalyticsAPIKeyPrefix + "_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newAuthServiceForKeyTests(&fakeAPIKeyRepository{}, nil)

			body, _ := json.Marshal(map[string]string{"scope": tt.bodyScope})
			req := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			apiKeysEngine(svc).ServeHTTP(rec, req)

			require.Equal(t, http.StatusCreated, rec.Code)
			var resp struct {
				Key struct {
					Scope string `json:"scope"`
				} `json:"key"`
				Token string `json:"token"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantScope, resp.Key.Scope)
			assert.True(t, strings.HasPrefix(resp.Token, tt.wantPrefix),
				"token %q must be fronted by %q", resp.Token, tt.wantPrefix)
		})
	}
}

func TestIssueAPIKeyHandler_RejectsUnknownScope(t *testing.T) {
	svc := newAuthServiceForKeyTests(&fakeAPIKeyRepository{}, nil)

	body, _ := json.Marshal(map[string]string{"scope": "admin"})
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	apiKeysEngine(svc).ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code,
		"an unrecognized scope must fail loudly rather than quietly minting a routing key")
}

// A rotated analytics key must come back as an analytics key; carrying forward
// only the name would hand the ETL job a spend-capable credential.
func TestRotateAPIKeyHandler_PreservesScope(t *testing.T) {
	svc := newAuthServiceForKeyTests(&fakeAPIKeyRepository{}, nil)
	oldKey, _, err := svc.IssueScopedAPIKey(context.Background(), testInstallationID, auth.ScopeAnalyticsRead, nil, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/keys/"+oldKey.ID+"/rotate", nil)
	rec := httptest.NewRecorder()
	apiKeysEngine(svc).ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var resp struct {
		Key struct {
			Scope string `json:"scope"`
		} `json:"key"`
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, string(auth.ScopeAnalyticsRead), resp.Key.Scope)
	assert.True(t, strings.HasPrefix(resp.Token, auth.AnalyticsAPIKeyPrefix+"_"))
}

func TestRotateAPIKeyHandler_SoftDeletesOldKeyAndIssuesNew(t *testing.T) {
	repo := &fakeAPIKeyRepository{}
	svc := newAuthServiceForKeyTests(repo, nil)
	oldKey, _, err := svc.IssueAPIKey(context.Background(), testInstallationID, nil, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/keys/"+oldKey.ID+"/rotate", nil)
	rec := httptest.NewRecorder()
	apiKeysEngine(svc).ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var resp struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEqual(t, oldKey.ID, resp.Key.ID, "rotation must issue a new key id")
	assert.NotEmpty(t, resp.Token)
	assert.Contains(t, repo.softDeletedSnapshot(), oldKey.ID,
		"rotation must soft-delete the old key in the repository")
}

func TestRotateAPIKeyHandler_ForeignKeyIDReturnsNotFound(t *testing.T) {
	repo := &fakeAPIKeyRepository{}
	svc := newAuthServiceForKeyTests(repo, nil)
	// Key belongs to a different installation than the one injected by apiKeysEngine.
	foreignKey, _, err := svc.IssueAPIKey(context.Background(), "other-inst", nil, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/keys/"+foreignKey.ID+"/rotate", nil)
	rec := httptest.NewRecorder()
	apiKeysEngine(svc).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code,
		"rotating a key id owned by a foreign installation must map auth.ErrAPIKeyNotFound to 404")
	assert.Empty(t, repo.softDeletedSnapshot(),
		"a rejected rotation must not soft-delete the foreign key")
}

func TestListExternalKeysHandler_ReturnsProviderKeysForInstallation(t *testing.T) {
	externalKey := &auth.ExternalAPIKey{
		ID:             "ext-1",
		InstallationID: testInstallationID,
		Provider:       "anthropic",
		KeyPrefix:      "sk-a",
		KeySuffix:      "test",
	}
	repo := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{externalKey}}
	svc := newAuthServiceForKeyTests(&fakeAPIKeyRepository{}, repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/provider-keys", nil)
	rec := httptest.NewRecorder()
	apiKeysEngine(svc).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Keys []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Keys, 1)
	assert.Equal(t, "ext-1", body.Keys[0].ID)
	assert.Equal(t, "anthropic", body.Keys[0].Provider)
}

// fakeExternalAPIKeyRepo records whether a key was actually persisted, so a test
// can distinguish "guard rejected the write" from "write went through".
type fakeExternalAPIKeyRepo struct {
	created               int
	createdBase           *string
	createdAliases        map[string]string
	createdIdentityHeader string
	softDeletedByProvider int
	keys                  []*auth.ExternalAPIKey
}

func (f *fakeExternalAPIKeyRepo) Create(_ context.Context, params auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	f.created++
	f.createdBase = params.BaseURL
	f.createdAliases = params.ModelAliases
	key := &auth.ExternalAPIKey{ID: params.ExternalID, Provider: params.Provider, ModelAliases: params.ModelAliases}
	if params.IdentityHeader != nil {
		f.createdIdentityHeader = *params.IdentityHeader
		key.IdentityHeader = *params.IdentityHeader
	}
	if params.IdentityHeaderFormat != nil {
		key.IdentityHeaderFormat = *params.IdentityHeaderFormat
	}
	if params.BaseURL != nil {
		key.BaseURL = *params.BaseURL
	}
	return key, nil
}
func (f *fakeExternalAPIKeyRepo) GetForInstallation(_ context.Context, installationID string) ([]*auth.ExternalAPIKey, error) {
	out := make([]*auth.ExternalAPIKey, 0, len(f.keys))
	for _, k := range f.keys {
		if k.InstallationID == installationID {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeExternalAPIKeyRepo) SoftDeleteByProvider(context.Context, string, string) error {
	f.softDeletedByProvider++
	return nil
}
func (f *fakeExternalAPIKeyRepo) SoftDelete(context.Context, string, string) error { return nil }
func (f *fakeExternalAPIKeyRepo) UpdateModelAliases(_ context.Context, installationID, id string, aliases map[string]string) (*auth.ExternalAPIKey, error) {
	for _, k := range f.keys {
		if k.ID == id && k.InstallationID == installationID {
			k.ModelAliases = aliases
			return k, nil
		}
	}
	return nil, auth.ErrExternalAPIKeyNotFound
}
func (f *fakeExternalAPIKeyRepo) MarkUsed(context.Context, string) error { return nil }

// upsertKeyEngine wires UpsertExternalKeyHandler behind a middleware that injects
// an already-authed installation, so the handler reaches the env-shadow guard
// without a real auth flow.
func upsertKeyEngine(svc *auth.Service) *gin.Engine {
	return upsertKeyEngineWithModels(svc, nil)
}

func upsertKeyEngineWithModels(svc *auth.Service, models admin.DeployedModelsSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/admin/v1/provider-keys", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
	}, admin.UpsertExternalKeyHandler(svc, models))
	return engine
}

func postProviderKey(engine *gin.Engine, provider string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"provider": provider, "key": "sk-test-key"})
	return postProviderKeyBody(engine, body)
}

func postProviderKeyWithBaseURL(engine *gin.Engine, provider, baseURL string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"provider": provider, "key": "sk-test-key", "base_url": baseURL})
	return postProviderKeyBody(engine, body)
}

func postProviderKeyBody(engine *gin.Engine, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/provider-keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func newUpsertKeyService(repo auth.ExternalAPIKeyRepository) *auth.Service {
	return auth.NewService(nil, nil, repo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) })
}

func TestUpsertExternalKeyHandler_RejectsEnvShadowedProvider(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "sk-ant-deployment-key")

	repo := &fakeExternalAPIKeyRepo{}
	svc := auth.NewService(nil, nil, repo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) })

	rec := postProviderKey(upsertKeyEngine(svc), providers.ProviderAnthropic)

	assert.Equal(t, http.StatusConflict, rec.Code,
		"a provider with a deployment env key must not accept a dashboard BYOK key")
	assert.Equal(t, 0, repo.created,
		"the BYOK key must not be persisted when the env guard fires")
}

func TestUpsertExternalKeyHandler_AllowsProviderWithoutEnvKey(t *testing.T) {
	// No env var set for the provider — the guard must let the write through.
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")

	repo := &fakeExternalAPIKeyRepo{}
	svc := auth.NewService(nil, nil, repo, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) })

	rec := postProviderKey(upsertKeyEngine(svc), providers.ProviderAnthropic)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, 1, repo.created, "the BYOK key must be persisted when no env key shadows it")
}

func TestUpsertExternalKeyHandler_PersistsAndReturnsBaseURL(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropicGateway), "")

	repo := &fakeExternalAPIKeyRepo{}
	rec := postProviderKeyWithBaseURL(
		upsertKeyEngine(newUpsertKeyService(repo)),
		providers.ProviderAnthropicGateway,
		"https://gateway.example.com/llm/",
	)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, repo.createdBase, "base_url must reach the repository — dropping it is what made BYOK gateways unreachable")
	assert.Equal(t, "https://gateway.example.com/llm", *repo.createdBase,
		"the trailing slash must be trimmed: providers append their own /v1/messages path")

	var body struct {
		BaseURL string `json:"base_url"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "https://gateway.example.com/llm", body.BaseURL,
		"the response must echo the stored endpoint so the dashboard can show where the key points")
}

func TestUpsertExternalKeyHandler_RejectsGatewayKeyWithoutBaseURL(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropicGateway), "")

	repo := &fakeExternalAPIKeyRepo{}
	rec := postProviderKey(upsertKeyEngine(newUpsertKeyService(repo)), providers.ProviderAnthropicGateway)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"a gateway has no default endpoint, so a key without one could never be dispatched")
	assert.Equal(t, 0, repo.created, "the undispatchable key must not be persisted")
}

func TestUpsertExternalKeyHandler_RejectsGatewayKeyWithSlashOnlyBaseURL(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropicGateway), "")

	repo := &fakeExternalAPIKeyRepo{}
	rec := postProviderKeyWithBaseURL(
		upsertKeyEngine(newUpsertKeyService(repo)),
		providers.ProviderAnthropicGateway,
		"///",
	)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"a value that normalizes away to nothing leaves the same undispatchable key as omitting it")
	assert.Equal(t, 0, repo.created)
	assert.Equal(t, 0, repo.softDeletedByProvider,
		"a rejected upsert must not take out the working key it would have replaced")
}

func TestUpsertExternalKeyHandler_RejectsRelativeBaseURL(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")

	repo := &fakeExternalAPIKeyRepo{}
	rec := postProviderKeyWithBaseURL(
		upsertKeyEngine(newUpsertKeyService(repo)),
		providers.ProviderAnthropic,
		"gateway.example.com",
	)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"a scheme-less base URL yields an unroutable upstream URL, so it must fail at write time, not at request time")
	assert.Equal(t, 0, repo.created)
}

func postProviderKeyWithAliases(engine *gin.Engine, provider string, aliases map[string]string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"provider": provider, "key": "sk-test-key", "model_aliases": aliases})
	return postProviderKeyBody(engine, body)
}

func TestUpsertExternalKeyHandler_PersistsAndReturnsModelAliases(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")

	repo := &fakeExternalAPIKeyRepo{}
	models := fakeDeployedModels{entries: []cluster.DeployedEntry{
		{Model: "claude-fable-5", Provider: providers.ProviderAnthropic},
	}}
	rec := postProviderKeyWithAliases(
		upsertKeyEngineWithModels(newUpsertKeyService(repo), models),
		providers.ProviderAnthropic,
		map[string]string{"claude-fable-5": " gateway-claude-fable-5 "},
	)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, map[string]string{"claude-fable-5": "gateway-claude-fable-5"}, repo.createdAliases,
		"the alias must reach the repository trimmed, or the endpoint gets a padded model name it can't match")

	var body struct {
		ModelAliases map[string]string `json:"model_aliases"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, map[string]string{"claude-fable-5": "gateway-claude-fable-5"}, body.ModelAliases,
		"the response must echo the stored aliases so the dashboard can show the mapping")
}

func TestUpsertExternalKeyHandler_RejectsAliasForUnknownModel(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")

	repo := &fakeExternalAPIKeyRepo{}
	models := fakeDeployedModels{entries: []cluster.DeployedEntry{
		{Model: "claude-fable-5", Provider: providers.ProviderAnthropic},
	}}
	rec := postProviderKeyWithAliases(
		upsertKeyEngineWithModels(newUpsertKeyService(repo), models),
		providers.ProviderAnthropic,
		map[string]string{"claude-fabel-5": "gateway-claude-fable-5"},
	)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"a typo'd catalog id would silently never match a routed model, so it must fail at write time")
	assert.Equal(t, 0, repo.created)
	assert.Equal(t, 0, repo.softDeletedByProvider,
		"a rejected upsert must not take out the working key it would have replaced")
}

func postProviderKeyWithIdentityHeader(engine *gin.Engine, provider, header, format string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{
		"provider": provider, "key": "sk-test-key",
		"identity_header": header, "identity_header_format": format,
	})
	return postProviderKeyBody(engine, body)
}

func TestUpsertExternalKeyHandler_PersistsAndReturnsIdentityHeader(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")

	repo := &fakeExternalAPIKeyRepo{}
	rec := postProviderKeyWithIdentityHeader(
		upsertKeyEngine(newUpsertKeyService(repo)),
		providers.ProviderAnthropic, " X-Caller-Identity ", "JSON",
	)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "X-Caller-Identity", repo.createdIdentityHeader)

	var body struct {
		IdentityHeader       string `json:"identity_header"`
		IdentityHeaderFormat string `json:"identity_header_format"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "X-Caller-Identity", body.IdentityHeader)
	assert.Equal(t, "json", body.IdentityHeaderFormat,
		"the response must echo the stored config so the dashboard can show what the endpoint receives")
}

func TestUpsertExternalKeyHandler_RejectsReservedIdentityHeader(t *testing.T) {
	t.Setenv(providers.APIKeyEnvVar(providers.ProviderAnthropic), "")

	repo := &fakeExternalAPIKeyRepo{}
	rec := postProviderKeyWithIdentityHeader(
		upsertKeyEngine(newUpsertKeyService(repo)),
		providers.ProviderAnthropic, "Authorization", "email",
	)

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"forwarding identity into Authorization would overwrite the upstream credential")
	assert.Equal(t, 0, repo.created)
	assert.Equal(t, 0, repo.softDeletedByProvider,
		"a rejected upsert must not take out the working key it would have replaced")
}

func aliasUpdateEngine(svc *auth.Service, models admin.DeployedModelsSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.PUT("/admin/v1/provider-keys/:id/model-aliases", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
	}, admin.UpdateExternalKeyAliasesHandler(svc, models))
	return engine
}

func putModelAliases(engine *gin.Engine, id string, aliases map[string]string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"model_aliases": aliases})
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/provider-keys/"+id+"/model-aliases", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestUpdateExternalKeyAliasesHandler_ReplacesAliasesWithoutTheSecret(t *testing.T) {
	repo := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{
		{ID: "ext-1", InstallationID: "inst-1", Provider: providers.ProviderOpenAIGateway},
	}}
	models := fakeDeployedModels{entries: []cluster.DeployedEntry{{Model: "gpt-5", Provider: providers.ProviderOpenAI}}}

	rec := putModelAliases(aliasUpdateEngine(newUpsertKeyService(repo), models), "ext-1", map[string]string{"gpt-5": "openai-gpt-5"})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]string{"gpt-5": "openai-gpt-5"}, repo.keys[0].ModelAliases,
		"editing aliases must not require re-entering a credential the dashboard can't show")
	var body struct {
		ModelAliases map[string]string `json:"model_aliases"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, map[string]string{"gpt-5": "openai-gpt-5"}, body.ModelAliases)
}

func TestUpdateExternalKeyAliasesHandler_RejectsUnknownCatalogModel(t *testing.T) {
	repo := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{
		{ID: "ext-1", InstallationID: "inst-1", Provider: providers.ProviderOpenAIGateway},
	}}
	models := fakeDeployedModels{entries: []cluster.DeployedEntry{{Model: "gpt-5", Provider: providers.ProviderOpenAI}}}

	rec := putModelAliases(aliasUpdateEngine(newUpsertKeyService(repo), models), "ext-1", map[string]string{"gpt-6": "openai-gpt-6"})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, repo.keys[0].ModelAliases, "a typo'd catalog id must fail at write time, not silently at runtime")
}

func TestUpdateExternalKeyAliasesHandler_ForeignKeyIsNotFound(t *testing.T) {
	repo := &fakeExternalAPIKeyRepo{keys: []*auth.ExternalAPIKey{
		{ID: "ext-1", InstallationID: "inst-2", Provider: providers.ProviderOpenAIGateway},
	}}
	models := fakeDeployedModels{entries: []cluster.DeployedEntry{{Model: "gpt-5", Provider: providers.ProviderOpenAI}}}

	rec := putModelAliases(aliasUpdateEngine(newUpsertKeyService(repo), models), "ext-1", map[string]string{"gpt-5": "openai-gpt-5"})

	assert.Equal(t, http.StatusNotFound, rec.Code, "another tenant's key must read as absent, not as a server error")
	assert.Nil(t, repo.keys[0].ModelAliases)
}
