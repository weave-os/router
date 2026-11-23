package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/gcsregistry"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/postgres/serving"
	"weave-os/router/internal/sqlc"
)

type fixtureCredential struct {
	raw            string
	keyHash        string
	apiKeyID       string
	subjectID      string
	installationID string
	installation   uuid.UUID
}

func provisionManagedFixture(ctx context.Context, databaseURL, installationID, externalID string) (fixtureBinding, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fixtureBinding{}, errors.New("managed fixture could not connect to disposable database")
	}
	defer pool.Close()
	credential, err := createFixtureCredential(ctx, pool, installationID, externalID)
	if err != nil {
		return fixtureBinding{}, err
	}
	target := policyregistry.ServingTarget(os.Getenv("ROUTER_SERVING_TARGET"))
	if target == "" {
		target = policyregistry.TargetInternal
	}
	if target != policyregistry.TargetInternal && target != policyregistry.TargetStable {
		return fixtureBinding{}, errors.New("unsupported fixture serving target")
	}
	queries := dbbudget.Queries(pool)
	admissions, err := serving.NewServingAdmissionRepo(pool, policyregistry.EnvironmentProd)
	if err != nil {
		return fixtureBinding{}, err
	}
	if err := completeFixtureCredential(ctx, queries, admissions, credential, target); err != nil {
		return fixtureBinding{}, err
	}
	if err := fundFixtureOrganization(ctx, pool, externalID); err != nil {
		return fixtureBinding{}, err
	}
	if err := verifyFixtureBearer(ctx, serving.CredentialLookup{Queries: queries}, credential); err != nil {
		return fixtureBinding{}, err
	}
	binding := fixtureBinding{RouterKey: credential.raw, SubjectID: credential.subjectID, APIKeyID: credential.apiKeyID}
	if os.Getenv("STORAGE_EMULATOR_HOST") == "" || os.Getenv("ROUTER_SERVING_REGISTRY_URI") == "" {
		return binding, nil
	}
	if err := verifyManagedAdmission(ctx, queries, admissions, credential, target); err != nil {
		return fixtureBinding{}, err
	}
	binding.ManagedAdmissionReady = true
	return binding, nil
}

func createFixtureCredential(ctx context.Context, pool *pgxpool.Pool, installationID, externalID string) (fixtureCredential, error) {
	installation, err := uuid.Parse(installationID)
	if err != nil {
		return fixtureCredential{}, errors.New("seed installation identity is invalid")
	}
	raw := auth.GenerateID(auth.APIKeyPrefix)
	keyHash, keyPrefix, keySuffix := auth.APITokenFingerprint(raw)
	created, err := postgres.NewCredentialSubjectRepo(pool).CreatePending(ctx, externalID, auth.CreateAPIKeyParams{InstallationID: installation.String(), ExternalID: externalID, KeyPrefix: keyPrefix, KeyHash: keyHash, KeySuffix: keySuffix, Scope: auth.ScopeRouting})
	if err != nil {
		return fixtureCredential{}, errors.New("canonical personal credential setup failed")
	}
	if created == nil || created.CredentialSubjectID == "" {
		return fixtureCredential{}, errors.New("canonical credential setup returned no subject")
	}
	return fixtureCredential{raw: raw, keyHash: keyHash, apiKeyID: created.ID, subjectID: created.CredentialSubjectID, installationID: installationID, installation: installation}, nil
}

func rejectIncompleteProjection(context.Context, policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
	return policyregistry.SessionReleaseBinding{}, errors.New("incomplete projection reached decision")
}

func completeFixtureCredential(ctx context.Context, queries *sqlc.Queries, admissions *serving.ServingAdmissionRepo, credential fixtureCredential, target policyregistry.ServingTarget) error {
	if _, _, err := admissions.Admit(ctx, credential.installationID, credential.apiKeyID, "", rejectIncompleteProjection); !errors.Is(err, auth.ErrPersonalCredentialRequired) {
		return errors.New("incomplete personal projection did not fail closed")
	}
	subject := uuid.MustParse(credential.subjectID)
	if changed, err := queries.UpdateCredentialSubjectProjection(ctx, subject); err != nil || changed != 1 {
		return errors.New("canonical credential projection did not complete")
	}
	if _, err := queries.UpdateCredentialSubjectEnrollment(ctx, sqlc.UpdateCredentialSubjectEnrollmentParams{Enrolled: target == policyregistry.TargetInternal, SubjectID: subject}); err != nil {
		return errors.New("canonical credential enrollment did not complete")
	}
	if changed, err := setFixtureInstallationAccess(ctx, queries, credential, true); err != nil || changed != 1 {
		return errors.New("canonical installation access did not complete")
	}
	return nil
}

func setFixtureInstallationAccess(ctx context.Context, queries *sqlc.Queries, credential fixtureCredential, enabled bool) (int64, error) {
	return queries.UpdateCredentialSubjectInstallationAccess(ctx, sqlc.UpdateCredentialSubjectInstallationAccessParams{AccessEnabled: enabled, SubjectID: uuid.MustParse(credential.subjectID), InstallationID: credential.installation})
}

func fundFixtureOrganization(ctx context.Context, pool *pgxpool.Pool, externalID string) error {
	if _, err := pool.Exec(ctx, `INSERT INTO router.organization_credit_balance (organization_id, balance_usd_micros) VALUES ($1, 20000000) ON CONFLICT (organization_id) DO UPDATE SET balance_usd_micros=GREATEST(router.organization_credit_balance.balance_usd_micros, EXCLUDED.balance_usd_micros)`, externalID); err != nil {
		return errors.New("disposable prepaid balance setup failed")
	}
	if balance, err := postgres.NewBillingRepo(pool).GetBalance(ctx, externalID); err != nil || balance < 20000000 {
		return errors.New("canonical prepaid balance verification failed")
	}
	return nil
}

func verifyFixtureBearer(ctx context.Context, lookup serving.CredentialLookup, credential fixtureCredential) error {
	verifiedKey, verifiedInstallation, err := lookup.GetActiveByHashWithInstallation(ctx, credential.keyHash)
	if err != nil || verifiedKey.ID != credential.apiKeyID || verifiedInstallation.ID != credential.installationID {
		return errors.New("canonical bearer lookup verification failed")
	}
	verifiedSubject, err := lookup.GetCredentialSubject(ctx, credential.subjectID, credential.installationID)
	if err != nil {
		return err
	}
	if err := auth.ValidateServingCredential(*verifiedKey, verifiedSubject); err != nil {
		return err
	}
	if _, _, err := lookup.GetActiveByHashWithInstallation(ctx, strings.Repeat("0", 64)); err == nil {
		return errors.New("invalid bearer hash accepted")
	}
	return nil
}

func verifyManagedAdmission(ctx context.Context, queries *sqlc.Queries, admissions *serving.ServingAdmissionRepo, credential fixtureCredential, target policyregistry.ServingTarget) error {
	registry, err := gcsregistry.NewGCSRegistry(ctx, os.Getenv("ROUTER_SERVING_REGISTRY_URI"))
	if err != nil {
		return errors.New("local serving registry initialization failed")
	}
	defer func() { _ = registry.Close() }()
	admission := policyregistry.ServingAdmission{Store: registry}
	sessionID := uuid.NewString()
	_, admitted, err := admissions.Admit(ctx, credential.installationID, credential.apiKeyID, sessionID, admission.Decide)
	if err != nil {
		return fmt.Errorf("canonical managed admission verification failed: %w", err)
	}
	if admitted.Target != target {
		return errors.New("canonical admission selected unexpected target")
	}
	if _, _, err := admissions.Admit(ctx, uuid.NewString(), credential.apiKeyID, "", admission.Decide); err == nil {
		return errors.New("invalid installation admitted")
	}
	if changed, err := setFixtureInstallationAccess(ctx, queries, credential, false); err != nil || changed != 1 {
		return errors.New("disabled-access fixture setup failed")
	}
	if _, _, err := admissions.Admit(ctx, credential.installationID, credential.apiKeyID, "", admission.Decide); !errors.Is(err, auth.ErrPersonalCredentialRequired) {
		return errors.New("disabled access did not fail closed")
	}
	if _, err := setFixtureInstallationAccess(ctx, queries, credential, true); err != nil {
		return err
	}
	_, _, err = admissions.Admit(ctx, credential.installationID, credential.apiKeyID, sessionID, admission.Decide)
	return err
}
