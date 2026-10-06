package subscription_verification_check_test

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
)

// The candidate admission under test executes generated SQL against migrated
// Postgres. Fixtures roll back and are confined to an explicitly loopback DSN.
func TestVerificationSQLSharingOwnershipAndIsolation(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("local Postgres integration requires ROUTER_TEST_DATABASE_URL")
	}
	require.NotEmpty(t, dsn, "this verification must execute SQL, never silently skip")
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
	org, otherOrg, personal, shared, outsider, key := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, installation := range []uuid.UUID{org, otherOrg} {
		_, err = tx.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name) VALUES($1,$2,'verification')`, installation, installation.String())
		require.NoError(t, err)
	}
	for _, subject := range []uuid.UUID{personal, shared, outsider} {
		_, err = tx.Exec(ctx, `INSERT INTO router.credential_subjects(id,projection_complete) VALUES($1,true)`, subject)
		require.NoError(t, err)
		installation := org
		if subject == outsider {
			installation = otherOrg
		}
		_, err = tx.Exec(ctx, `INSERT INTO router.credential_subject_installations(subject_id,installation_id,access_enabled) VALUES($1,$2,true)`, subject, installation)
		require.NoError(t, err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO router.model_router_api_keys(id,installation_id,external_id,key_prefix,key_hash,key_suffix) VALUES($1,$2,$3,'verify',$3,'test')`, key, org, key.String())
	require.NoError(t, err)
	personalAccount, sharedAccount, unownedAccount, foreignAccount := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for i, account := range []uuid.UUID{personalAccount, sharedAccount, unownedAccount, foreignAccount} {
		var subject any
		switch i {
		case 0:
			subject = personal
		case 1:
			subject = shared
		case 3:
			subject = outsider
		}
		_, err = tx.Exec(ctx, `INSERT INTO router.model_router_subscription_accounts(id,subscriber_id,api_key_id,provider,external_account_id,provider_user_id,refresh_token_ciphertext) VALUES($1,$2,$3,'codex',$4::varchar,$4::text,'synthetic-ciphertext')`, account, subject, key, account.String())
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO router.model_router_subscription_account_installations(installation_id,subscription_account_id) VALUES($1,$2)`, org, account)
		require.NoError(t, err)
	}
	repo := postgres.NewSubscriptionAccountRepo(tx)
	candidates := repo.(interface {
		ListSubscriptionCandidates(context.Context, auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error)
	})
	owner := auth.SubscriptionOwner{InstallationID: org.String(), SubscriberID: personal.String(), APIKeyID: key.String()}
	// OAuth re-enrollment cannot silently adopt an unassigned legacy account.
	adopted, _, adoptionErr := repo.UpsertSubscriptionAccount(ctx, auth.CreateSubscriptionAccountParams{Owner: owner, Provider: auth.SubscriptionProviderCodex, ExternalAccountID: unownedAccount.String(), ProviderUserID: unownedAccount.String(), RefreshToken: []byte("synthetic-replacement-must-not-persist")})
	require.Error(t, adoptionErr)
	require.Nil(t, adopted)
	var remainsUnassigned bool
	var preservedCiphertext []byte
	require.NoError(t, tx.QueryRow(ctx, `SELECT subscriber_id IS NULL,refresh_token_ciphertext FROM router.model_router_subscription_accounts WHERE id=$1`, unownedAccount).Scan(&remainsUnassigned, &preservedCiphertext))
	require.True(t, remainsUnassigned)
	require.Equal(t, []byte("synthetic-ciphertext"), preservedCiphertext)

	assertCandidates := func(expected []string) {
		t.Helper()
		accounts, err := candidates.ListSubscriptionCandidates(ctx, owner)
		require.NoError(t, err)
		ids := make([]string, len(accounts))
		for i, account := range accounts {
			ids[i] = account.ID
		}
		require.Equal(t, expected, ids)
	}
	assertCandidates([]string{personalAccount.String(), sharedAccount.String()})
	accounts, err := repo.ListSubscriptionAccounts(ctx, owner)
	require.NoError(t, err)
	for _, account := range accounts {
		require.NotEqual(t, sharedAccount.String(), account.ID, "borrowing grants no management rights")
	}
	// A requester whose own membership lapsed cannot borrow shared capacity.
	for _, revocation := range []string{
		`UPDATE router.credential_subject_installations SET access_enabled=false WHERE subject_id=$1`,
		`UPDATE router.credential_subjects SET revoked_at=now() WHERE id=$1`,
	} {
		_, err = tx.Exec(ctx, `SAVEPOINT requester_revocation`)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, revocation, personal)
		require.NoError(t, err)
		assertCandidates([]string{})
		_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT requester_revocation`)
		require.NoError(t, err)
	}
	_, err = tx.Exec(ctx, `UPDATE router.model_router_installations SET subscription_sharing_enabled=false WHERE id=$1`, org)
	require.NoError(t, err)
	assertCandidates([]string{personalAccount.String()})
	_, err = tx.Exec(ctx, `UPDATE router.model_router_installations SET subscription_sharing_enabled=true WHERE id=$1`, org)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE router.credential_subject_installations SET access_enabled=false WHERE subject_id=$1`, shared)
	require.NoError(t, err)
	assertCandidates([]string{personalAccount.String()})
	_, err = tx.Exec(ctx, `UPDATE router.credential_subject_installations SET access_enabled=true WHERE subject_id=$1`, shared)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE router.credential_subjects SET revoked_at=now() WHERE id=$1`, shared)
	require.NoError(t, err)
	assertCandidates([]string{personalAccount.String()})
	// A subject-less key cannot borrow any member's capacity.
	owner.SubscriberID = ""
	assertCandidates([]string{})
	// Restore the verified requester so the routing-disabled SQL gate is exercised.
	owner.SubscriberID = personal.String()
	_, err = tx.Exec(ctx, `UPDATE router.model_router_installations SET subscription_routing_disabled=true WHERE id=$1`, org)
	require.NoError(t, err)
	assertCandidates([]string{})

	duplicateA, duplicateB, duplicateKey := uuid.New(), uuid.New(), uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO router.model_router_api_keys(id,installation_id,external_id,key_prefix,key_hash,key_suffix) VALUES($1,$2,$3,'verify',$3,'test')`, duplicateKey, org, duplicateKey.String())
	require.NoError(t, err)
	for _, account := range []struct {
		id         uuid.UUID
		subscriber uuid.UUID
		apiKey     uuid.UUID
	}{{duplicateA, personal, key}, {duplicateB, shared, duplicateKey}} {
		_, err = tx.Exec(ctx, `INSERT INTO router.model_router_subscription_accounts(id,subscriber_id,api_key_id,provider,external_account_id,refresh_token_ciphertext,enabled) VALUES($1,$2,$3,'codex','duplicate-physical-account','synthetic-ciphertext',false)`, account.id, account.subscriber, account.apiKey)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `INSERT INTO router.model_router_subscription_account_installations(installation_id,subscription_account_id) VALUES($1,$2)`, org, account.id)
		require.NoError(t, err)
	}
	personalOwner := auth.SubscriptionOwner{InstallationID: org.String(), SubscriberID: personal.String(), APIKeyID: key.String()}
	sharedOwner := auth.SubscriptionOwner{InstallationID: org.String(), SubscriberID: shared.String(), APIKeyID: duplicateKey.String()}
	require.ErrorIs(t, repo.UpdateSubscriptionAccountState(ctx, duplicateA.String(), personalOwner, true, nil), auth.ErrSubscriptionAccountNotFound)
	require.NoError(t, repo.DeleteSubscriptionAccount(ctx, duplicateB.String(), sharedOwner))
	require.ErrorIs(t, repo.UpdateSubscriptionAccountState(ctx, duplicateA.String(), personalOwner, true, nil), auth.ErrSubscriptionAccountNotFound, "removing a workspace collision does not verify the legacy provider user")
}
