package serving

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/router"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions/entitlement"
)

type ServingAdmissionRepo struct {
	pool          *pgxpool.Pool
	environment   policyregistry.Environment
	worker        policyregistry.ServingTarget
	workerChannel policyregistry.ServingChannel
}

func (r *ServingAdmissionRepo) ForWorker(worker policyregistry.ServingTarget, channel policyregistry.ServingChannel) *ServingAdmissionRepo {
	scoped := *r
	scoped.worker = worker
	scoped.workerChannel = channel
	return &scoped
}

func NewServingAdmissionRepo(pool *pgxpool.Pool, environment policyregistry.Environment) (*ServingAdmissionRepo, error) {
	if pool == nil {
		return nil, errors.New("serving admission requires router primary pool")
	}
	if err := policyregistry.ValidateEnvironment(environment); err != nil {
		return nil, err
	}
	return &ServingAdmissionRepo{pool: pool, environment: environment}, nil
}

func (r *ServingAdmissionRepo) Admit(ctx context.Context, installationID, apiKeyID, clientSessionID string, decide policyregistry.AdmissionDecision) (policyregistry.AdmissionScope, policyregistry.SessionReleaseBinding, error) {
	timing := admissionTiming{started: time.Now(), outcome: admissionFailed}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() { timing.emit(ctx) }()
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
	transactionStart := time.Now()
	err = pgx.BeginTxFunc(ctx, dbbudget.NewDBTX(r.pool), pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		projectionStart := time.Now()
		recordProjection := func() {
			if timing.projection == 0 {
				timing.projection = time.Since(projectionStart)
			}
		}
		defer recordProjection()
		queries := dbbudget.Queries(tx)
		installation, err := queries.GetServingInstallationForAdmission(ctx, installationUUID)
		if err != nil {
			return err
		}
		subject, err := admitServingCredential(ctx, queries, installationUUID, keyUUID, apiKeyID, installationID)
		if err != nil {
			return err
		}
		projection, err := r.projectServingTarget(ctx, queries, installationUUID, subject)
		if err != nil {
			return err
		}
		projection.RoutingControlsDigest = policyregistry.RoutingControlsDigest(installation.AllowedModels, installation.ExcludedModels, installation.ExcludedProviders, installation.RoutingStrategy)
		identity := apiKeyID
		if subject != nil {
			identity = subject.ID
		}
		timing.target = projection.Target
		digest, persistent := policyregistry.ServingConversationDigest(identity, clientSessionID)
		scope = policyregistry.AdmissionScope{InstallationID: installationID, CredentialIdentity: identity, ConversationDigest: digest, Persistent: persistent}
		timing.persistent = persistent
		recordProjection()
		previous, err := lockSessionBinding(ctx, queries, installationUUID, scope, &timing)
		if err != nil {
			return err
		}
		if err := policyregistry.RejectSessionChannelChange(previous, projection.Target); err != nil {
			return err
		}
		profileStart := time.Now()
		routingModel, inherited := policyregistry.AdmissionRoutingModel(ctx, previous)
		if err := projectServingProfile(ctx, queries, installationUUID, subject, routingModel, &projection); err != nil {
			return err
		}
		timing.projection += time.Since(profileStart)
		clock := func(clockCtx context.Context) (time.Time, error) {
			stamp, err := queries.GetServingAdmissionClock(clockCtx)
			if err != nil {
				return time.Time{}, err
			}
			return stamp.Time, nil
		}
		decideStart := time.Now()
		admitted, err = decide(ctx, policyregistry.SerializedAdmission{Projection: projection, Previous: previous, Clock: clock, Timings: &timing.registry})
		timing.decide = time.Since(decideStart)
		if err != nil {
			return err
		}
		if !persistent {
			return nil
		}
		if inherited && previous.Target == admitted.Target {
			admitted.RoutingModel = routingModel
		}
		return persistSessionBinding(ctx, queries, installationUUID, scope, previous, admitted)
	})
	timing.transaction = time.Since(transactionStart)
	if err != nil {
		timing.outcome = admissionOutcomeFor(err)
		return policyregistry.AdmissionScope{}, policyregistry.SessionReleaseBinding{}, logAdmissionFailure(ctx, installationID, err)
	}
	timing.outcome = admissionAdmitted
	timing.activationID = admitted.ActivationID
	return scope, admitted, nil
}

var _ policyregistry.ServingAdmissionStore = (*ServingAdmissionRepo)(nil)

func subscriberPlanProjection(planValue string, version int64) (policyregistry.AdmissionProjection, error) {
	plan := entitlement.Plan(planValue)
	profile, ok := entitlement.ServingProfileFor(plan)
	if !ok || version <= 0 {
		return policyregistry.AdmissionProjection{}, errors.New("active subscriber plan has no valid routing policy")
	}
	return policyregistry.AdmissionProjection{
		ProfileKey:           profile.Key,
		ProfileName:          profile.Name,
		Plan:                 plan,
		EntitlementVersion:   version,
		AssignmentGeneration: version,
	}, nil
}

func enrollmentProjection(lane sqlc.GetServingInstallationLaneEnrollmentRow, subject *auth.CredentialSubject) policyregistry.AdmissionProjection {
	projection := policyregistry.AdmissionProjection{Target: policyregistry.TargetStable, EnrollmentGeneration: lane.EnrollmentGeneration}
	if lane.InternalEnrolled {
		projection.Target = policyregistry.TargetInternal
	}
	if subject != nil {
		projection.EnrollmentGeneration += subject.EnrollmentGeneration
		if subject.InternalEnrolled {
			projection.Target = policyregistry.TargetInternal
		}
	}
	return projection
}

func projectServingProfile(ctx context.Context, queries *sqlc.Queries, installationUUID uuid.UUID, subject *auth.CredentialSubject, routingModel router.RoutingModel, projection *policyregistry.AdmissionProjection) error {
	if subject != nil {
		activePlan, err := queries.GetActiveServingSubscriberPlan(ctx, uuid.MustParse(subject.ID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			planProjection, err := subscriberPlanProjection(activePlan.Plan, activePlan.Version)
			if err != nil {
				return err
			}
			projection.ProfileKey = planProjection.ProfileKey
			projection.ProfileName = planProjection.ProfileName
			projection.Plan = planProjection.Plan
			projection.EntitlementVersion = planProjection.EntitlementVersion
			projection.AssignmentGeneration = planProjection.AssignmentGeneration
			if routingModel != router.ModelAutomatic {
				return policyregistry.ErrRoutingModelNotAssigned
			}
			return nil
		}
	}
	if routingModel != router.ModelAutomatic {
		assignment, err := queries.GetServingRoutingModelProfileAssignment(ctx, sqlc.GetServingRoutingModelProfileAssignmentParams{InstallationID: installationUUID, RoutingModel: string(routingModel)})
		if errors.Is(err, sql.ErrNoRows) {
			return policyregistry.ErrRoutingModelNotAssigned
		}
		if err != nil {
			return err
		}
		projection.ProfileKey = assignment.ProfileKey.String()
		projection.AssignmentGeneration = assignment.AssignmentGeneration
		return nil
	}
	assignment, err := queries.GetServingProfileAssignment(ctx, installationUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	projection.ProfileKey = uuidString(assignment.ProfileKey)
	if projection.ProfileKey == entitlement.LegacyBoostProfileKey {
		projection.ProfileKey = ""
	}
	projection.AssignmentGeneration = assignment.AssignmentGeneration
	return nil
}
