package postgres

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/switchpolicy"
	"weave-os/router/internal/sqlc"
)

func advanceShadowDatabaseFixture(ctx context.Context, store switchpolicy.ShadowStore, key switchpolicy.ShadowKey, event switchpolicy.ShadowObservation) (switchpolicy.ShadowOutcome, error) {
	for attempt := 0; attempt < 8; attempt++ {
		prior, found, err := store.GetAgreementShadow(ctx, key)
		if err != nil {
			return switchpolicy.ShadowOutcome{}, err
		}
		next, outcome, err := switchpolicy.EvaluateShadow(prior, found, event)
		if err != nil || outcome.Duplicate {
			return outcome, err
		}
		ok, err := store.CompareAndSwapAgreementShadow(ctx, key, prior.Revision, next)
		if err != nil {
			return switchpolicy.ShadowOutcome{}, err
		}
		if ok {
			return outcome, nil
		}
	}
	return switchpolicy.ShadowOutcome{}, errors.New("fixture CAS retry budget exhausted")
}

func TestAgreementShadowPostgresAtomicity(t *testing.T) {
	databaseURL := os.Getenv("ROUTER_AGREEMENT_SHADOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires a disposable agreement_shadow_test database")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") || parsed.Path != "/agreement_shadow_test" {
		t.Fatal("integration test requires the explicitly named disposable loopback database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewTelemetryRepo(pool)
	key := switchpolicy.ShadowKey{InstallationID: uuid.NewString(), SessionKey: []byte("atomic-shadow-fixture"), Role: sessionpin.DefaultRole}
	defer func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM router.agreement_shadow_states WHERE installation_id=$1", key.InstallationID)
	}()
	luna := switchpolicy.Selection{Target: switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}, Group: escalation.Low}
	sol := switchpolicy.Selection{Target: switchpolicy.Target{Provider: providers.ProviderOpenAI, Model: "gpt-6.1-sol"}, Group: escalation.High}
	event := switchpolicy.ShadowObservation{RequestID: "postgres-duplicate", StartedAt: time.Date(2026, 1, 1, 0, 0, 1, 123, time.UTC), PolicyIdentity: "fixture-classifier/fixture-policy", Proposal: sol, Confidence: .9, LivePin: luna, Eligible: []switchpolicy.Target{luna.Target, sol.Target}}
	var wg sync.WaitGroup
	outcomes := make(chan switchpolicy.ShadowOutcome, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := advanceShadowDatabaseFixture(ctx, NewTelemetryRepo(pool), key, event)
			outcomes <- out
			errs <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	duplicates := 0
	for out := range outcomes {
		if out.Duplicate {
			duplicates++
		}
	}
	state, found, err := store.GetAgreementShadow(ctx, key)
	if err != nil || !found || state.Revision != 1 || duplicates != 1 || state.TwoUp.Votes != 1 || !state.LastStartedAt.Equal(event.StartedAt) {
		t.Fatalf("atomic create/duplicate or timestamp round trip failed: %+v duplicates=%d error=%v", state, duplicates, err)
	}
	otherInstallation := key
	otherInstallation.InstallationID = uuid.NewString()
	if _, found, err := store.GetAgreementShadow(ctx, otherInstallation); err != nil || found {
		t.Fatalf("installation namespace leaked: found=%v error=%v", found, err)
	}
	next := state
	next.Revision++
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.CompareAndSwapAgreementShadow(ctx, key, state.Revision, next)
			results <- ok
			if err != nil {
				t.Errorf("CAS: %v", err)
			}
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("expected one writer to win the same revision, got %d", wins)
	}
	other := key
	other.Role = "independent-role"
	if _, err := advanceShadowDatabaseFixture(ctx, store, other, event); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE router.agreement_shadow_states SET updated_at=now()-interval '8 days' WHERE installation_id=$1 AND role=$2", key.InstallationID, key.Role); err != nil {
		t.Fatal(err)
	}
	if err := sqlc.New(pool).SweepAgreementShadowStates(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetAgreementShadow(ctx, key); err != nil || found {
		t.Fatalf("expired shadow survived cleanup: found=%v err=%v", found, err)
	}
	if otherState, found, err := store.GetAgreementShadow(ctx, other); err != nil || !found || otherState.TwoUp.Votes != 1 {
		t.Fatalf("cleanup or shared-role state crossed namespaces: %+v %v", otherState, err)
	}
}
