package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/router/compactioncheckpoint"
	"weave-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5/pgtype"
)

type CompactionCheckpointRepo struct {
	tx        sqlc.DBTX
	encryptor auth.Encryptor
}

func NewCompactionCheckpointRepo(tx sqlc.DBTX, encryptor auth.Encryptor) *CompactionCheckpointRepo {
	return &CompactionCheckpointRepo{tx: tx, encryptor: encryptor}
}

var _ compactioncheckpoint.Store = (*CompactionCheckpointRepo)(nil)

func checkpointPurpose(sessionKey [16]byte, endpoint string) string {
	return fmt.Sprintf("compaction-checkpoint:%x:%s", sessionKey, endpoint)
}

func (r *CompactionCheckpointRepo) Get(ctx context.Context, identity string, sessionKey [16]byte, endpoint string) (compactioncheckpoint.Checkpoint, bool, error) {
	row, err := sqlc.New(r.tx).GetCompactionCheckpoint(ctx, sqlc.GetCompactionCheckpointParams{
		CredentialIdentity: identity,
		SessionKey:         sessionKey[:],
		Endpoint:           endpoint,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return compactioncheckpoint.Checkpoint{}, false, nil
		}
		return compactioncheckpoint.Checkpoint{}, false, err
	}
	if len(row.PrefixDigest) != 32 || len(row.PolicyDigest) != 32 || row.Boundary <= 0 {
		return compactioncheckpoint.Checkpoint{}, false, fmt.Errorf("invalid compaction checkpoint metadata")
	}
	summary, err := r.encryptor.Decrypt(row.SummaryCiphertext, identity, checkpointPurpose(sessionKey, endpoint))
	if err != nil {
		return compactioncheckpoint.Checkpoint{}, false, fmt.Errorf("decrypt compaction checkpoint: %w", err)
	}
	checkpoint := compactioncheckpoint.Checkpoint{
		CredentialIdentity: identity,
		SessionKey:         sessionKey,
		Endpoint:           endpoint,
		Boundary:           int(row.Boundary),
		Summary:            string(summary),
		Model:              row.SummaryModel,
		ExpiresAt:          row.ExpiresAt.Time,
	}
	copy(checkpoint.PrefixDigest[:], row.PrefixDigest)
	copy(checkpoint.PolicyDigest[:], row.PolicyDigest)
	return checkpoint, true, nil
}

func (r *CompactionCheckpointRepo) Upsert(ctx context.Context, checkpoint compactioncheckpoint.Checkpoint) error {
	ciphertext, err := r.encryptor.Encrypt(
		[]byte(checkpoint.Summary), checkpoint.CredentialIdentity,
		checkpointPurpose(checkpoint.SessionKey, checkpoint.Endpoint),
	)
	if err != nil {
		return fmt.Errorf("encrypt compaction checkpoint: %w", err)
	}
	return sqlc.New(r.tx).UpsertCompactionCheckpoint(ctx, sqlc.UpsertCompactionCheckpointParams{
		CredentialIdentity: checkpoint.CredentialIdentity,
		SessionKey:         checkpoint.SessionKey[:],
		Endpoint:           checkpoint.Endpoint,
		PrefixDigest:       checkpoint.PrefixDigest[:],
		PolicyDigest:       checkpoint.PolicyDigest[:],
		Boundary:           int32(checkpoint.Boundary),
		SummaryCiphertext:  ciphertext,
		SummaryModel:       checkpoint.Model,
		ExpiresAt:          pgtype.Timestamptz{Time: checkpoint.ExpiresAt.UTC(), Valid: true},
	})
}

func (r *CompactionCheckpointRepo) SweepExpired(ctx context.Context) error {
	return sqlc.New(r.tx).DeleteExpiredCompactionCheckpoints(ctx)
}
