// Command reporting_provenance_check verifies migration-first reporting compatibility
// against an explicitly configured loopback database. Fixture writes roll back.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/proxy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("PASS: unmarked configuration, marked snapshot, old-column insert and versioned telemetry roundtrip; fixtures rolled back")
}

func run() error {
	raw := os.Getenv("ROUTER_TEST_DATABASE_URL")
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		return errors.New("ROUTER_TEST_DATABASE_URL must name a disposable loopback PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	installation, user, experiment := uuid.New(), uuid.New(), uuid.New()
	snapshotExperimentID := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name) VALUES($1,'synthetic-reporting','synthetic-reporting')`, installation); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO router.model_router_users(id,installation_id,email) VALUES($1,$2,'fixture@example.invalid')`, user, installation); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO router.blind_router_experiment_configurations(installation_id,organization_id,enabled,router_on_percentage,seed) VALUES($1,'synthetic',true,50,$2)`, installation, uuid.New()); err != nil {
		return err
	}
	repo := postgres.NewBlindExperimentRepo(tx)
	unmarked, err := repo.GetForUser(ctx, installation.String(), user.String())
	if err != nil {
		return err
	}
	if !unmarked.Enabled || unmarked.ExperimentSnapshotID != 0 {
		return errors.New("old configuration must remain enabled without reporting metadata")
	}
	if _, err = tx.Exec(ctx, `UPDATE router.blind_router_experiment_configurations SET reporting_experiment_id=$2,reporting_revision=3 WHERE installation_id=$1`, installation, experiment); err != nil {
		return err
	}
	incomplete, err := repo.GetForUser(ctx, installation.String(), user.String())
	if err != nil {
		return err
	}
	if incomplete.ExperimentSnapshotID != 0 {
		return errors.New("missing watermark exposed an incomplete reporting marker")
	}
	var percentageSnapshot int64
	if err = tx.QueryRow(ctx, `INSERT INTO router.experiment_settings_snapshots(installation_id,experiment_id,revision,mode,settings)
      SELECT installation_id,$3,1,'percentage',jsonb_build_object('source_experiment_id',$2::text,'algorithm_version',1,'seed',seed::text,'router_on_percentage',router_on_percentage)
      FROM router.blind_router_experiment_configurations WHERE installation_id=$1 RETURNING id`, installation, experiment, snapshotExperimentID).Scan(&percentageSnapshot); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE router.blind_router_experiment_configurations SET reporting_experiment_id=$2,reporting_revision=3,reporting_updated_at=updated_at,experiment_snapshot_id=$3 WHERE installation_id=$1`, installation, experiment, percentageSnapshot); err != nil {
		return err
	}
	marked, err := repo.GetForUser(ctx, installation.String(), user.String())
	if err != nil {
		return err
	}
	if marked.ExperimentSnapshotID != percentageSnapshot || marked.CohortExperimentID != "" || marked.RouterOnPercentage != unmarked.RouterOnPercentage || marked.Seed != unmarked.Seed {
		return errors.New("marked configuration changed serving snapshot or lost reporting revision")
	}
	var teamSnapshot int64
	if err = tx.QueryRow(ctx, `INSERT INTO router.experiment_settings_snapshots(installation_id,experiment_id,revision,mode,settings)
      VALUES($1,$4,2,'teams',jsonb_build_object('source_experiment_id',$2::text,'algorithm_version',1,'router_user_ids',jsonb_build_array($3::text))) RETURNING id`, installation, experiment, user.String(), snapshotExperimentID).Scan(&teamSnapshot); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO router.installation_routing_policies(installation_id,mode,revision,reporting_experiment_id,reporting_updated_at,experiment_snapshot_id) VALUES($1,'assigned',7,$2,now(),$3)`, installation, experiment, teamSnapshot); err != nil {
		return err
	}
	policy, err := postgres.NewRoutingPolicyRepo(tx).GetPolicy(ctx, installation.String())
	if err != nil {
		return err
	}
	if policy.Mode != auth.RoutingPolicyAssigned || policy.Revision != 7 || policy.ExperimentSnapshotID != teamSnapshot {
		return errors.New("team policy marker and serving revision did not roundtrip together")
	}
	// Reporting metadata failure must never alter the serving policy.
	var malformedSnapshot int64
	if err = tx.QueryRow(ctx, `INSERT INTO router.experiment_settings_snapshots(installation_id,experiment_id,revision,mode,settings)
      VALUES($1,$2,3,'teams',jsonb_build_object('source_experiment_id',$3::text,'algorithm_version',1,'router_user_ids',jsonb_build_array(42))) RETURNING id`, installation, snapshotExperimentID, experiment).Scan(&malformedSnapshot); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE router.installation_routing_policies SET experiment_snapshot_id=$2 WHERE installation_id=$1`, installation, malformedSnapshot); err != nil {
		return err
	}
	malformedPolicy, err := postgres.NewRoutingPolicyRepo(tx).GetPolicy(ctx, installation.String())
	if err != nil {
		return err
	}
	if malformedPolicy.Mode != policy.Mode || malformedPolicy.Revision != policy.Revision || malformedPolicy.ExperimentSnapshotID != 0 {
		return errors.New("malformed reporting membership changed routing or remained eligible")
	}
	if _, err = tx.Exec(ctx, `UPDATE router.installation_routing_policies SET experiment_snapshot_id=$2 WHERE installation_id=$1`, installation, teamSnapshot); err != nil {
		return err
	}
	// A legacy control plane advances updated_at without refreshing reporting metadata.
	if _, err = tx.Exec(ctx, `UPDATE router.blind_router_experiment_configurations SET router_on_percentage=75,updated_at=updated_at+interval '1 second' WHERE installation_id=$1`, installation); err != nil {
		return err
	}
	stale, err := repo.GetForUser(ctx, installation.String(), user.String())
	if err != nil {
		return err
	}
	if stale.ExperimentSnapshotID != 0 || stale.RouterOnPercentage != 75 {
		return errors.New("legacy percentage writer left stale reporting attribution or changed serving behavior")
	}
	if _, err = tx.Exec(ctx, `UPDATE router.installation_routing_policies SET revision=8,updated_at=updated_at+interval '1 second' WHERE installation_id=$1`, installation); err != nil {
		return err
	}
	stalePolicy, err := postgres.NewRoutingPolicyRepo(tx).GetPolicy(ctx, installation.String())
	if err != nil {
		return err
	}
	if stalePolicy.ExperimentSnapshotID != 0 || stalePolicy.Mode != auth.RoutingPolicyAssigned || stalePolicy.Revision != 8 {
		return errors.New("legacy team writer left stale reporting attribution or changed serving behavior")
	}
	// A pre-migration writer omits every reporting column.
	if _, err = tx.Exec(ctx, `INSERT INTO router.model_router_request_telemetry(installation_id,request_id,span_type,trace_id,timestamp) VALUES($1,'old-writer','router.upstream','old-trace',now())`, installation); err != nil {
		return err
	}
	var missing bool
	if err = tx.QueryRow(ctx, `SELECT experiment_snapshot_id IS NULL AND reporting_schema_version IS NULL AND reporting_mode IS NULL AND reporting_experiment_id IS NULL AND reporting_revision IS NULL AND reporting_assigned_arm IS NULL AND reporting_treatment_applied IS NULL AND reporting_bypass_reason IS NULL AND reporting_subject_key IS NULL FROM router.model_router_request_telemetry WHERE installation_id=$1 AND request_id='old-writer'`, installation).Scan(&missing); err != nil {
		return err
	}
	if !missing {
		return errors.New("old writer fabricated reporting provenance")
	}
	if err = postgres.NewTelemetryRepo(tx).InsertRequestTelemetry(ctx, proxy.InsertTelemetryParams{InstallationID: installation.String(), RequestID: "marked-writer", SpanType: "router.upstream", TraceID: "marked-trace", Timestamp: time.Now(), ExperimentSnapshotID: &teamSnapshot}); err != nil {
		return err
	}
	var persistedSnapshot int64
	var oldFieldsEmpty bool
	if err = tx.QueryRow(ctx, `SELECT experiment_snapshot_id,reporting_schema_version IS NULL AND reporting_mode IS NULL AND reporting_experiment_id IS NULL AND reporting_revision IS NULL AND reporting_assigned_arm IS NULL AND reporting_treatment_applied IS NULL AND reporting_bypass_reason IS NULL AND reporting_subject_key IS NULL FROM router.model_router_request_telemetry WHERE installation_id=$1 AND request_id='marked-writer'`, installation).Scan(&persistedSnapshot, &oldFieldsEmpty); err != nil {
		return err
	}
	if persistedSnapshot != teamSnapshot || !oldFieldsEmpty {
		return errors.New("new writer must store only its snapshot reference")
	}
	return tx.Rollback(ctx)
}
