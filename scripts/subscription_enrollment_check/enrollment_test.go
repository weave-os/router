package subscription_enrollment_check_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"

	subscriptionsapi "weave-os/router/internal/api/subscriptions"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/sqlc"
)

func TestClaudeLoginReconnectsThroughAPIAndPostgres(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_DATABASE_URL is required for the enrollment end-to-end test")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname(), "use an ephemeral loopback database")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	queries := sqlc.New(tx)
	installation, err := queries.CreateModelRouterInstallation(ctx, sqlc.CreateModelRouterInstallationParams{
		ExternalID: uuid.NewString(), Name: "subscription-enrollment-fixture",
	})
	require.NoError(t, err)
	subject, err := queries.InsertCredentialSubject(ctx)
	require.NoError(t, err)
	require.NoError(t, queries.InsertCredentialSubjectInstallation(ctx, sqlc.InsertCredentialSubjectInstallationParams{
		SubjectID: subject.ID, InstallationID: installation.ID,
	}))
	keys := map[string]*auth.APIKey{}
	for _, token := range []string{"rk_fixture_first", "rk_fixture_rotated"} {
		key, createErr := queries.InsertPersonalRoutingKey(ctx, sqlc.InsertPersonalRoutingKeyParams{
			InstallationID: installation.ID, SubjectID: subject.ID, ExternalID: uuid.NewString(),
			KeyPrefix: "rk_fixture", KeySuffix: "test", KeyHash: uuid.NewString(),
		})
		require.NoError(t, createErr)
		keys[token] = &auth.APIKey{ID: key.ID.String(), CredentialSubjectID: subject.ID.String()}
	}
	owner := auth.SubscriptionOwnerForKey(keys["rk_fixture_first"])
	repo := postgres.NewSubscriptionAccountRepo(tx)
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	var keysetJSON bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&keysetJSON)))
	encryptor, err := auth.NewTinkEncryptor(keysetJSON.String())
	require.NoError(t, err)
	svc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).
		WithEncryptor(encryptor).WithSubscriptionAccounts(repo)

	const accountA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const accountB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	const organizationA = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	const organizationB = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	var exchanges atomic.Int32
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/oauth/token", func(c *gin.Context) {
		attempt := exchanges.Add(1)
		accountID := accountA
		organizationID := organizationA
		email := "account@example.test"
		if attempt == 2 {
			email = "changed@example.test"
		}
		if attempt == 3 {
			organizationID = organizationB
		}
		if attempt == 4 {
			accountID = accountB
			organizationID = organizationB
		}
		c.JSON(http.StatusOK, gin.H{
			"access_token": fmt.Sprintf("access-fixture-%d", attempt), "refresh_token": fmt.Sprintf("refresh-fixture-%d", attempt),
			"account":      gin.H{"uuid": accountID, "email_address": email},
			"organization": gin.H{"uuid": organizationID, "name": "Test Org"},
		})
	})
	group := router.Group("/v1", func(c *gin.Context) {
		key := keys[c.GetHeader(auth.RouterKeyHeader)]
		if key == nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("router_installation", &auth.Installation{ID: installation.ID.String(), ExternalID: installation.ExternalID})
		c.Set("router_api_key", key)
	})
	subscriptionsapi.Register(group, svc)
	// A subprocess does not synchronize Go memory; hand off the rollback-only
	// transaction explicitly between fixture assertions and HTTP enrollment.
	enrollmentReady := make(chan struct{}, 1)
	enrollmentDone := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/subscriptions/accounts" {
			select {
			case <-enrollmentReady:
			case <-ctx.Done():
				return
			}
			defer func() { enrollmentDone <- struct{}{} }()
		}
		router.ServeHTTP(w, r)
	}))
	defer server.Close()

	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	fixtureDir := t.TempDir()
	// OAuth is stubbed locally; never open a real browser during the fixture.
	require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, "open"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	login := func(key string) {
		t.Helper()
		command := exec.CommandContext(ctx, "python3", filepath.Join(root, "install/tests/claude_login_test.py"),
			filepath.Join(root, "install/install.sh"), "--enroll", server.URL)
		command.Env = append(os.Environ(), "HOME="+fixtureDir, "PATH="+fixtureDir+":"+os.Getenv("PATH"),
			"WEAVE_ROUTER_KEY="+key, "WEAVE_ANTHROPIC_OAUTH_TOKEN="+server.URL+"/oauth/token", "NO_COLOR=1")
		enrollmentReady <- struct{}{}
		output, runErr := command.CombinedOutput()
		require.True(t, !bytes.Contains(output, []byte("refresh-fixture-")) && !bytes.Contains(output, []byte("access-fixture-")), "CLI must not expose credentials")
		require.NoError(t, runErr, "interactive enrollment failed; output withheld to protect credentials")
		select {
		case <-enrollmentDone:
		case <-ctx.Done():
			t.Fatal("enrollment handler did not complete")
		}
	}
	login("rk_fixture_first")
	enrolled, err := repo.ListSubscriptionAccounts(ctx, owner)
	require.NoError(t, err)
	require.Len(t, enrolled, 1)
	first := enrolled[0]
	require.Equal(t, accountA+":"+organizationA, first.ExternalAccountID)
	require.Equal(t, auth.SubscriptionProviderClaude, first.Provider)
	require.Equal(t, "Test Org: account@example.test", first.DisplayName)

	leaseID := uuid.NewString()
	lease, err := svc.TryAcquireSubscriptionRefreshLease(ctx, owner, first.ID, leaseID, time.Minute)
	require.NoError(t, err)
	require.True(t, lease.Acquired)
	before, err := repo.GetSubscriptionCredentialRecord(ctx, first.ID, owner)
	require.NoError(t, err)
	require.NoError(t, svc.PersistSubscriptionTokens(ctx, owner, first.ID, leaseID, before.TokenRefreshVersion,
		[]byte("refresh-fixture-cached"), []byte("access-fixture-cached"), time.Now().Add(time.Hour)))
	lease, err = svc.TryAcquireSubscriptionRefreshLease(ctx, owner, first.ID, leaseID, time.Minute)
	require.NoError(t, err)
	require.True(t, lease.Acquired)
	before, err = repo.GetSubscriptionCredentialRecord(ctx, first.ID, owner)
	require.NoError(t, err)
	require.NotEmpty(t, before.AccessTokenCiphertext)
	require.Equal(t, leaseID, before.TokenRefreshLeaseID)
	apiRequest(t, router, http.MethodPatch, first.ID, map[string]any{
		"enabled": false, "cooldown_until": time.Now().Add(time.Hour),
	}, http.StatusNoContent)
	paused := apiAccounts(t, router)
	require.Len(t, paused, 1)
	require.False(t, paused[0].Enabled)
	require.NotNil(t, paused[0].CooldownUntil)

	login("rk_fixture_rotated")
	accounts := apiAccounts(t, router)
	require.Len(t, accounts, 1, "reconnecting the same provider identity must reuse the row")
	require.Equal(t, first.ID, accounts[0].ID)
	require.Equal(t, "Test Org: changed@example.test", accounts[0].DisplayName)
	require.True(t, accounts[0].Enabled)
	require.Nil(t, accounts[0].CooldownUntil)
	stored, err := repo.GetSubscriptionCredentialRecord(ctx, first.ID, owner)
	require.NoError(t, err)
	require.True(t, !bytes.Equal(stored.RefreshTokenCiphertext, []byte("refresh-fixture-2")), "credential must be encrypted at rest")
	latest, err := svc.SubscriptionRefreshToken(ctx, owner, first.ID)
	require.NoError(t, err)
	require.True(t, bytes.Equal(latest, []byte("refresh-fixture-2")), "reconnect must retain the latest refresh token")
	require.Empty(t, stored.AccessTokenCiphertext)
	require.Nil(t, stored.AccessTokenExpiresAt)
	require.Empty(t, stored.TokenRefreshLeaseID)
	require.Greater(t, stored.TokenRefreshVersion, before.TokenRefreshVersion)
	require.ErrorIs(t, svc.PersistSubscriptionTokens(ctx, owner, first.ID, leaseID, before.TokenRefreshVersion,
		[]byte("refresh-fixture-stale"), []byte("access-fixture-stale"), time.Now().Add(time.Hour)), auth.ErrSubscriptionRefreshConflict)

	login("rk_fixture_rotated")
	accounts = apiAccounts(t, router)
	require.Len(t, accounts, 2, "distinct Claude organizations must not merge")
	require.ElementsMatch(t, []string{accountA + ":" + organizationA, accountA + ":" + organizationB}, []string{accounts[0].ExternalAccountID, accounts[1].ExternalAccountID})

	login("rk_fixture_rotated")
	accounts = apiAccounts(t, router)
	require.Len(t, accounts, 3, "distinct Claude accounts must not merge")
	require.Contains(t, []string{accounts[0].ExternalAccountID, accounts[1].ExternalAccountID, accounts[2].ExternalAccountID}, accountB+":"+organizationB)

	for index, token := range []string{"refresh-fixture-codex-first", "refresh-fixture-codex-second"} {
		apiRequest(t, router, http.MethodPost, "", map[string]any{
			"provider": auth.SubscriptionProviderCodex, "external_account_id": "chatgpt-fixture", "display_name": fmt.Sprintf("ChatGPT Org: account-%d@example.test", index+1), "refresh_token": token,
		}, http.StatusCreated)
	}
	accounts = apiAccounts(t, router)
	require.Len(t, accounts, 4, "Codex reconnects still deduplicate independently")
	for _, account := range accounts {
		if account.Provider == auth.SubscriptionProviderCodex {
			require.Equal(t, "ChatGPT Org: account-2@example.test", account.DisplayName)
			latest, err = svc.SubscriptionRefreshToken(ctx, owner, account.ID)
			require.NoError(t, err)
			require.True(t, bytes.Equal(latest, []byte("refresh-fixture-codex-second")), "Codex retains the latest credential")
		}
	}
}

type accountResponse struct {
	ID                string                    `json:"id"`
	Provider          auth.SubscriptionProvider `json:"provider"`
	ExternalAccountID string                    `json:"external_account_id"`
	DisplayName       string                    `json:"display_name"`
	Enabled           bool                      `json:"enabled"`
	CooldownUntil     *time.Time                `json:"cooldown_until"`
}

func apiAccounts(t *testing.T, handler http.Handler) []accountResponse {
	t.Helper()
	response := apiRequest(t, handler, http.MethodGet, "", nil, http.StatusOK)
	var accounts []accountResponse
	require.NoError(t, json.Unmarshal(response, &accounts))
	return accounts
}

func apiRequest(t *testing.T, handler http.Handler, method, accountID string, body any, status int) []byte {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	path := "/v1/subscriptions/accounts"
	if accountID != "" {
		path += "/" + accountID
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.RouterKeyHeader, "rk_fixture_rotated")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, status, response.Code)
	for _, forbidden := range []string{"refresh_token", "access_token", "ciphertext", "refresh-fixture-", "access-fixture-"} {
		require.False(t, bytes.Contains(response.Body.Bytes(), []byte(forbidden)), "API response must not expose credentials")
	}
	return response.Body.Bytes()
}
