package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type observationReport struct {
	InstallationID      uuid.UUID   `json:"installation_id"`
	Since               time.Time   `json:"since"`
	Until               time.Time   `json:"until"`
	UpstreamRows        int64       `json:"upstream_rows"`
	DistinctClusterIDs  []int32     `json:"distinct_cluster_ids"`
	RowsWithoutClusters int64       `json:"rows_without_clusters"`
	ModelPicks          []modelPick `json:"model_picks"`
}

type modelPick struct {
	DecisionModel *string `json:"decision_model"`
	UpstreamRows  int64   `json:"upstream_rows"`
	Percent       float64 `json:"percent"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv("DATABASE_URL"), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, dsn string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify-routing-observations", flag.ContinueOnError)
	flags.SetOutput(stderr)
	installation := flags.String("installation", "", "staging installation UUID (required)")
	since := flags.String("since", "", "inclusive window start in RFC3339 (required)")
	until := flags.String("until", "", "exclusive window end in RFC3339 (required)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("unexpected positional arguments")
	}

	installationID, err := uuid.Parse(*installation)
	if err != nil || installationID == uuid.Nil {
		return errors.New("-installation must be a nonzero UUID")
	}
	fromTime, err := time.Parse(time.RFC3339, *since)
	if err != nil {
		return errors.New("-since must be an RFC3339 timestamp")
	}
	toTime, err := time.Parse(time.RFC3339, *until)
	if err != nil {
		return errors.New("-until must be an RFC3339 timestamp")
	}
	if !fromTime.Before(toTime) {
		return errors.New("-until must be later than -since")
	}
	if dsn == "" {
		return errors.New("DATABASE_URL must be set explicitly")
	}
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("DATABASE_URL is invalid")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return fmt.Errorf("connect to telemetry database: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	// The two aggregates must see the same rows while the Router writes telemetry.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin telemetry snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := sqlc.New(tx)
	window := sqlc.SummarizeRoutingObservationsParams{
		InstallationID: installationID,
		FromTime:       pgtype.Timestamptz{Time: fromTime, Valid: true},
		ToTime:         pgtype.Timestamptz{Time: toTime, Valid: true},
	}
	summary, err := queries.SummarizeRoutingObservations(ctx, window)
	if err != nil {
		return fmt.Errorf("summarize routing observations: %w", err)
	}
	modelCounts, err := queries.CountRoutingObservationModelPicks(ctx, sqlc.CountRoutingObservationModelPicksParams(window))
	if err != nil {
		return fmt.Errorf("count routing model picks: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish telemetry snapshot: %w", err)
	}

	picks := make([]modelPick, 0, len(modelCounts))
	for _, count := range modelCounts {
		pick := modelPick{
			UpstreamRows: count.UpstreamRows,
			Percent:      100 * float64(count.UpstreamRows) / float64(summary.UpstreamRows),
		}
		if count.DecisionModel != "" {
			pick.DecisionModel = &count.DecisionModel
		}
		picks = append(picks, pick)
	}
	report := observationReport{
		InstallationID:      installationID,
		Since:               fromTime.UTC(),
		Until:               toTime.UTC(),
		UpstreamRows:        summary.UpstreamRows,
		DistinctClusterIDs:  summary.ClusterIds,
		RowsWithoutClusters: summary.RowsWithoutClusters,
		ModelPicks:          picks,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write routing observation report: %w", err)
	}
	return nil
}
