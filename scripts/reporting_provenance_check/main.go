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
	if !unmarked.Enabled || unmarked.ReportingExperimentID != "" || unmarked.ReportingRevision != 0 {
		return errors.New("old configuration must remain enabled without reporting metadata")
	}
	if _, err = tx.Exec(ctx, `UPDATE router.blind_router_experiment_configurations SET reporting_experiment_id=$2,reporting_revision=3 WHERE installation_id=$1`, installation, experiment); err != nil {
		return err
	}
	incomplete, err := repo.GetForUser(ctx, installation.String(), user.String())
	if err != nil {
		return err
	}
	if incomplete.ReportingExperimentID != "" || incomplete.ReportingRevision != 0 {
		return errors.New("missing watermark exposed an incomplete reporting marker")
	}
	if _, err = tx.Exec(ctx, `UPDATE router.blind_router_experiment_configurations SET reporting_experiment_id=$2,reporting_revision=3,reporting_updated_at=updated_at WHERE installation_id=$1`, installation, experiment); err != nil {
		return err
	}
	marked, err := repo.GetForUser(ctx, installation.String(), user.String())
	if err != nil {
		return err
	}
	if marked.ReportingExperimentID != experiment.String() || marked.ReportingRevision != 3 || marked.CohortExperimentID != "" || marked.RouterOnPercentage != unmarked.RouterOnPercentage || marked.Seed != unmarked.Seed {
		return errors.New("marked configuration changed serving snapshot or lost reporting revision")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO router.installation_routing_policies(installation_id,mode,revision,reporting_experiment_id,reporting_updated_at) VALUES($1,'assigned',7,$2,now())`, installation, experiment); err != nil {
		return err
	}
	policy, err := postgres.NewRoutingPolicyRepo(tx).GetPolicy(ctx, installation.String())
	if err != nil {
		return err
	}
	if policy.Mode != auth.RoutingPolicyAssigned || policy.Revision != 7 || policy.ReportingExperimentID != experiment.String() {
		return errors.New("team policy marker and serving revision did not roundtrip together")
	}
	// A legacy control plane advances updated_at without refreshing reporting metadata.
	if _, err = tx.Exec(ctx, `UPDATE router.blind_router_experiment_configurations SET router_on_percentage=75,updated_at=updated_at+interval '1 second' WHERE installation_id=$1`, installation); err != nil {
		return err
	}
	stale, err := repo.GetForUser(ctx, installation.String(), user.String())
	if err != nil {
		return err
	}
	if stale.ReportingExperimentID != "" || stale.ReportingRevision != 0 || stale.RouterOnPercentage != 75 {
		return errors.New("legacy percentage writer left stale reporting attribution or changed serving behavior")
	}
	if _, err = tx.Exec(ctx, `UPDATE router.installation_routing_policies SET revision=8,updated_at=updated_at+interval '1 second' WHERE installation_id=$1`, installation); err != nil {
		return err
	}
	stalePolicy, err := postgres.NewRoutingPolicyRepo(tx).GetPolicy(ctx, installation.String())
	if err != nil {
		return err
	}
	if stalePolicy.ReportingExperimentID != "" || stalePolicy.Mode != auth.RoutingPolicyAssigned || stalePolicy.Revision != 8 {
		return errors.New("legacy team writer left stale reporting attribution or changed serving behavior")
	}
	// A pre-migration writer omits every reporting column.
	if _, err = tx.Exec(ctx, `INSERT INTO router.model_router_request_telemetry(installation_id,request_id,span_type,trace_id,timestamp) VALUES($1,'old-writer','router.upstream','old-trace',now())`, installation); err != nil {
		return err
	}
	var missing bool
	if err = tx.QueryRow(ctx, `SELECT reporting_schema_version IS NULL AND reporting_mode IS NULL AND reporting_experiment_id IS NULL AND reporting_revision IS NULL AND reporting_assigned_arm IS NULL AND reporting_treatment_applied IS NULL AND reporting_bypass_reason IS NULL AND reporting_subject_key IS NULL FROM router.model_router_request_telemetry WHERE installation_id=$1 AND request_id='old-writer'`, installation).Scan(&missing); err != nil {
		return err
	}
	if !missing {
		return errors.New("old writer fabricated reporting provenance")
	}
	version, revision, applied := proxy.ReportingProvenanceSchemaVersion, int64(7), true
	if err = postgres.NewTelemetryRepo(tx).InsertRequestTelemetry(ctx, proxy.InsertTelemetryParams{InstallationID: installation.String(), RequestID: "marked-writer", SpanType: "router.upstream", TraceID: "marked-trace", Timestamp: time.Now(), ReportingSchemaVersion: &version, ReportingMode: auth.ReportingModeTeams, ReportingExperimentID: experiment.String(), ReportingRevision: &revision, ReportingAssignedArm: auth.BlindExperimentArmRouterOn, ReportingTreatmentApplied: &applied, ReportingSubjectKey: user.String()}); err != nil {
		return err
	}
	var gotVersion int16
	var gotRevision int64
	var gotApplied bool
	var mode, id, arm, subject string
	var bypass *string
	if err = tx.QueryRow(ctx, `SELECT reporting_schema_version,reporting_mode,reporting_experiment_id::text,reporting_revision,reporting_assigned_arm,reporting_treatment_applied,reporting_bypass_reason,reporting_subject_key FROM router.model_router_request_telemetry WHERE installation_id=$1 AND request_id='marked-writer'`, installation).Scan(&gotVersion, &mode, &id, &gotRevision, &arm, &gotApplied, &bypass, &subject); err != nil {
		return err
	}
	if gotVersion != version || mode != "teams" || id != experiment.String() || gotRevision != revision || arm != "router_on" || !gotApplied || bypass != nil || subject != user.String() {
		return errors.New("persisted reporting provenance differs from request snapshot")
	}
	return tx.Rollback(ctx)
}
