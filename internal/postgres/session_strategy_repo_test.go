package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sessionStrategyExec struct {
	query string
	args  []any
}

type sessionStrategyDB struct {
	row       pgx.Row
	execTag   pgconn.CommandTag
	execErr   error
	execCalls []sessionStrategyExec
	rowQuery  string
	rowArgs   []any
}

func (db *sessionStrategyDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	db.execCalls = append(db.execCalls, sessionStrategyExec{query: query, args: args})
	return db.execTag, db.execErr
}

func (*sessionStrategyDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}

func (db *sessionStrategyDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	db.rowQuery = query
	db.rowArgs = args
	return db.row
}

type sessionStrategyRow struct {
	strategy string
	err      error
}

type sessionStrategyEnabledRow struct {
	enabled bool
	err     error
}

func (row sessionStrategyEnabledRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	*dest[0].(*bool) = row.enabled
	return nil
}

func (row sessionStrategyRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	*dest[0].(*string) = row.strategy
	return nil
}

func TestSessionStrategyRepoGet(t *testing.T) {
	t.Parallel()

	installationID := uuid.New()
	key := [sessionstrategy.SessionKeyLen]byte{1, 2, 3}
	db := &sessionStrategyDB{row: sessionStrategyRow{strategy: string(router.StrategyHMMBeta)}}
	repo := NewSessionStrategyRepo(db)

	preference, ok, err := repo.Get(context.Background(), installationID, key)

	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, sessionstrategy.Preference{
		InstallationID: installationID,
		SessionKey:     key,
		Strategy:       router.StrategyHMMBeta,
	}, preference)
	assert.Contains(t, db.rowQuery, "installation_id = $1::uuid")
	assert.Contains(t, db.rowQuery, "session_key = $2::bytea")
	require.Len(t, db.rowArgs, 2)
	assert.Equal(t, installationID, db.rowArgs[0])
	assert.Equal(t, key[:], db.rowArgs[1])
}

func TestSessionStrategyRepoGetMissingMeansStable(t *testing.T) {
	t.Parallel()

	db := &sessionStrategyDB{row: sessionStrategyRow{err: sql.ErrNoRows}}
	repo := NewSessionStrategyRepo(db)

	preference, ok, err := repo.Get(context.Background(), uuid.New(), [sessionstrategy.SessionKeyLen]byte{})

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, preference)
}

func TestSessionStrategyRepoToggleAcceptsOnlyHMMBeta(t *testing.T) {
	t.Parallel()

	installationID := uuid.New()
	key := [sessionstrategy.SessionKeyLen]byte{4, 5, 6}
	db := &sessionStrategyDB{row: sessionStrategyEnabledRow{enabled: true}}
	repo := NewSessionStrategyRepo(db)

	_, err := repo.Toggle(context.Background(), sessionstrategy.Preference{
		InstallationID: installationID,
		SessionKey:     key,
		Strategy:       "stable",
	})
	require.ErrorIs(t, err, sessionstrategy.ErrInvalidStrategy)
	assert.Empty(t, db.rowQuery)

	enabled, err := repo.Toggle(context.Background(), sessionstrategy.Preference{
		InstallationID: installationID,
		SessionKey:     key,
		Strategy:       router.StrategyHMMBeta,
	})
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.True(t, strings.Contains(db.rowQuery, "ON CONFLICT (installation_id, session_key)"))
	assert.Contains(t, db.rowQuery, "enabled = NOT router.session_strategy_preferences.enabled")
	assert.Contains(t, db.rowQuery, "RETURNING enabled")
	assert.Equal(t, []any{installationID, key[:], string(router.StrategyHMMBeta)}, db.rowArgs)
}

func TestSessionStrategyRepoDisableReportsWhetherBetaWasOn(t *testing.T) {
	t.Parallel()

	installationID := uuid.New()
	key := [sessionstrategy.SessionKeyLen]byte{4, 5, 6}
	db := &sessionStrategyDB{execTag: pgconn.NewCommandTag("UPDATE 1")}
	repo := NewSessionStrategyRepo(db)

	wasEnabled, err := repo.Disable(context.Background(), installationID, key)
	require.NoError(t, err)
	assert.True(t, wasEnabled)
	require.Len(t, db.execCalls, 1)
	assert.Contains(t, db.execCalls[0].query, "SET enabled = FALSE")
	assert.Contains(t, db.execCalls[0].query, "AND enabled")
	assert.Equal(t, []any{installationID, key[:]}, db.execCalls[0].args)

	db.execTag = pgconn.NewCommandTag("UPDATE 0")
	wasEnabled, err = repo.Disable(context.Background(), installationID, key)
	require.NoError(t, err)
	assert.False(t, wasEnabled, "a session already on stable routing was not enabled")
}

func TestSessionStrategyRepoGetIgnoresDisabledRows(t *testing.T) {
	t.Parallel()

	db := &sessionStrategyDB{row: sessionStrategyRow{err: sql.ErrNoRows}}
	repo := NewSessionStrategyRepo(db)

	_, ok, err := repo.Get(context.Background(), uuid.New(), [sessionstrategy.SessionKeyLen]byte{7, 8, 9})

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Contains(t, db.rowQuery, "AND enabled")
}

func TestSessionPinConversionPreservesRoutingStrategy(t *testing.T) {
	t.Parallel()

	pin := toSessionPin(sqlc.RouterSessionPin{RoutingStrategy: "hmm_beta"})
	assert.Equal(t, router.StrategyHMMBeta, pin.Strategy)
}

func TestSessionPinMutationsCarryExpectedRoutingStrategy(t *testing.T) {
	t.Parallel()

	key := [sessionpin.SessionKeyLen]byte{10, 11, 12}
	db := &sessionStrategyDB{row: sessionStrategyRow{err: sql.ErrNoRows}}
	repo := NewSessionPinRepo(db)
	ctx := context.Background()

	_, ok, err := repo.Consume(ctx, key, "default", router.StrategyCluster)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Contains(t, db.rowQuery, "routing_strategy = ''")
	assert.Contains(t, db.rowQuery, "<> 'hmm_beta'")
	assert.Equal(t, []any{key[:], "default", string(router.StrategyCluster)}, db.rowArgs)

	_, ok, err = repo.Consume(ctx, key, "default", router.StrategyHMMBeta)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, []any{key[:], "default", string(router.StrategyHMMBeta)}, db.rowArgs)

	_, err = repo.IncrementUpstreamErrors(ctx, key, "default", router.StrategyHMMBeta)
	require.NoError(t, err)
	assert.Equal(t, []any{key[:], "default", string(router.StrategyHMMBeta)}, db.rowArgs)

	_, err = repo.IncrementOverloadErrors(ctx, key, "default", router.StrategyHMMBeta)
	require.NoError(t, err)
	assert.Equal(t, []any{key[:], "default", string(router.StrategyHMMBeta)}, db.rowArgs)

	require.NoError(t, repo.UpdateUsage(ctx, key, "default", sessionpin.Usage{
		Strategy: router.StrategyHMMBeta,
		EndedAt:  time.Unix(1, 0),
	}))
	require.NoError(t, repo.ResetUpstreamErrors(ctx, key, "default", router.StrategyHMMBeta))
	require.NoError(t, repo.ResetOverloadErrors(ctx, key, "default", router.StrategyHMMBeta))
	require.NoError(t, repo.DisableProvider(ctx, key, "default", "anthropic", router.StrategyHMMBeta))

	require.Len(t, db.execCalls, 4)
	for _, call := range db.execCalls {
		assert.Contains(t, call.query, "routing_strategy = ''")
		assert.Contains(t, call.query, "<> 'hmm_beta'")
		assert.Equal(t, string(router.StrategyHMMBeta), call.args[len(call.args)-1])
	}
}
