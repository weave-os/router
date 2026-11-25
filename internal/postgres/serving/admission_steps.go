package serving

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/sqlc"
)

func admitServingCredential(ctx context.Context, queries *sqlc.Queries, installationUUID, keyUUID uuid.UUID, apiKeyID, installationID string) (*auth.CredentialSubject, error) {
	credential, err := queries.GetServingCredentialForAdmission(ctx, sqlc.GetServingCredentialForAdmissionParams{APIKeyID: keyUUID, InstallationID: installationUUID})
	if err != nil {
		return nil, err
	}
	key := auth.APIKey{ID: apiKeyID, InstallationID: installationID, Scope: auth.APIKeyScope(credential.Scope), CredentialSubjectID: uuidString(credential.CredentialSubjectID)}
	var subject *auth.CredentialSubject
	if credential.CredentialSubjectID.Valid {
		projection, err := queries.GetServingSubjectForAdmission(ctx, sqlc.GetServingSubjectForAdmissionParams{SubjectID: uuid.UUID(credential.CredentialSubjectID.Bytes), InstallationID: installationUUID})
		if err != nil {
			return nil, err
		}
		subject = &auth.CredentialSubject{ID: projection.RouterCredentialSubject.ID.String(), ProjectionComplete: projection.RouterCredentialSubject.ProjectionComplete, InternalEnrolled: projection.RouterCredentialSubject.InternalEnrolled, EnrollmentGeneration: projection.RouterCredentialSubject.EnrollmentGeneration, AccessEnabled: projection.AccessEnabled, RevokedAt: timestamptzPtr(projection.RouterCredentialSubject.RevokedAt)}
	}
	if err := auth.ValidateServingCredential(key, subject); err != nil {
		return nil, err
	}
	return subject, nil
}

func (r *ServingAdmissionRepo) projectServingTarget(ctx context.Context, queries *sqlc.Queries, installationUUID uuid.UUID, subject *auth.CredentialSubject) (policyregistry.AdmissionProjection, error) {
	lane, err := queries.GetServingInstallationLaneEnrollment(ctx, installationUUID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return policyregistry.AdmissionProjection{}, err
	}
	projection := enrollmentProjection(lane, subject)
	if r.environment == policyregistry.EnvironmentStaging {
		projection.Target = policyregistry.TargetStaging
	}
	rows, err := queries.ListServingInstallationChannelPermissions(ctx, installationUUID)
	if err != nil {
		return policyregistry.AdmissionProjection{}, err
	}
	permissions, err := channelPermissions(rows)
	if err != nil {
		return policyregistry.AdmissionProjection{}, err
	}
	resolution, err := policyregistry.ChannelAdmission{Worker: r.worker, WorkerChannel: r.workerChannel, Enrolled: projection.Target, Permissions: permissions}.ResolveTarget()
	if err != nil {
		return policyregistry.AdmissionProjection{}, err
	}
	projection.Target = resolution.Target
	projection.EnrollmentGeneration += resolution.Generation
	return projection, nil
}

func channelPermissions(rows []sqlc.ListServingInstallationChannelPermissionsRow) ([]policyregistry.ChannelPermission, error) {
	permissions := make([]policyregistry.ChannelPermission, 0, len(rows))
	for _, row := range rows {
		channel, err := policyregistry.ParseServingChannel(row.Channel)
		if err != nil {
			return nil, err
		}
		permissions = append(permissions, policyregistry.ChannelPermission{Channel: channel, Generation: row.Generation})
	}
	return permissions, nil
}

func lockSessionBinding(ctx context.Context, queries *sqlc.Queries, scope policyregistry.AdmissionScope, timing *admissionTiming) (*policyregistry.SessionReleaseBinding, error) {
	if !scope.Persistent {
		return nil, nil
	}
	lockKey := scope.InstallationID + "/" + scope.CredentialIdentity + "/" + hex.EncodeToString(scope.ConversationDigest[:])
	lockStart := time.Now()
	err := queries.GetServingConversationLock(ctx, lockKey)
	timing.lockWait += time.Since(lockStart)
	if err != nil {
		return nil, err
	}
	installationUUID, err := uuid.Parse(scope.InstallationID)
	if err != nil {
		return nil, err
	}
	encoded, err := queries.GetSessionReleaseBinding(ctx, sqlc.GetSessionReleaseBindingParams{InstallationID: installationUUID, CredentialScope: scope.CredentialIdentity, ConversationDigest: scope.ConversationDigest[:]})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	previous := &policyregistry.SessionReleaseBinding{}
	if err := json.Unmarshal(encoded, previous); err != nil {
		return nil, fmt.Errorf("decode session release binding: %w", err)
	}
	return previous, nil
}

func persistSessionBinding(ctx context.Context, queries *sqlc.Queries, scope policyregistry.AdmissionScope, previous *policyregistry.SessionReleaseBinding, admitted policyregistry.SessionReleaseBinding) error {
	encoded, err := json.Marshal(admitted)
	if err != nil {
		return err
	}
	installationUUID, err := uuid.Parse(scope.InstallationID)
	if err != nil {
		return err
	}
	activationUUID, err := uuid.Parse(admitted.ActivationID)
	if err != nil {
		return err
	}
	profileUUID, profileRevision, err := admittedProfile(admitted)
	if err != nil {
		return err
	}
	if previous != nil {
		if err := policyregistry.RejectStaleServingGeneration(previous.BindingGeneration, admitted.BindingGeneration); err != nil {
			return err
		}
	}
	digest := scope.ConversationDigest
	updated, err := queries.UpsertSessionReleaseBinding(ctx, sqlc.UpsertSessionReleaseBindingParams{InstallationID: installationUUID, CredentialScope: scope.CredentialIdentity, ConversationDigest: digest[:], Target: string(admitted.Target), ActivationID: activationUUID, ReleaseSha256: admitted.Selection.Release.SHA256, BindingSha256: admitted.Selection.Binding.SHA256, ProfileKey: profileUUID, ProfileRevisionSha256: profileRevision, EnrollmentGeneration: admitted.EnrollmentGeneration, AssignmentGeneration: admitted.AssignmentGeneration, BindingGeneration: admitted.BindingGeneration, Binding: encoded, CreatedAt: pgtype.Timestamptz{Time: admitted.CreatedAt, Valid: true}, LastAdmittedAt: pgtype.Timestamptz{Time: admitted.LastAdmittedAt, Valid: true}})
	if err != nil {
		return err
	}
	if updated == 0 {
		return policyregistry.ErrStaleServingGeneration
	}
	return nil
}

func admittedProfile(admitted policyregistry.SessionReleaseBinding) (pgtype.UUID, *string, error) {
	if admitted.ProfileKey == "" {
		return pgtype.UUID{}, nil, nil
	}
	parsed, err := uuid.Parse(admitted.ProfileKey)
	if err != nil {
		return pgtype.UUID{}, nil, err
	}
	if admitted.Selection.Profile == nil {
		return pgtype.UUID{}, nil, errors.New("admitted profile lacks exact revision")
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, &admitted.Selection.Profile.SHA256, nil
}

func admissionRefused(err error) bool {
	return errors.Is(err, auth.ErrPersonalCredentialRequired) || errors.Is(err, auth.ErrInvalidKeyScope) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, policyregistry.ErrStaleServingGeneration) || errors.Is(err, policyregistry.ErrRoutingModelNotAssigned) || errors.Is(err, policyregistry.ErrServingChannelNotPermitted) || errors.Is(err, policyregistry.ErrServingChannelChanged)
}

func admissionOutcomeFor(err error) admissionOutcome {
	if admissionRefused(err) {
		return admissionDenied
	}
	return admissionFailed
}

func logAdmissionFailure(ctx context.Context, installationID string, err error) error {
	logger := observability.FromContext(ctx)
	if admissionRefused(err) {
		logger.Debug("Serving admission denied", "installation_id", installationID, "err", err)
	} else {
		logger.Error("Serving admission transaction failed", "installation_id", installationID, "err", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrInvalidToken
	}
	if errors.Is(err, auth.ErrInvalidKeyScope) {
		return auth.ErrWrongKeyScope
	}
	return err
}
