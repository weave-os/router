package subscription_enrollment_check_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/subscriptions"
)

func TestCodexSharedWorkspaceSeatsAndLegacyReconnect(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable local Postgres")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname())
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	installation := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name) VALUES($1,$2,'synthetic-identity')`, installation, installation.String())
	require.NoError(t, err)
	owners := make([]auth.SubscriptionOwner, 3)
	for i := range owners {
		subject, key := uuid.New(), uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO router.credential_subjects(id,projection_complete) VALUES($1,true)`, subject)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO router.credential_subject_installations(subject_id,installation_id,access_enabled) VALUES($1,$2,true)`, subject, installation)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO router.model_router_api_keys(id,installation_id,external_id,key_prefix,key_hash,key_suffix,credential_subject_id) VALUES($1,$2,$3,'rk_',$4,'test',$5)`, key, installation, key.String(), uuid.NewString(), subject)
		require.NoError(t, err)
		owners[i] = auth.SubscriptionOwner{SubscriberID: subject.String(), APIKeyID: key.String(), InstallationID: installation.String()}
	}
	workspace := uuid.NewString()
	legacy := make([]uuid.UUID, 2)
	for i := range legacy {
		legacy[i] = uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO router.model_router_subscription_accounts(id,subscriber_id,api_key_id,provider,external_account_id,refresh_token_ciphertext,enabled,health_state) VALUES($1,$2,$3,'codex',$4,'legacy',false,'reconnect_required')`, legacy[i], owners[i].SubscriberID, owners[i].APIKeyID, workspace)
		require.NoError(t, err)
	}
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		user := r.Form.Get("refresh_token")
		payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_user_id":%q}}`, workspace, user)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"eyJhbGciOiJSUzI1NiJ9.`+payload+`.sig","refresh_token":"rotated-`+user+`"}`)
	}))
	defer oauth.Close()
	repo := postgres.NewSubscriptionAccountRepo(tx)
	svc := auth.NewService(nil, nil, nil, nil, auth.NoOpAPIKeyCache{}, nil, time.Now).WithSubscriptionAccounts(repo).WithCodexEnrollmentVerifier(subscriptions.NewOAuthClient(oauth.Client(), oauth.URL, "", nil))
	enroll := func(owner auth.SubscriptionOwner, user string) (*auth.SubscriptionAccount, error) {
		return svc.AddSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: owner, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: workspace, RefreshToken: []byte(user)})
	}
	require.Error(t, svc.UpdateSubscriptionAccountState(ctx, owners[0], legacy[0].String(), true, nil))
	accountA, err := enroll(owners[0], "provider-user-a")
	require.NoError(t, err)
	require.Equal(t, legacy[0].String(), accountA.ID)
	untouched, err := repo.ListSubscriptionAccounts(ctx, owners[1])
	require.NoError(t, err)
	require.Len(t, untouched, 1)
	require.False(t, untouched[0].Enabled)
	accountB, err := enroll(owners[1], "provider-user-b")
	require.NoError(t, err)
	require.Equal(t, legacy[1].String(), accountB.ID)
	require.True(t, accountA.Enabled)
	require.True(t, accountB.Enabled)
	require.Equal(t, workspace, accountA.ExternalAccountID)
	require.NotEqual(t, accountA.ProviderUserID, accountB.ProviderUserID)
	_, err = enroll(owners[2], "provider-user-a")
	require.Error(t, err, "same provider seat cannot move across owners")
	reconnectedAccountA, err := enroll(owners[0], "provider-user-a")
	require.NoError(t, err)
	require.Equal(t, accountA.ID, reconnectedAccountA.ID)
	credentials, err := svc.LoadSubscriptionCredentials(ctx, owners[0], accountA.ID)
	require.NoError(t, err)
	require.Equal(t, "provider-user-a", credentials.ProviderUserID)
	require.True(t, strings.HasPrefix(string(credentials.RefreshToken), "rotated-"))
}
