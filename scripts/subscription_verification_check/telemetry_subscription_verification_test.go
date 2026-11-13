package subscription_verification_check_test

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"net/url"
	"os"
	"testing"
	"time"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/sqlc"
)

func TestVerificationSQLWinningAttributionAndPaidExclusion(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("local Postgres integration requires ROUTER_TEST_DATABASE_URL")
	}
	require.NotEmpty(t, dsn)
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, parsed.Hostname())
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	org := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO router.model_router_installations(id,external_id,name) VALUES($1,$2,'telemetry-verification')`, org, org.String())
	require.NoError(t, err)
	queries := sqlc.New(tx)
	account, owner := uuid.New(), uuid.New()
	now := time.Now()
	stringPtr := func(value string) *string { return &value }
	cases := []struct {
		id, source, headers string
		status              int32
		included            bool
	}{
		{"included", "codex_subscription", "{}", 200, true},
		{"historical-success", "codex_subscription", "{}", 0, true},
		{"paid-source", "subscription_overage", "{}", 200, false},
		{"historical-paid", "subscription", `{"anthropic-ratelimit-unified-representative-claim":"overage","anthropic-ratelimit-unified-overage-in-use":"true"}`, 200, false},
		{"failed-subscription", "codex_subscription", "{}", 429, false},
		{"api-fallback", "", "{}", 200, false},
	}
	for _, tc := range cases {
		arg := sqlc.InsertRequestTelemetryParams{InstallationID: org, RequestID: tc.id, SpanType: "router.upstream", TraceID: "synthetic-trace", Timestamp: pgtype.Timestamptz{Time: now, Valid: true}, RequestedModel: "auto", DecisionModel: "gpt-5.6-sol", DecisionProvider: providers.ProviderOpenAI, DecisionReason: "synthetic", EmbedInput: "concatenated_stream", UpstreamStatusCode: tc.status, SubscriptionAccountID: pgtype.UUID{Bytes: account, Valid: true}, SubscriptionOwnerID: pgtype.UUID{Bytes: owner, Valid: true}, SubscriptionTier: stringPtr("shared"), IntendedModelFamily: stringPtr("claude-opus-5"), FinalModelFamily: stringPtr("gpt-5.6-sol"), UnifiedLimitHeaders: []byte(tc.headers)}
		if tc.source != "" {
			arg.CredentialSource = stringPtr(tc.source)
		}
		require.NoError(t, queries.InsertRequestTelemetry(ctx, arg))
		require.NoError(t, queries.InsertRequestTelemetry(ctx, arg), "replayed ingestion must remain exactly once")
	}
	rows, err := queries.GetRoutingDecisionsForExport(ctx, sqlc.GetRoutingDecisionsForExportParams{InstallationID: org, FromTime: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, ToTime: pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}, RowLimit: 100})
	require.NoError(t, err)
	require.Len(t, rows, len(cases), "all failed/fallback requests retained once")
	expected := map[string]bool{}
	for _, tc := range cases {
		expected[tc.id] = tc.included
	}
	for _, row := range rows {
		require.Equal(t, expected[row.RequestID], row.SubscriptionServed, row.RequestID)
		require.Equal(t, account, uuid.UUID(row.SubscriptionAccountID.Bytes))
		require.Equal(t, owner, uuid.UUID(row.SubscriptionOwnerID.Bytes))
		require.Equal(t, "shared", *row.SubscriptionTier)
		require.Equal(t, "claude-opus-5", *row.IntendedModelFamily)
		require.Equal(t, "gpt-5.6-sol", *row.FinalModelFamily)
	}
}
