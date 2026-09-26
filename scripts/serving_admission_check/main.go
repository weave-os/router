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
	"weave-os/router/internal/observability"
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
	if err := control.ProjectProfile(ctx, externalID, []string{installation.ID}, profileKey); err != nil {
		return err
	}
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
	if sharedBinding.Target != policyregistry.TargetStable || sharedBinding.ProfileKey != profileKey {
		return errors.New("shared key gained internal access or lost its assigned profile")
	}
	if err := checkRetainedAdmissionClock(ctx, admissions, installation.ID, shared.ID); err != nil {
		return err
	}
	if err := checkAdmissionTimingRecord(ctx, admissions, installation.ID, shared.ID, decide); err != nil {
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
	if rotatedBinding.BindingGeneration != 1 || replacement.CredentialSubjectID != personal.CredentialSubjectID {
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
	if disabled.Target != policyregistry.TargetStable || disabled.ProfileKey != profileKey || disabled.BindingGeneration != 2 {
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
	if err := control.ProjectProfile(ctx, "wrong-owner", []string{installation.ID}, uuid.NewString()); !errors.Is(err, auth.ErrInstallationNotFound) {
		return errors.New("wrong organization projected a profile")
	}
	_, unchanged, err := admissions.Admit(ctx, installation.ID, replacement.ID, "conversation", decide)
	if err != nil {
		return err
	}
	if unchanged.ProfileKey != profileKey || unchanged.AssignmentGeneration != disabled.AssignmentGeneration {
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

// admissionTimingAttributes are the fields the timing record must carry, and
// forbiddenTimingAttributes the identity-bearing fields it must never carry.
var (
	admissionTimingAttributes = []string{"admission_total_ms", "admission_tx_ms", "admission_lock_wait_ms", "admission_gcs_state_read_ms", "admission_gcs_selection_set_read_ms", "admission_gcs_selection_set_reads", "admission_decide_ms", "admission_projection_queries_ms", "admission_persistent", "admission_outcome", "target", "activation_id"}
	forbiddenTimingAttributes = []string{"installation_id", "api_key_id", "key_id", "credential_identity", "subject_id", "conversation_digest", "client_session_id", "session_id", "binding", "err"}
)

// checkAdmissionTimingRecord asserts that both an admitted and a denied admission emit exactly
// one measurement record carrying durations and no caller identity.
func checkAdmissionTimingRecord(ctx context.Context, admissions *serving.ServingAdmissionRepo, installationID, keyID string, decide policyregistry.AdmissionDecision) error {
	var logs bytes.Buffer
	observed := observability.WithLogger(ctx, slog.New(slog.NewJSONHandler(&logs, nil)))
	if _, _, err := admissions.Admit(observed, installationID, keyID, "timing-record", decide); err != nil {
		return err
	}
	admittedRecord, err := soleTimingRecord(&logs)
	if err != nil {
		return err
	}
	if admittedRecord["admission_outcome"] != "admitted" || admittedRecord["admission_persistent"] != true {
		return fmt.Errorf("admitted timing record misreports its outcome: %v", admittedRecord)
	}
	if total, ok := admittedRecord["admission_total_ms"].(float64); !ok || total <= 0 {
		return errors.New("admitted timing record has no total duration")
	}
	logs.Reset()
	_, _, err = admissions.Admit(observed, installationID, uuid.NewString(), "timing-record", decide)
	if !errors.Is(err, auth.ErrInvalidToken) {
		return fmt.Errorf("unknown credential was not denied: %v", err)
	}
	deniedRecord, err := soleTimingRecord(&logs)
	if err != nil {
		return err
	}
	if deniedRecord["admission_outcome"] != "denied" {
		return fmt.Errorf("denied admission was not measured as denied: %v", deniedRecord)
	}
	logs.Reset()
	_, _, err = admissions.Admit(observed, installationID, keyID, "timing-record", func(_ context.Context, admission policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
		if admission.Previous == nil {
			return policyregistry.SessionReleaseBinding{}, errors.New("timing record check lost its previous binding")
		}
		stale := *admission.Previous
		stale.BindingGeneration = 0
		return stale, nil
	})
	if !errors.Is(err, policyregistry.ErrStaleServingGeneration) {
		return fmt.Errorf("stale generation was admitted: %v", err)
	}
	staleRecord, err := soleTimingRecord(&logs)
	if err != nil {
		return err
	}
	if staleRecord["admission_outcome"] != "denied" || staleRecord["activation_id"] != "" {
		return fmt.Errorf("unadmitted attempt reported an activation: %v", staleRecord)
	}
	return nil
}

func soleTimingRecord(logs *bytes.Buffer) (map[string]any, error) {
	var timing []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		if record["msg"] == "Serving admission timing" {
			timing = append(timing, record)
		}
	}
	if len(timing) != 1 {
		return nil, fmt.Errorf("admission emitted %d timing records", len(timing))
	}
	record := timing[0]
	for _, attribute := range admissionTimingAttributes {
		if _, ok := record[attribute]; !ok {
			return nil, fmt.Errorf("timing record is missing %s", attribute)
		}
	}
	for _, attribute := range forbiddenTimingAttributes {
		if _, ok := record[attribute]; ok {
			return nil, fmt.Errorf("timing record leaked %s", attribute)
		}
	}
	return record, nil
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
	if admission.Previous != nil && admission.Previous.Target == projection.Target && admission.Previous.EnrollmentGeneration == projection.EnrollmentGeneration && admission.Previous.AssignmentGeneration == projection.AssignmentGeneration {
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
	binding := policyregistry.SessionReleaseBinding{Target: projection.Target, ActivationID: uuid.NewString(), Selection: selection, ProfileKey: projection.ProfileKey, EnrollmentGeneration: projection.EnrollmentGeneration, AssignmentGeneration: projection.AssignmentGeneration, BindingGeneration: 1, CreatedAt: now, LastAdmittedAt: now}
	if admission.Previous != nil {
		binding.BindingGeneration = admission.Previous.BindingGeneration + 1
		binding.CreatedAt = admission.Previous.CreatedAt
	}
	return binding
}
