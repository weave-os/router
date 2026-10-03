package main

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"net/url"
	"os"
	"testing"
	"time"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres/serving"
)

func TestInternalTestBookAndGrantIsolation(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_DATABASE_URL is required for the isolated Postgres test")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, parsed.Hostname(), "only disposable loopback databases are allowed")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	installation, subject, key, otherKey := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		for _, cleanup := range []string{
			"DELETE FROM router.internal_test_credit_ledger WHERE subject_id=$1",
			"DELETE FROM router.internal_test_plan_launches WHERE subject_id=$1",
			"DELETE FROM router.internal_test_budgets WHERE subject_id=$1",
			"DELETE FROM router.model_router_api_keys WHERE credential_subject_id=$1",
			"DELETE FROM router.credential_subject_installations WHERE subject_id=$1",
			"DELETE FROM router.credential_subjects WHERE id=$1",
		} {
			_, err := pool.Exec(context.Background(), cleanup, subject)
			require.NoError(t, err)
		}
		_, err := pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id=$1", installation)
		require.NoError(t, err)
	})
	_, err = pool.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name) VALUES($1,'synthetic-test-org','Synthetic test')`, installation)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO router.credential_subjects(id,projection_complete,internal_enrolled,enrollment_generation) VALUES($1,true,true,3)`, subject)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO router.credential_subject_installations(subject_id,installation_id,access_enabled) VALUES($1,$2,true)`, subject, installation)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO router.model_router_api_keys(id,installation_id,external_id,key_prefix,key_hash,key_suffix,credential_subject_id)
 VALUES($1,$2,'synthetic-test-key','rk_synthetic',$3,'test',$4),($5,$2,'synthetic-shared-key','rk_shared',$6,'test',NULL)`, key, installation, uuid.NewString(), subject, otherKey, uuid.NewString())
	require.NoError(t, err)
	repo := serving.NewTestPlanRepo(pool)
	_, err = repo.GetTestIdentity(ctx, subject.String())
	require.Error(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO router.internal_test_budgets(subject_id,installation_id,label,balance_usd_micros,enabled) VALUES($1,$2,'Synthetic internal budget',1000000,true)`, subject, installation)
	require.NoError(t, err)
	identity, err := repo.GetTestIdentity(ctx, subject.String())
	require.NoError(t, err)
	require.Equal(t, int64(1000000), identity.BalanceMicros)
	now := time.Now().UTC()
	launch := policyregistry.TestPlanLaunch{ID: uuid.NewString(), Preview: policyregistry.TestPlanPreview{Identity: identity, Plan: policyregistry.TestPlanStable}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	hash := policyregistry.Digest([]byte("synthetic-grant"))
	require.NoError(t, repo.SaveTestLaunch(ctx, launch, hash))
	session := uuid.NewString()
	_, err = repo.AuthorizeTestLaunch(ctx, hash, installation.String(), otherKey.String(), session)
	require.Error(t, err)
	admitted, err := repo.AuthorizeTestLaunch(ctx, hash, installation.String(), key.String(), session)
	require.NoError(t, err)
	require.Equal(t, launch.ID, admitted.ID)
	_, err = repo.AuthorizeTestLaunch(ctx, hash, installation.String(), key.String(), uuid.NewString())
	require.Error(t, err)
	require.NoError(t, repo.RevokeTestLaunch(ctx, launch.ID))
	require.NoError(t, repo.RevokeTestLaunch(ctx, launch.ID), "revoke remains idempotent for an existing launch")
	require.ErrorIs(t, repo.RevokeTestLaunch(ctx, uuid.NewString()), policyregistry.ErrTestLaunchNotFound)
	_, err = repo.AuthorizeTestLaunch(ctx, hash, installation.String(), key.String(), session)
	require.Error(t, err)
	launch.ID = uuid.NewString()
	launch.CreatedAt = now.Add(-2 * time.Hour)
	launch.ExpiresAt = now.Add(-time.Hour)
	require.NoError(t, repo.SaveTestLaunch(ctx, launch, policyregistry.Digest([]byte("synthetic-expired-grant"))))
	_, err = repo.AuthorizeTestLaunch(ctx, policyregistry.Digest([]byte("synthetic-expired-grant")), installation.String(), key.String(), session)
	require.Error(t, err)
	book := serving.NewInternalTestBook(pool)
	_, err = book.Balance(ctx, billing.OrganizationOwner("synthetic-test-org"))
	require.ErrorIs(t, err, billing.ErrOwnerKindUnsupported)
	debit := billing.PrepaidDebit{Owner: billing.InternalTestOwner(subject.String()), DeltaUsdMicros: -250000, RouterRequestID: "synthetic-request", RouterModel: "synthetic-model", APIKeyID: key.String()}
	balance, err := book.Debit(ctx, debit)
	require.NoError(t, err)
	require.Equal(t, int64(750000), balance)
	debit.DeltaUsdMicros = -800000
	_, err = book.Debit(ctx, debit)
	require.Error(t, err, "an inference charge larger than the remaining budget must not overdraw it")
	balance, err = book.Balance(ctx, billing.InternalTestOwner(subject.String()))
	require.NoError(t, err)
	require.Equal(t, int64(750000), balance)
	debit.DeltaUsdMicros = -250000
	var spent, ledgerCount int64
	err = pool.QueryRow(ctx, `SELECT spent_usd_micros FROM router.model_router_api_keys WHERE id=$1`, key).Scan(&spent)
	require.NoError(t, err)
	require.Equal(t, int64(250000), spent)
	err = pool.QueryRow(ctx, `SELECT count(*) FROM router.internal_test_credit_ledger WHERE subject_id=$1`, subject).Scan(&ledgerCount)
	require.NoError(t, err)
	require.Equal(t, int64(1), ledgerCount)
	debit.APIKeyID = otherKey.String()
	_, err = book.Debit(ctx, debit)
	require.Error(t, err)
	_, err = pool.Exec(ctx, `UPDATE router.internal_test_budgets SET enabled=false WHERE subject_id=$1`, subject)
	require.NoError(t, err)
	_, err = repo.GetTestIdentity(ctx, subject.String())
	require.Error(t, err)
	_, err = book.Balance(ctx, billing.InternalTestOwner(subject.String()))
	require.Error(t, err)
}
