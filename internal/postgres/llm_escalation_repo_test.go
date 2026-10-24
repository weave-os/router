package postgres_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"weave-os/router/internal/postgres"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertLLMEscalationSession(t *testing.T, pool *pgxpool.Pool, installationID uuid.UUID, expiresAt time.Time, continuations int) ([]byte, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	scope := make([]byte, 32)
	_, err := rand.Read(scope)
	require.NoError(t, err)
	lifetime := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO router.llm_escalation_sessions (scope, lifetime, installation_id, state, expires_at) VALUES ($1, $2, $3, '{}'::jsonb, $4)", scope, lifetime, installationID, expiresAt)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO router.llm_escalation_continuations (activation, response_digest, lifetime, history)
SELECT sha256($1 || i::text::bytea), sha256($1 || ('d' || i)::bytea), $2, '[{"role":"user"}]'::jsonb FROM generate_series(1, $3::int) AS i`, scope, lifetime, continuations)
	require.NoError(t, err)
	return scope, lifetime
}

func countLLMEscalationRows(t *testing.T, pool *pgxpool.Pool, lifetime uuid.UUID) (sessions, continuations int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM router.llm_escalation_sessions WHERE lifetime = $1", lifetime).Scan(&sessions))
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM router.llm_escalation_continuations WHERE lifetime = $1", lifetime).Scan(&continuations))
	return sessions, continuations
}

// A peer sweeper or request holding one expired session must not stall the
// sweep past its operation budget, and histories larger than one batch drain.
// Gated on ROUTER_TEST_DATABASE_URL (see flag_definitions_test.go).
func TestLLMEscalationRepo_SweepExpiredDrainsBatchesAroundLockedRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	installationID := uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO router.model_router_installations (id, external_id, name) VALUES ($1, $2, $2)", installationID, installationID.String()[:30])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.model_router_installations WHERE id = $1", installationID)
	})

	expired := time.Now().Add(-time.Hour)
	_, large := insertLLMEscalationSession(t, pool, installationID, expired, 200)
	lockedScope, locked := insertLLMEscalationSession(t, pool, installationID, expired, 3)
	_, live := insertLLMEscalationSession(t, pool, installationID, time.Now().Add(time.Hour), 3)

	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	_, err = holder.Exec(ctx, "SELECT 1 FROM router.llm_escalation_sessions WHERE scope = $1 FOR UPDATE", lockedScope)
	require.NoError(t, err)

	require.NoError(t, postgres.NewLLMEscalationRepo(pool).SweepExpired(ctx))

	sessions, continuations := countLLMEscalationRows(t, pool, large)
	assert.Zero(t, sessions)
	assert.Zero(t, continuations)
	sessions, _ = countLLMEscalationRows(t, pool, locked)
	assert.Equal(t, 1, sessions, "a locked expired session is left for a later sweep")
	sessions, continuations = countLLMEscalationRows(t, pool, live)
	assert.Equal(t, 1, sessions)
	assert.Equal(t, 3, continuations)

	require.NoError(t, holder.Rollback(ctx))
	require.NoError(t, postgres.NewLLMEscalationRepo(pool).SweepExpired(ctx))
	sessions, continuations = countLLMEscalationRows(t, pool, locked)
	assert.Zero(t, sessions)
	assert.Zero(t, continuations)
}
