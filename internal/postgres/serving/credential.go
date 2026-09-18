package serving

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"
)

// CredentialLookup reads only the credential identity needed for the subsequent locked admission.
type CredentialLookup struct{ Queries *sqlc.Queries }

// GetActiveByHashWithInstallation reuses routing-key SQLC predicates without loading BYOK or user settings.
func (r CredentialLookup) GetActiveByHashWithInstallation(ctx context.Context, hash string) (*auth.APIKey, *auth.Installation, error) {
	row, err := r.Queries.GetActiveModelRouterAPIKeyWithInstallationByHash(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	key := row.RouterModelRouterAPIKey
	return &auth.APIKey{ID: key.ID.String(), InstallationID: key.InstallationID.String(), Scope: auth.APIKeyScope(key.Scope), CredentialSubjectID: uuidString(key.CredentialSubjectID)}, &auth.Installation{ID: row.RouterModelRouterInstallation.ID.String()}, nil
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func timestamptzPtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
