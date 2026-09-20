package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/sqlc"
)

// CredentialSubjectRepo owns atomic personal-key issuance and authenticated projection writes.
// Only the private control plane maps subjects to accounts; no public router endpoint exposes these writes.
type CredentialSubjectRepo struct{ pool *pgxpool.Pool }

// NewCredentialSubjectRepo binds subject operations to the router primary.
func NewCredentialSubjectRepo(pool *pgxpool.Pool) *CredentialSubjectRepo {
	return &CredentialSubjectRepo{pool: pool}
}

// CreatePending issues a new subject and key together without touching an installation-shared key.
func (r *CredentialSubjectRepo) CreatePending(ctx context.Context, externalID string, key auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	installationID, err := uuid.Parse(key.InstallationID)
	if err != nil {
		return nil, err
	}
	if key.Scope.Normalized() != auth.ScopeRouting {
		return nil, auth.ErrInvalidKeyScope
	}
	var created *auth.APIKey
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		installations, err := queries.GetServingInstallationsForProjection(ctx, sqlc.GetServingInstallationsForProjectionParams{InstallationIds: []uuid.UUID{installationID}, ExternalID: externalID})
		if err != nil {
			return err
		}
		if len(installations) != 1 {
			return auth.ErrInstallationNotFound
		}
		subject, err := queries.InsertCredentialSubject(ctx)
		if err != nil {
			return err
		}
		if err := queries.InsertCredentialSubjectInstallation(ctx, sqlc.InsertCredentialSubjectInstallationParams{SubjectID: subject.ID, InstallationID: installationID}); err != nil {
			return err
		}
		row, err := queries.InsertPersonalRoutingKey(ctx, sqlc.InsertPersonalRoutingKeyParams{InstallationID: installationID, SubjectID: subject.ID, ExternalID: key.ExternalID, Name: key.Name, KeyPrefix: key.KeyPrefix, KeyHash: key.KeyHash, KeySuffix: key.KeySuffix})
		if err != nil {
			return err
		}
		created = toAuthAPIKey(row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create pending personal credential: %w", err)
	}
	return created, nil
}

// Rotate replaces only a credential owned by the authenticated subject; its subject survives rotation.
func (r *CredentialSubjectRepo) Rotate(ctx context.Context, externalID, subjectID, previousKeyID string, replacement auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	installationID, err := uuid.Parse(replacement.InstallationID)
	if err != nil {
		return nil, err
	}
	subjectUUID, err := uuid.Parse(subjectID)
	if err != nil {
		return nil, err
	}
	keyUUID, err := uuid.Parse(previousKeyID)
	if err != nil {
		return nil, err
	}
	if replacement.Scope.Normalized() != auth.ScopeRouting {
		return nil, auth.ErrInvalidKeyScope
	}
	var created *auth.APIKey
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		installations, err := queries.GetServingInstallationsForProjection(ctx, sqlc.GetServingInstallationsForProjectionParams{InstallationIds: []uuid.UUID{installationID}, ExternalID: externalID})
		if err != nil {
			return err
		}
		if len(installations) != 1 {
			return auth.ErrInstallationNotFound
		}
		key, err := queries.GetServingCredentialForAdmission(ctx, sqlc.GetServingCredentialForAdmissionParams{APIKeyID: keyUUID, InstallationID: installationID})
		if err != nil {
			return err
		}
		if !key.CredentialSubjectID.Valid || uuid.UUID(key.CredentialSubjectID.Bytes) != subjectUUID {
			return auth.ErrPersonalCredentialRequired
		}
		projection, err := queries.GetServingSubjectForAdmission(ctx, sqlc.GetServingSubjectForAdmissionParams{SubjectID: subjectUUID, InstallationID: installationID})
		if err != nil {
			return err
		}
		if !projection.RouterCredentialSubject.ProjectionComplete || projection.RouterCredentialSubject.RevokedAt.Valid || !projection.AccessEnabled {
			return auth.ErrPersonalCredentialRequired
		}
		changed, err := queries.SoftDeleteModelRouterAPIKey(ctx, sqlc.SoftDeleteModelRouterAPIKeyParams{ID: keyUUID, InstallationID: installationID})
		if err != nil {
			return err
		}
		if changed != 1 {
			return auth.ErrAPIKeyNotFound
		}
		row, err := queries.InsertPersonalRoutingKey(ctx, sqlc.InsertPersonalRoutingKeyParams{InstallationID: installationID, SubjectID: subjectUUID, ExternalID: replacement.ExternalID, Name: replacement.Name, KeyPrefix: replacement.KeyPrefix, KeyHash: replacement.KeyHash, KeySuffix: replacement.KeySuffix})
		if err != nil {
			return err
		}
		created = toAuthAPIKey(row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("rotate personal credential: %w", err)
	}
	return created, nil
}

// ProjectProfile writes one organization's installation assignments atomically after registration.
// The caller must verify both production target selection sets before making this projection effective.
func (r *CredentialSubjectRepo) ProjectProfile(ctx context.Context, externalID string, installationIDs []string, profileKey string) error {
	if len(installationIDs) == 0 {
		return errors.New("profile projection requires installations")
	}
	parsedIDs := make([]uuid.UUID, 0, len(installationIDs))
	seen := make(map[uuid.UUID]struct{}, len(installationIDs))
	for _, id := range installationIDs {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return err
		}
		if _, duplicate := seen[parsed]; duplicate {
			return errors.New("duplicate profile projection installation")
		}
		seen[parsed] = struct{}{}
		parsedIDs = append(parsedIDs, parsed)
	}
	profile := uuidOrNil("")
	if profileKey != "" {
		parsed, err := uuid.Parse(profileKey)
		if err != nil || parsed == uuid.Nil {
			return errors.New("invalid profile assignment key")
		}
		profile = uuidOrNil(parsed.String())
	}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		locked, err := queries.GetServingInstallationsForProjection(ctx, sqlc.GetServingInstallationsForProjectionParams{InstallationIds: parsedIDs, ExternalID: externalID})
		if err != nil {
			return err
		}
		if len(locked) != len(parsedIDs) {
			return auth.ErrInstallationNotFound
		}
		for _, id := range locked {
			if _, err := queries.UpsertServingProfileAssignment(ctx, sqlc.UpsertServingProfileAssignmentParams{InstallationID: id, ProfileKey: profile}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("project organization profile assignment: %w", err)
	}
	return nil
}

// ProjectSubjectProfile writes one opaque subject assignment source after profile registration.
func (r *CredentialSubjectRepo) ProjectSubjectProfile(ctx context.Context, externalID, subjectID, installationID string, source policyregistry.AdmissionAssignmentSource, state policyregistry.AdmissionAssignmentState, desiredGeneration int64, profileKey, evidenceID string, failureDetail *string) error {
	if desiredGeneration <= 0 {
		return errors.New("subject profile projection requires a positive desired generation")
	}
	if evidenceID == "" {
		return errors.New("subject profile projection requires evidence identity")
	}
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return err
	}
	subjectUUID, err := uuid.Parse(subjectID)
	if err != nil {
		return err
	}
	profile := uuidOrNil("")
	if profileKey != "" {
		parsed, err := uuid.Parse(profileKey)
		if err != nil || parsed == uuid.Nil {
			return errors.New("invalid profile assignment key")
		}
		profile = uuidOrNil(parsed.String())
	}
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		installations, err := queries.GetServingInstallationsForProjection(ctx, sqlc.GetServingInstallationsForProjectionParams{InstallationIds: []uuid.UUID{installationUUID}, ExternalID: externalID})
		if err != nil {
			return err
		}
		if len(installations) != 1 {
			return auth.ErrInstallationNotFound
		}
		locked, err := queries.GetCredentialSubjectInstallationForProfileProjection(ctx, sqlc.GetCredentialSubjectInstallationForProfileProjectionParams{SubjectID: subjectUUID, InstallationID: installationUUID})
		if err != nil {
			return err
		}
		if locked != subjectUUID {
			return auth.ErrPersonalCredentialRequired
		}
		_, err = queries.UpsertCredentialSubjectProfileAssignment(ctx, sqlc.UpsertCredentialSubjectProfileAssignmentParams{SubjectID: subjectUUID, InstallationID: installationUUID, AssignmentSource: string(source), AssignmentState: string(state), DesiredGeneration: desiredGeneration, DesiredProfileKey: profile, EvidenceID: evidenceID, LastFailureDetail: failureDetail})
		return err
	})
	if err != nil {
		return fmt.Errorf("project subject profile assignment: %w", err)
	}
	return nil
}
