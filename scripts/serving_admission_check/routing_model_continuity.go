package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router"
)

func checkRoutingModelSessionContinuity(ctx context.Context, pool *pgxpool.Pool, installationID string) error {
	admissions, err := scopedAdmissionRepo(pool, policyregistry.TargetStable)
	if err != nil {
		return err
	}
	key, err := postgres.NewRepository(pool, auth.NoOpEncryptor{}).APIKeys.Create(ctx, newKey(installationID))
	if err != nil {
		return err
	}
	keyID := key.ID
	decide := func(ctx context.Context, admission policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
		now, err := admission.Clock(ctx)
		if err != nil {
			return policyregistry.SessionReleaseBinding{}, err
		}
		return fixtureBinding(admission, now), nil
	}
	efficientProfile := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO router.installation_routing_model_profile_assignments (installation_id, routing_model, profile_key) VALUES ($1, $2, $3)`, installationID, string(router.ModelAutoEfficient), efficientProfile); err != nil {
		return fmt.Errorf("assign efficient profile: %w", err)
	}
	const session = "efficient-continuity"
	freshCache := func() (*policyregistry.AdmissionDecisionCache, error) {
		return policyregistry.NewAdmissionDecisionCache(admissions, 10, time.Minute, time.Now)
	}
	type turn struct {
		name  string
		admit func(*policyregistry.AdmissionDecisionCache) (policyregistry.SessionReleaseBinding, error)
	}
	efficientTurn := func(cache *policyregistry.AdmissionDecisionCache) (policyregistry.SessionReleaseBinding, error) {
		_, binding, err := cache.AdmitRoutingModel(ctx, installationID, keyID, session, router.ModelAutoEfficient, decide)
		return binding, err
	}
	countTokens := func(cache *policyregistry.AdmissionDecisionCache) (policyregistry.SessionReleaseBinding, error) {
		_, binding, err := cache.Admit(ctx, installationID, keyID, session, decide)
		return binding, err
	}
	for _, step := range []turn{{"efficient turn", efficientTurn}, {"count_tokens", countTokens}, {"efficient turn", efficientTurn}} {
		cache, err := freshCache()
		if err != nil {
			return err
		}
		binding, err := step.admit(cache)
		if err != nil {
			return fmt.Errorf("%s in an efficient session: %w", step.name, err)
		}
		if binding.ProfileKey != efficientProfile || binding.RoutingModel != router.ModelAutoEfficient || binding.BindingGeneration != 1 {
			return fmt.Errorf("%s rebound the efficient session: profile_changed=%t routing_model=%q generation=%d", step.name, binding.ProfileKey != efficientProfile, binding.RoutingModel, binding.BindingGeneration)
		}
		var storedGeneration int64
		var storedProfile uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT binding_generation, profile_key FROM router.session_release_bindings WHERE installation_id = $1 AND credential_scope = $2`, installationID, keyID).Scan(&storedGeneration, &storedProfile); err != nil {
			return fmt.Errorf("read efficient session binding after %s: %w", step.name, err)
		}
		if storedGeneration != 1 || storedProfile.String() != efficientProfile {
			return fmt.Errorf("%s persisted a rebind of the efficient session: generation=%d", step.name, storedGeneration)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO router.installation_lane_enrollments (installation_id, internal_enrolled, enrollment_generation) VALUES ($1, true, 1)`, installationID); err != nil {
		return fmt.Errorf("enroll the internal lane: %w", err)
	}
	cache, err := freshCache()
	if err != nil {
		return err
	}
	rebound, err := countTokens(cache)
	if err != nil {
		return fmt.Errorf("count_tokens after a serving-target change: %w", err)
	}
	if rebound.Target != policyregistry.TargetInternal || rebound.RoutingModel != "" {
		return fmt.Errorf("a serving-target change kept the efficient routing model: target=%q routing_model=%q", rebound.Target, rebound.RoutingModel)
	}
	var storedRoutingModel string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(binding->>'routing_model', '') FROM router.session_release_bindings WHERE installation_id = $1 AND credential_scope = $2`, installationID, keyID).Scan(&storedRoutingModel); err != nil {
		return fmt.Errorf("read the rebound session binding: %w", err)
	}
	if storedRoutingModel != "" {
		return fmt.Errorf("a serving-target change persisted routing_model=%q", storedRoutingModel)
	}
	return nil
}
