package subscription_verification_check_test

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/subscriptions"
)

func TestVerificationSQLUnsignedEmailCannotClaimPersonalServing(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("local Postgres integration requires ROUTER_TEST_DATABASE_URL")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname())
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	org, subject, orgKey, personalKey := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	legacyToken, personalToken := "rk_synthetic_legacy_"+uuid.NewString(), "rk_synthetic_personal_"+uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name,subscription_sharing_enabled) VALUES($1,$2,'verification',false)`, org, org.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_subscription_accounts WHERE api_key_id=$1`, personalKey)
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_api_keys WHERE installation_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM router.credential_subject_identities WHERE installation_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM router.credential_subject_installations WHERE installation_id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM router.credential_subjects WHERE id=$1`, subject)
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_installations WHERE id=$1`, org)
	})
	_, err = pool.Exec(ctx, `INSERT INTO router.credential_subjects(id,projection_complete) VALUES($1,true)`, subject)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO router.credential_subject_installations(subject_id,installation_id,access_enabled) VALUES($1,$2,true)`, subject, org)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO router.credential_subject_identities(installation_id,email,subject_id) VALUES($1,'synthetic-member@example.invalid',$2)`, org, subject)
	require.NoError(t, err)
	for _, key := range []struct {
		id    uuid.UUID
		token string
		owner any
	}{{orgKey, legacyToken, nil}, {personalKey, personalToken, subject}} {
		_, err = pool.Exec(ctx, `INSERT INTO router.model_router_api_keys(id,installation_id,external_id,key_prefix,key_hash,key_suffix,credential_subject_id) VALUES($1,$2,$3,'rk_',$4,'test',$5)`, key.id, org, key.id.String(), auth.HashAPIKeySHA256(key.token), key.owner)
		require.NoError(t, err)
	}
	repositories := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	authService := auth.NewService(repositories.Installations, repositories.APIKeys, repositories.ExternalAPIKeys, repositories.Users, auth.NoOpAPIKeyCache{}, nil, time.Now).WithEncryptor(auth.NoOpEncryptor{}).WithCredentialSubjectLookup(postgres.NewCredentialSubjectRepo(pool)).WithSubscriptionAccounts(repositories.SubscriptionAccounts).WithRoutingPolicies(repositories.RoutingPolicies, nil).WithCodexEnrollmentVerifier(verificationCodexEnrollment{})
	account, err := authService.AddSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: auth.SubscriptionOwner{InstallationID: org.String(), SubscriberID: subject.String(), APIKeyID: personalKey.String()}, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: "synthetic-email-owner-" + uuid.NewString(), RefreshToken: []byte("synthetic-refresh")})
	require.NoError(t, err)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": verificationCodexIDToken(t, account.ExternalAccountID), "access_token": "synthetic-personal-access", "refresh_token": "synthetic-rotated-refresh", "expires_in": 3600})
	}))
	defer tokenServer.Close()
	runtime := subscriptions.NewRuntime(authService, subscriptions.NewOAuthClient(tokenServer.Client(), tokenServer.URL, tokenServer.URL, time.Now), time.Now)
	var bearers []string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearers = append(bearers, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"identity answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer providerServer.Close()
	client := &verificationIncludedClient{openai.NewClient("synthetic-api-key", providerServer.URL)}
	client.SetCodexBaseURL(providerServer.URL)
	svc := proxy.NewService(verificationBillingRouter{}, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil).WithManagedSubscriptions(runtime).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
	var winners []*proxy.ManagedSubscriptionUsage
	var resolvedOwners []auth.SubscriptionOwner
	var proxyErrors []error
	engine := gin.New()
	engine.POST("/v1/chat/completions", middleware.WithAuth(authService, false), func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		resolvedOwners = append(resolvedOwners, middleware.SubscriptionOwnerFrom(c))
		proxyErrors = append(proxyErrors, svc.ProxyOpenAIChatCompletion(c.Request.Context(), body, c.Writer, c.Request))
		winners = append(winners, c.Request.Context().Value(proxy.ManagedSubscriptionUsageContextKey{}).(*proxy.ManagedSubscriptionUsage))
	})
	actualIngress := httptest.NewServer(engine)
	defer actualIngress.Close()
	for _, scenario := range []struct {
		name, token, email string
		wantPersonal       bool
	}{{"legacy-projected-email", legacyToken, "synthetic-member@example.invalid", false}, {"verified-personal-bogus-email", personalToken, "unprojected@example.invalid", true}, {"legacy-sharing-on", legacyToken, "synthetic-member@example.invalid", false}} {
		t.Run(scenario.name, func(t *testing.T) {
			if scenario.name == "legacy-sharing-on" {
				_, err = pool.Exec(ctx, `UPDATE router.model_router_installations SET subscription_sharing_enabled=true WHERE id=$1`, org)
				require.NoError(t, err)
			}
			before := len(bearers)
			proxyErrorsBefore := len(proxyErrors)
			body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"synthetic identity"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
			request, err := http.NewRequest("POST", actualIngress.URL+"/v1/chat/completions", strings.NewReader(body))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer "+scenario.token)
			request.Header.Set("X-Weave-User-Email", scenario.email)
			request.Header.Set("Content-Type", "application/json")
			response, err := actualIngress.Client().Do(request)
			require.NoError(t, err)
			payload, _ := io.ReadAll(response.Body)
			response.Body.Close()
			require.Len(t, proxyErrors, proxyErrorsBefore+1, "the request must reach the proxy handler")
			require.NoError(t, proxyErrors[proxyErrorsBefore])
			require.Equal(t, 200, response.StatusCode, string(payload))
			require.Contains(t, string(payload), "identity answer")
			require.Len(t, bearers, before+1)
			winner := winners[len(winners)-1]
			identity := resolvedOwners[len(resolvedOwners)-1]
			if scenario.wantPersonal {
				require.Equal(t, subject.String(), identity.SubscriberID)
				require.Equal(t, "Bearer synthetic-personal-access", bearers[before])
				require.True(t, winner.Served)
				require.Equal(t, account.ID, winner.SubscriptionAccountID)
				require.Equal(t, auth.SubscriptionTierPersonal, winner.SubscriptionTier)
			} else {
				require.Empty(t, identity.SubscriberID, "unsigned email cannot authenticate the projected owner")
				// Subject-less keys fail closed to API capacity even with sharing on.
				require.Equal(t, "Bearer synthetic-api-key", bearers[before])
				require.False(t, winner.Served)
			}
		})
	}
}

// verificationCodexEnrollment isolates owner/admission tests from provider OAuth.
type verificationCodexEnrollment struct{}

func (verificationCodexEnrollment) VerifyCodexEnrollment(_ context.Context, workspaceID string, refreshToken []byte) (auth.VerifiedCodexEnrollment, error) {
	return auth.VerifiedCodexEnrollment{ProviderUserID: "synthetic-provider-user", RefreshToken: []byte(string(refreshToken) + ":" + workspaceID)}, nil
}
func verificationCodexIDToken(t *testing.T, workspaceID string) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": workspaceID, "chatgpt_user_id": "synthetic-provider-user"}}).SignedString([]byte("synthetic-signing-key"))
	require.NoError(t, err)
	return token
}
