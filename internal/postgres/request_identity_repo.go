package postgres

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
)

type requestIdentityRepo struct {
	tx sqlc.DBTX
}

// NewRequestIdentityRepo constructs the read side of Weave's email-to-subject projection.
func NewRequestIdentityRepo(tx sqlc.DBTX) auth.RequestIdentityRepository {
	return &requestIdentityRepo{tx: tx}
}

func (r *requestIdentityRepo) GetSubscriberForEmail(ctx context.Context, installationID, email string) (string, error) {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return "", err
	}
	subjectID, err := sqlc.New(r.tx).GetCredentialSubjectForRequestEmail(ctx, sqlc.GetCredentialSubjectForRequestEmailParams{
		InstallationID: installationUUID, Email: email,
	})
	if err != nil {
		return "", err
	}
	return subjectID.String(), nil
}
