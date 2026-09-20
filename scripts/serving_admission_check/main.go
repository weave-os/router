// Command serving_admission_check exercises primary-database admission against an ephemeral fixture.
// It refuses non-loopback databases and never logs routing credentials.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/postgres/serving"
	"weave-os/router/internal/sqlc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Serving admission integration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Serving admission integration passed")
}

func run() error {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	parsed, err := url.Parse(dsn)
	if err != nil || dsn == "" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		return errors.New("ROUTER_TEST_DATABASE_URL must name an ephemeral loopback Postgres fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	repositories := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	externalID := "fixture_" + uuid.NewString()[:16]
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: externalID, Name: "Serving admission fixture"})
	if err != nil {
		return err
	}
	control := postgres.NewCredentialSubjectRepo(pool)
	personal, err := control.CreatePending(ctx, externalID, newKey(installation.ID))
	if err != nil {
		return err
	}
	shared, err := repositories.APIKeys.Create(ctx, newKey(installation.ID))
	if err != nil {
		return err
	}
	admissions, err := serving.NewServingAdmissionRepo(pool, policyregistry.EnvironmentProd)
	if err != nil {
		return err
	}
	var firstAdmissions atomic.Int64
	decide := func(ctx context.Context, admission policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
		if admission.Projection.ProfileRequired && admission.Projection.ProfileKey == "" {
			return policyregistry.SessionReleaseBinding{}, errors.New("required profile projection missing")
		}
		if admission.Previous == nil {
			firstAdmissions.Add(1)
		}
		now, err := admission.Clock(ctx)
		if err != nil {
			return policyregistry.SessionReleaseBinding{}, err
		}
		return fixtureBinding(admission, now), nil
	}
	_, _, err = admissions.Admit(ctx, installation.ID, personal.ID, "conversation", decide)
	if !errors.Is(err, auth.ErrPersonalCredentialRequired) {
		return fmt.Errorf("pending subject unexpectedly admitted: %w", err)
	}
	queries := sqlc.New(pool)
	subjectID, err := uuid.Parse(personal.CredentialSubjectID)
	if err != nil {
		return err
	}
	installationUUID, err := uuid.Parse(installation.ID)
	if err != nil {
		return err
	}
	_, err = queries.UpdateCredentialSubjectProjection(ctx, subjectID)
	if err != nil {
		return err
	}
	_, err = queries.UpdateCredentialSubjectInstallationAccess(ctx, sqlc.UpdateCredentialSubjectInstallationAccessParams{SubjectID: subjectID, InstallationID: installationUUID, AccessEnabled: true})
	if err != nil {
		return err
	}
	_, err = queries.UpdateCredentialSubjectEnrollment(ctx, sqlc.UpdateCredentialSubjectEnrollmentParams{SubjectID: subjectID, Enrolled: true})
	if err != nil {
		return err
	}
	profileKey := uuid.NewString()
	_, _, err = admissions.Admit(ctx, installation.ID, personal.ID, "conversation", decide)
	if err == nil {
		return errors.New("subject without explicit assignment reached lane default")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, personal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStateEffective, 1, profileKey, "fixture-subscriber-plan", nil); err != nil {
		return err
	}
	secondPersonal, err := control.CreatePending(ctx, externalID, newKey(installation.ID))
	if err != nil {
		return err
	}
	secondSubjectID := uuid.MustParse(secondPersonal.CredentialSubjectID)
	if _, err := queries.UpdateCredentialSubjectProjection(ctx, secondSubjectID); err != nil {
		return err
	}
	if _, err := queries.UpdateCredentialSubjectInstallationAccess(ctx, sqlc.UpdateCredentialSubjectInstallationAccessParams{SubjectID: secondSubjectID, InstallationID: installationUUID, AccessEnabled: true}); err != nil {
		return err
	}
	if _, err := queries.UpdateCredentialSubjectEnrollment(ctx, sqlc.UpdateCredentialSubjectEnrollmentParams{SubjectID: secondSubjectID, Enrolled: true}); err != nil {
		return err
	}
	secondSubscriberProfileKey := uuid.NewString()
	if err := control.ProjectSubjectProfile(ctx, externalID, secondPersonal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStateEffective, 1, secondSubscriberProfileKey, "fixture-second-subscriber-plan", nil); err != nil {
		return err
	}
	_, secondPlanBinding, err := admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if secondPlanBinding.ProfileKey != secondSubscriberProfileKey || secondPlanBinding.AssignmentSource != policyregistry.AssignmentSourceSubscriberPlan {
		return errors.New("second subject did not receive its subscriber plan")
	}
	secondProfileKey := uuid.NewString()
	if err := control.ProjectSubjectProfile(ctx, externalID, secondPersonal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceCohort, policyregistry.AssignmentStateEffective, 1, secondProfileKey, "fixture-second-cohort", nil); err != nil {
		return err
	}
	_, secondBinding, err := admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if secondBinding.ProfileKey != secondProfileKey || secondBinding.AssignmentSource != policyregistry.AssignmentSourceCohort || secondBinding.BindingGeneration != 2 {
		return errors.New("cohort assignment did not precede the second subject plan")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, secondPersonal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceCohort, policyregistry.AssignmentStateAbsent, 2, "", "fixture-removed-cohort", nil); err != nil {
		return err
	}
	_, secondFallback, err := admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if secondFallback.ProfileKey != secondSubscriberProfileKey || secondFallback.AssignmentSource != policyregistry.AssignmentSourceSubscriberPlan || secondFallback.BindingGeneration != 3 {
		return errors.New("removed cohort did not restore the second subject plan")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, secondPersonal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceCohort, policyregistry.AssignmentStatePending, 3, secondProfileKey, "fixture-pending-cohort", nil); err != nil {
		return err
	}
	_, pendingFallback, err := admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if pendingFallback.BindingGeneration != secondFallback.BindingGeneration || pendingFallback.AssignmentSource != policyregistry.AssignmentSourceSubscriberPlan {
		return errors.New("pending cohort displaced the previous effective subject plan")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, secondPersonal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceCohort, policyregistry.AssignmentStateEffective, 3, secondProfileKey, "fixture-restored-cohort", nil); err != nil {
		return err
	}
	if _, secondBinding, err = admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide); err != nil || secondBinding.ProfileKey != secondProfileKey || secondBinding.BindingGeneration != 4 {
		return fmt.Errorf("restored cohort assignment was not admitted: %w", err)
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, secondPersonal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceCohort, policyregistry.AssignmentStateRevoked, 4, secondProfileKey, "fixture-revoked-cohort", nil); err != nil {
		return err
	}
	if _, _, err := admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide); err == nil {
		return errors.New("revoked subject assignment remained admissible")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, secondPersonal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceCohort, policyregistry.AssignmentStateEffective, 5, secondProfileKey, "fixture-restored-cohort-after-revocation", nil); err != nil {
		return err
	}
	if _, secondBinding, err = admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide); err != nil || secondBinding.ProfileKey != secondProfileKey || secondBinding.BindingGeneration != 5 {
		return fmt.Errorf("restored cohort assignment was not admitted: %w", err)
	}
	firstAdmissions.Store(0)
	group, concurrentCtx := errgroup.WithContext(ctx)
	for range 16 {
		group.Go(func() error {
			_, binding, err := admissions.Admit(concurrentCtx, installation.ID, personal.ID, "conversation", decide)
			if err != nil {
				return err
			}
			if binding.BindingGeneration != 1 || binding.Target != policyregistry.TargetInternal || binding.ProfileKey != profileKey {
				return errors.New("concurrent admission lost its original generation, enrollment or profile")
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	if firstAdmissions.Load() != 1 {
		return fmt.Errorf("concurrent first requests created %d bindings", firstAdmissions.Load())
	}
	_, sharedBinding, err := admissions.Admit(ctx, installation.ID, shared.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if sharedBinding.Target != policyregistry.TargetStable || sharedBinding.ProfileKey != "" {
		return errors.New("shared key gained internal access or subject profile")
	}
	orgProfileKey := uuid.NewString()
	if err := control.ProjectProfile(ctx, externalID, []string{installation.ID}, orgProfileKey); err != nil {
		return err
	}
	_, overrideBinding, err := admissions.Admit(ctx, installation.ID, personal.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if overrideBinding.ProfileKey != orgProfileKey || overrideBinding.BindingGeneration != 2 {
		return errors.New("organization override did not take precedence over subject assignment")
	}
	if err := control.ProjectProfile(ctx, externalID, []string{installation.ID}, ""); err != nil {
		return err
	}
	_, restoredBinding, err := admissions.Admit(ctx, installation.ID, personal.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if restoredBinding.ProfileKey != profileKey || restoredBinding.BindingGeneration != 3 {
		return errors.New("cleared organization override did not restore subject assignment")
	}
	if err := checkRetainedAdmissionClock(ctx, admissions, installation.ID, shared.ID); err != nil {
		return err
	}
	replacement, err := control.Rotate(ctx, externalID, personal.CredentialSubjectID, personal.ID, newKey(installation.ID))
	if err != nil {
		return err
	}
	_, rotatedBinding, err := admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if rotatedBinding.BindingGeneration != restoredBinding.BindingGeneration || replacement.CredentialSubjectID != personal.CredentialSubjectID {
		return errors.New("personal rotation lost subject or conversation continuity")
	}
	_, _, err = admissions.Admit(ctx, installation.ID, personal.ID, "conversation", decide)
	if err == nil {
		return errors.New("revoked predecessor credential remained admissible")
	}
	_, err = control.Rotate(ctx, externalID, personal.CredentialSubjectID, shared.ID, newKey(installation.ID))
	if !errors.Is(err, auth.ErrPersonalCredentialRequired) {
		return errors.New("personal rotation accepted an installation-shared key")
	}
	_, err = queries.UpdateCredentialSubjectEnrollment(ctx, sqlc.UpdateCredentialSubjectEnrollmentParams{SubjectID: subjectID, Enrolled: false})
	if err != nil {
		return err
	}
	disabledScope, disabled, err := admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if disabled.Target != policyregistry.TargetStable || disabled.ProfileKey != profileKey || disabled.BindingGeneration != 4 {
		return errors.New("enrollment disable did not rebind while retaining profile")
	}
	if err := checkAttribution(ctx, pool, policyregistry.ServingAssertion{APIKeyID: replacement.ID, Scope: disabledScope, Admission: disabled}); err != nil {
		return err
	}
	_, _, err = admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", func(_ context.Context, admission policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
		if admission.Previous == nil {
			return policyregistry.SessionReleaseBinding{}, errors.New("stale generation check missing previous binding")
		}
		stale := *admission.Previous
		stale.BindingGeneration = 1
		return stale, nil
	})
	if !errors.Is(err, policyregistry.ErrStaleServingGeneration) {
		return fmt.Errorf("stale generation overwrote rebound session: %v", err)
	}
	failureDetail := "fixture missing target profile"
	if err := control.ProjectSubjectProfile(ctx, externalID, personal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStateFailed, 2, uuid.NewString(), "fixture-failed-plan", &failureDetail); err != nil {
		return err
	}
	_, preserved, err := admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if preserved.ProfileKey != profileKey || preserved.AssignmentGeneration != disabled.AssignmentGeneration {
		return errors.New("failed subject projection altered the effective profile")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, personal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStateDefaultFollowing, 3, "", "fixture-default-following", nil); err != nil {
		return err
	}
	_, defaulted, err := admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if defaulted.ProfileKey != "" || defaulted.BindingGeneration != 5 {
		return errors.New("explicit default-following subject did not use lane default")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, personal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStateEffective, 4, profileKey, "fixture-restored-plan", nil); err != nil {
		return err
	}
	_, restoredPlan, err := admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if restoredPlan.ProfileKey != profileKey || restoredPlan.BindingGeneration != 6 {
		return errors.New("restored subscriber plan did not rebind from lane default")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, personal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStatePending, 4, profileKey, "fixture-regressed-plan", nil); err == nil {
		return errors.New("terminal subject assignment generation regressed to pending")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, personal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStateEffective, 3, uuid.NewString(), "fixture-stale-plan", nil); err == nil {
		return errors.New("stale subject assignment generation was accepted")
	}
	if err := control.ProjectSubjectProfile(ctx, externalID, personal.CredentialSubjectID, installation.ID, policyregistry.AssignmentSourceSubscriberPlan, policyregistry.AssignmentStateEffective, 4, uuid.NewString(), "fixture-conflicting-plan", nil); err == nil {
		return errors.New("conflicting subject assignment generation was accepted")
	}
	if err := control.ProjectProfile(ctx, "wrong-owner", []string{installation.ID}, uuid.NewString()); !errors.Is(err, auth.ErrInstallationNotFound) {
		return errors.New("wrong organization projected an installation profile")
	}
	_, unchanged, err := admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if unchanged.ProfileKey != profileKey || unchanged.AssignmentGeneration != restoredPlan.AssignmentGeneration {
		return errors.New("failed projection altered the effective profile")
	}
	for range 2 {
		scope, requestScoped, err := admissions.Admit(ctx, installation.ID, replacement.ID, "", decide)
		if err != nil {
			return err
		}
		if scope.Persistent || requestScoped.BindingGeneration != 1 {
			return errors.New("missing conversation ID persisted a shared pseudo-session")
		}
	}
	_, err = queries.UpdateCredentialSubjectRevoked(ctx, subjectID)
	if err != nil {
		return err
	}
	_, _, err = admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if !errors.Is(err, auth.ErrPersonalCredentialRequired) {
		return errors.New("revoked subject remained eligible")
	}
	_, _, err = admissions.Admit(ctx, installation.ID, shared.ID, "conversation", decide)
	if err != nil {
		return fmt.Errorf("subject revocation affected shared credential: %w", err)
	}
	_, survivingSubject, err := admissions.Admit(ctx, installation.ID, secondPersonal.ID, "conversation", decide)
	if err != nil || survivingSubject.ProfileKey != secondProfileKey {
		return fmt.Errorf("subject revocation affected another subject: %w", err)
	}
	return nil
}

func checkRetainedAdmissionClock(ctx context.Context, admissions *serving.ServingAdmissionRepo, installationID, keyID string) error {
	startedAt := time.Now().UTC().Truncate(time.Second)
	var previousBinding *policyregistry.SessionReleaseBinding
	for _, elapsed := range []time.Duration{0, 12 * time.Hour, 25 * time.Hour, 36 * time.Hour} {
		now := startedAt.Add(elapsed)
		_, admitted, err := admissions.Admit(ctx, installationID, keyID, "retained-clock", func(_ context.Context, admission policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
			if previousBinding != nil {
				if admission.Previous == nil || !admission.Previous.LastAdmittedAt.Equal(previousBinding.LastAdmittedAt) {
					return policyregistry.SessionReleaseBinding{}, errors.New("retained admission did not persist its last-admitted timestamp")
				}
				if !now.Before(admission.Previous.LastAdmittedAt.Add(policyregistry.ServingIdleLifetime)) {
					return policyregistry.SessionReleaseBinding{}, errors.New("active retained session was incorrectly considered idle")
				}
			}
			return fixtureBinding(admission, now), nil
		})
		if err != nil {
			return err
		}
		if admitted.BindingGeneration != 1 || !admitted.CreatedAt.Equal(startedAt) || !admitted.LastAdmittedAt.Equal(now) {
			return errors.New("retained admission changed its generation, creation time or admission clock")
		}
		previousBinding = &admitted
	}
	return nil
}

func checkAttribution(ctx context.Context, pool *pgxpool.Pool, assertion policyregistry.ServingAssertion) error {
	attributions := serving.NewRequestAttributionRepo(pool)
	requestID := uuid.NewString()
	if err := attributions.RecordServingRequest(ctx, requestID, assertion); err != nil {
		return fmt.Errorf("record admitted request: %w", err)
	}
	rebound := assertion
	rebound.Admission.BindingGeneration++
	rebound.Admission.Target = policyregistry.TargetInternal
	err := attributions.RecordServingRequest(ctx, requestID, rebound)
	var constraintError *pgconn.PgError
	if !errors.As(err, &constraintError) || constraintError.Code != "23505" {
		return fmt.Errorf("duplicate request attribution was not rejected: %v", err)
	}
	queries := sqlc.New(pool)
	stored, err := queries.GetServingRequestAttribution(ctx, sqlc.GetServingRequestAttributionParams{RequestID: requestID, InstallationID: uuid.MustParse(assertion.Scope.InstallationID)})
	if err != nil {
		return err
	}
	var persistedBinding policyregistry.SessionReleaseBinding
	if err := json.Unmarshal(stored.Binding, &persistedBinding); err != nil {
		return err
	}
	expected, err := json.Marshal(assertion.Admission)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(persistedBinding)
	if err != nil {
		return err
	}
	if !bytes.Equal(expected, actual) || stored.APIKeyID.String() != assertion.APIKeyID {
		return errors.New("request attribution changed after a later conversation rebind")
	}
	_, err = queries.GetServingRequestAttribution(ctx, sqlc.GetServingRequestAttributionParams{RequestID: requestID, InstallationID: uuid.New()})
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("cross-installation request attribution lookup did not fail closed: %v", err)
	}
	return nil
}

func newKey(installationID string) auth.CreateAPIKeyParams {
	token := auth.GenerateID(auth.APIKeyPrefix)
	hash, prefix, suffix := auth.APITokenFingerprint(token)
	return auth.CreateAPIKeyParams{InstallationID: installationID, ExternalID: auth.GenerateID("kid"), KeyHash: hash, KeyPrefix: prefix, KeySuffix: suffix, Scope: auth.ScopeRouting}
}

func fixtureBinding(admission policyregistry.SerializedAdmission, now time.Time) policyregistry.SessionReleaseBinding {
	projection := admission.Projection
	if admission.Previous != nil && admission.Previous.Target == projection.Target && admission.Previous.ProfileKey == projection.ProfileKey && admission.Previous.EnrollmentGeneration == projection.EnrollmentGeneration && admission.Previous.AssignmentGeneration == projection.AssignmentGeneration && admission.Previous.SubjectAssignmentGeneration == projection.SubjectAssignmentGeneration && admission.Previous.AssignmentSource == projection.AssignmentSource {
		retained := *admission.Previous
		retained.LastAdmittedAt = now
		return retained
	}
	digest := strings.Repeat("a", 64)
	reference := policyregistry.ObjectRef{URI: "gs://fixture/objects/" + digest, SHA256: digest, Generation: 1}
	selection := policyregistry.ServingSelection{Release: reference, Binding: reference}
	if projection.ProfileKey != "" {
		selection.Profile = &reference
	}
	binding := policyregistry.SessionReleaseBinding{Target: projection.Target, ActivationID: uuid.NewString(), Selection: selection, ProfileKey: projection.ProfileKey, EnrollmentGeneration: projection.EnrollmentGeneration, AssignmentGeneration: projection.AssignmentGeneration, SubjectAssignmentGeneration: projection.SubjectAssignmentGeneration, AssignmentSource: projection.AssignmentSource, BindingGeneration: 1, CreatedAt: now, LastAdmittedAt: now}
	if admission.Previous != nil {
		binding.BindingGeneration = admission.Previous.BindingGeneration + 1
		binding.CreatedAt = admission.Previous.CreatedAt
	}
	return binding
}
