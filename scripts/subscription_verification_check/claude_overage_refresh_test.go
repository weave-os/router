package subscription_verification_check_test

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/requestcontext"
)

func TestVerificationSQLClaudeOverageRefreshGate(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("local Postgres integration requires ROUTER_TEST_DATABASE_URL")
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
	installationID, keyID := uuid.New(), uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name) VALUES($1,$2,'overage-verification')`, installationID, installationID.String())
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO router.model_router_api_keys(id,installation_id,external_id,key_prefix,key_hash,key_suffix) VALUES($1,$2,$3,'verify',$3,'test')`, keyID, installationID, keyID.String())
	require.NoError(t, err)
	repo := postgres.NewSubscriptionAccountRepo(tx)
	owner := auth.SubscriptionOwner{APIKeyID: keyID.String()}
	for _, test := range []struct {
		name       string
		provider   auth.SubscriptionProvider
		state      auth.SubscriptionAccountState
		extraUsage bool
		allowed    bool
	}{
		{name: "included quota only", provider: auth.SubscriptionProviderClaude, state: auth.SubscriptionAccountStateExhausted},
		{name: "Enterprise overage", provider: auth.SubscriptionProviderClaude, state: auth.SubscriptionAccountStateExhausted, extraUsage: true, allowed: true},
		{name: "provider cooldown", provider: auth.SubscriptionProviderClaude, state: auth.SubscriptionAccountStateCooldown, extraUsage: true},
		{name: "disabled", provider: auth.SubscriptionProviderClaude, state: auth.SubscriptionAccountStateDisabled, extraUsage: true},
		{name: "Codex exhaustion", provider: auth.SubscriptionProviderCodex, state: auth.SubscriptionAccountStateExhausted, extraUsage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			accountID := uuid.New()
			_, err := tx.Exec(ctx, `INSERT INTO router.model_router_subscription_accounts(id,api_key_id,provider,external_account_id,refresh_token_ciphertext,health_state,cooldown_until) VALUES($1,$2,$3,$4,'synthetic-ciphertext',$5,CURRENT_TIMESTAMP+INTERVAL '1 hour')`, accountID, keyID, string(test.provider), accountID.String(), string(test.state))
			require.NoError(t, err)
			leaseCtx := ctx
			if test.extraUsage {
				leaseCtx = requestcontext.WithSubscriptionExtraUsage(ctx)
			}
			leaseID := uuid.NewString()
			acquisition, err := repo.TryAcquireSubscriptionRefreshLease(leaseCtx, accountID.String(), owner, leaseID, time.Minute)
			require.NoError(t, err)
			require.Equal(t, test.allowed, acquisition.Acquired)
			if acquisition.Acquired {
				duplicate, err := repo.TryAcquireSubscriptionRefreshLease(leaseCtx, accountID.String(), owner, uuid.NewString(), time.Minute)
				require.NoError(t, err)
				require.False(t, duplicate.Acquired, "overage permission must not bypass an active refresh lease")
			}
		})
	}
}
