package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/sqlc"
)

// This exercises the schema constraint and actual cap query across owners and key rotation.
// Every fixture write rolls back in the caller-supplied local database.
func TestOwnerBoundIssuersPreserveSpendAcrossOwnersAndKeys(t *testing.T) {
	dsn := os.Getenv(databaseURLEnv)
	if dsn == "" {
		t.Skipf("%s not set", databaseURLEnv)
	}
	_, err := fixtureDSN(dsn)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer connection.Close(context.Background())
	tx, err := connection.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	repositories := postgres.NewRepository(tx, auth.NoOpEncryptor{})
	queries := sqlc.New(tx)
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: "fixture_" + uuid.NewString()[:16], Name: "Issuer spend fixture"})
	require.NoError(t, err)
	other, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: "fixture_" + uuid.NewString()[:16], Name: "Other issuer spend fixture"})
	require.NoError(t, err)

	createIssuer := func(installationID string, subjectID string) *auth.APIKey {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		jwk, err := (jose.JSONWebKey{Key: public}).MarshalJSON()
		require.NoError(t, err)
		parameters := newKey(installationID)
		parameters.Scope, parameters.PublicJWK, parameters.CredentialSubjectID = auth.ScopeSessionIssuer, jwk, subjectID
		issuer, err := repositories.APIKeys.Create(ctx, parameters)
		require.NoError(t, err)
		require.Equal(t, subjectID, issuer.CredentialSubjectID)
		return issuer
	}
	createSubject := func() string {
		subject, err := queries.InsertCredentialSubject(ctx)
		require.NoError(t, err)
		require.NoError(t, queries.InsertCredentialSubjectInstallation(ctx, sqlc.InsertCredentialSubjectInstallationParams{SubjectID: subject.ID, InstallationID: uuid.MustParse(installation.ID)}))
		return subject.ID.String()
	}
	ownerA := createSubject()
	first := createIssuer(installation.ID, ownerA)
	rotated := createIssuer(installation.ID, ownerA)
	reassigned := createIssuer(installation.ID, createSubject())
	legacy := createIssuer(installation.ID, "")
	foreign := createIssuer(other.ID, "")
	for _, row := range []struct {
		issuer *auth.APIKey
		spent  int64
	}{
		{first, 100}, {rotated, 200}, {reassigned, 300}, {legacy, 400}, {foreign, 900},
	} {
		_, err := tx.Exec(ctx, `INSERT INTO router.delegated_token_spend (issuer_key_id, scope_kind, scope_id, spent_usd_micros) VALUES ($1, 'session', 'session-1', $2), ($1, 'task', 'task-1', $2)`, row.issuer.ID, row.spent)
		require.NoError(t, err)
	}
	// Soft-deleting an old key must not remove the spend incurred before rotation.
	_, err = tx.Exec(ctx, `UPDATE router.model_router_api_keys SET deleted_at = NOW() WHERE id = $1`, first.ID)
	require.NoError(t, err)
	billing := postgres.NewBillingRepo(tx)
	for _, issuer := range []*auth.APIKey{rotated, reassigned} {
		session, task, err := billing.GetDelegatedTokenSpend(ctx, issuer.ID, "session-1", "task-1")
		require.NoError(t, err)
		require.EqualValues(t, 1000, session)
		require.EqualValues(t, 1000, task)
	}
	session, task, err := billing.GetDelegatedTokenSpend(ctx, legacy.ID, "session-1", "task-1")
	require.NoError(t, err)
	require.EqualValues(t, 400, session)
	require.EqualValues(t, 400, task)
}
