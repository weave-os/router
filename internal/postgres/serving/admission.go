// Package serving isolates gateway admission persistence from worker telemetry and inference adapters.
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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/sqlc"
)

// ServingAdmissionRepo serializes identity and session selection on the router primary.
// Lock order is installation, key, subject/access, then conversation for every admission.
type ServingAdmissionRepo struct {
	pool        *pgxpool.Pool
	environment policyregistry.Environment
}

// NewServingAdmissionRepo is wired only for managed gateway mode; legacy startup never calls it.
func NewServingAdmissionRepo(pool *pgxpool.Pool, environment policyregistry.Environment) (*ServingAdmissionRepo, error) {
	if pool == nil {
		return nil, errors.New("serving admission requires router primary pool")
	}
	if err := policyregistry.ValidateEnvironment(environment); err != nil {
		return nil, err
	}
	return &ServingAdmissionRepo{pool: pool, environment: environment}, nil
}

// Admit fails the entire request if either persistence or the authoritative release read fails.
func (r *ServingAdmissionRepo) Admit(ctx context.Context, installationID, apiKeyID, clientSessionID string, decide policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var scope policyregistry.AdmissionScope
	var admitted policyregistry.SessionReleaseBinding
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return scope, admitted, err
	}
	keyUUID, err := uuid.Parse(apiKeyID)
	if err != nil {
		return scope, admitted, err
	}
	if decide == nil {
		return scope, admitted, errors.New("admission decision is required")
	}
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		_, err := queries.GetServingInstallationForAdmission(ctx, installationUUID)
		if err != nil {
			return err
		}
		credential, err := queries.GetServingCredentialForAdmission(ctx, sqlc.GetServingCredentialForAdmissionParams{APIKeyID: keyUUID, InstallationID: installationUUID})
		if err != nil {
			return err
		}
		key := auth.APIKey{ID: apiKeyID, InstallationID: installationID, Scope: auth.APIKeyScope(credential.Scope), CredentialSubjectID: uuidString(credential.CredentialSubjectID)}
		var subject *auth.CredentialSubject
		if credential.CredentialSubjectID.Valid {
			projection, err := queries.GetServingSubjectForAdmission(ctx, sqlc.GetServingSubjectForAdmissionParams{SubjectID: uuid.UUID(credential.CredentialSubjectID.Bytes), InstallationID: installationUUID})
			if err != nil {
				return err
			}
			subject = &auth.CredentialSubject{ID: projection.RouterCredentialSubject.ID.String(), ProjectionComplete: projection.RouterCredentialSubject.ProjectionComplete, InternalEnrolled: projection.RouterCredentialSubject.InternalEnrolled, EnrollmentGeneration: projection.RouterCredentialSubject.EnrollmentGeneration, AccessEnabled: projection.AccessEnabled, RevokedAt: timestamptzPtr(projection.RouterCredentialSubject.RevokedAt)}
		}
		if err := auth.ValidateCredentialSubject(key, subject); err != nil {
			return err
		}
		projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable}
		identity := apiKeyID
		if subject != nil {
			identity = subject.ID
			projection.EnrollmentGeneration = subject.EnrollmentGeneration
			if subject.InternalEnrolled {
				projection.Target = policyregistry.TargetInternal
			}
		}
		if r.environment == policyregistry.EnvironmentStaging {
			projection.Target = policyregistry.TargetStaging
		}
		assignment, err := queries.GetServingProfileAssignment(ctx, installationUUID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			projection.ProfileKey = uuidString(assignment.ProfileKey)
			projection.AssignmentGeneration = assignment.AssignmentGeneration
		}
		digest, persistent := policyregistry.ServingConversationDigest(identity, clientSessionID)
		scope = policyregistry.AdmissionScope{InstallationID: installationID, CredentialIdentity: identity, ConversationDigest: digest, Persistent: persistent}
		var previous *policyregistry.SessionReleaseBinding
		if persistent {
			lockKey := installationID + "/" + identity + "/" + hex.EncodeToString(digest[:])
			if err := queries.GetServingConversationLock(ctx, lockKey); err != nil {
				return err
			}
			encoded, err := queries.GetSessionReleaseBinding(ctx, sqlc.GetSessionReleaseBindingParams{InstallationID: installationUUID, CredentialScope: identity, ConversationDigest: digest[:]})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				previous = &policyregistry.SessionReleaseBinding{}
				if err := json.Unmarshal(encoded, previous); err != nil {
					return fmt.Errorf("decode session release binding: %w", err)
				}
			}
		}
		clock := func(clockCtx context.Context) (time.Time, error) {
			stamp, err := queries.GetServingAdmissionClock(clockCtx)
			if err != nil {
				return time.Time{}, err
			}
			return stamp.Time, nil
		}
		admitted, err = decide(ctx, policyregistry.SerializedAdmission{Projection: projection, Previous: previous, Clock: clock})
		if err != nil {
			return err
		}
		if !persistent {
			return nil
		}
		encoded, err := json.Marshal(admitted)
		if err != nil {
			return err
		}
		activationUUID, err := uuid.Parse(admitted.ActivationID)
		if err != nil {
			return err
		}
		profileUUID := pgtype.UUID{}
		var profileRevision *string
		if admitted.ProfileKey != "" {
			parsed, err := uuid.Parse(admitted.ProfileKey)
			if err != nil {
				return err
			}
			if admitted.Selection.Profile == nil {
				return errors.New("admitted profile lacks exact revision")
			}
			profileUUID = pgtype.UUID{Bytes: parsed, Valid: true}
			profileRevision = &admitted.Selection.Profile.SHA256
		}
		if previous != nil {
			if err := policyregistry.RejectStaleServingGeneration(previous.BindingGeneration, admitted.BindingGeneration); err != nil {
				return err
			}
		}
		updated, err := queries.UpsertSessionReleaseBinding(ctx, sqlc.UpsertSessionReleaseBindingParams{InstallationID: installationUUID, CredentialScope: identity, ConversationDigest: digest[:], Target: string(admitted.Target), ActivationID: activationUUID, ReleaseSha256: admitted.Selection.Release.SHA256, BindingSha256: admitted.Selection.Binding.SHA256, ProfileKey: profileUUID, ProfileRevisionSha256: profileRevision, EnrollmentGeneration: admitted.EnrollmentGeneration, AssignmentGeneration: admitted.AssignmentGeneration, BindingGeneration: admitted.BindingGeneration, Binding: encoded, CreatedAt: pgtype.Timestamptz{Time: admitted.CreatedAt, Valid: true}, LastAdmittedAt: pgtype.Timestamptz{Time: admitted.LastAdmittedAt, Valid: true}})
		if err != nil {
			return err
		}
		if updated == 0 {
			return policyregistry.ErrStaleServingGeneration
		}
		return nil
	})
	if err != nil {
		logger := observability.FromContext(ctx)
		if errors.Is(err, auth.ErrPersonalCredentialRequired) || errors.Is(err, auth.ErrInvalidKeyScope) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, policyregistry.ErrStaleServingGeneration) {
			logger.Debug("Serving admission denied", "installation_id", installationID, "err", err)
		} else {
			logger.Error("Serving admission transaction failed", "installation_id", installationID, "err", err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = auth.ErrInvalidToken
		}
		if errors.Is(err, auth.ErrInvalidKeyScope) {
			err = auth.ErrWrongKeyScope
		}
		return policyregistry.AdmissionScope{}, policyregistry.SessionReleaseBinding{}, err
	}
	return scope, admitted, nil
}

var _ policyregistry.ServingAdmissionStore = (*ServingAdmissionRepo)(nil)
