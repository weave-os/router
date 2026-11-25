package analytics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/analytics"
	analyticsapi "weave-os/router/internal/api/analytics"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingUsageRepo struct {
	rows  []analytics.UsageRow
	query analytics.UsageQuery
}

func (r *capturingUsageRepo) GetUsageRows(_ context.Context, q analytics.UsageQuery) ([]analytics.UsageRow, error) {
	r.query = q
	rows := r.rows
	if q.Cursor.Offset >= len(rows) {
		return nil, nil
	}
	rows = rows[q.Cursor.Offset:]
	if len(rows) > q.Limit {
		return rows[:q.Limit], nil
	}
	return rows, nil
}

func serveUsage(t *testing.T, repo *capturingUsageRepo, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/usage", analyticsapi.UsageHandler(analytics.NewUsageService(repo, func() time.Time { return usageTestNow })))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/usage?"+rawQuery, nil))
	return rec
}

var usageTestNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func stringPtr(s string) *string { return &s }

func TestUsageHandlerParsesContract(t *testing.T) {
	repo := &capturingUsageRepo{}
	rec := serveUsage(t, repo, "organization_id=org-1&start=2026-11-01&end=2026-11-30&group_by=user,served_model&emails=a@example.com,b@example.com&api_key_ids=00000000-0000-0000-0000-000000000001&limit=50")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	q := repo.query
	assert.Equal(t, "org-1", q.OrganizationID)
	assert.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), q.Start)
	assert.Equal(t, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), q.End)
	assert.Equal(t, map[analytics.UsageGroup]struct{}{analytics.UsageGroupUser: {}, analytics.UsageGroupServedModel: {}}, q.Groups)
	assert.Equal(t, []string{"a@example.com", "b@example.com"}, q.Emails)
	assert.Equal(t, []string{"00000000-0000-0000-0000-000000000001"}, q.APIKeyIDs)
	assert.Equal(t, 51, q.Limit, "the service asks for one look-ahead row")
}

func TestUsageHandlerDefaultsToEveryGroup(t *testing.T) {
	repo := &capturingUsageRepo{}
	rec := serveUsage(t, repo, "organization_id=org-1&start=2026-11-01&end=2026-11-01")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, repo.query.Groups, len(analytics.UsageGroups))
	assert.Equal(t, analytics.UsageDefaultLimit+1, repo.query.Limit)
}

func TestUsageHandlerRejectsBadRequests(t *testing.T) {
	cases := map[string]string{
		"missing organization": "start=2026-11-01&end=2026-11-02",
		"missing start":        "organization_id=o&end=2026-11-02",
		"bad date":             "organization_id=o&start=2026-11-01T00:00:00Z&end=2026-11-02",
		"end before start":     "organization_id=o&start=2026-11-02&end=2026-11-01",
		"window over 92 days":  "organization_id=o&start=2026-01-01&end=2026-04-03",
		"unknown group":        "organization_id=o&start=2026-11-01&end=2026-11-02&group_by=team",
		"limit too large":      "organization_id=o&start=2026-11-01&end=2026-11-02&limit=10001",
		"bad cursor":           "organization_id=o&start=2026-11-01&end=2026-11-02&cursor=!!",
		"malformed api key id": "organization_id=o&start=2026-11-01&end=2026-11-02&api_key_ids=not-a-uuid",
		"limit over int32":     "organization_id=o&start=2026-11-01&end=2026-11-02&limit=99999999999",
	}
	for name, rawQuery := range cases {
		t.Run(name, func(t *testing.T) {
			rec := serveUsage(t, &capturingUsageRepo{}, rawQuery)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestUsageHandlerAcceptsExactly92Days(t *testing.T) {
	rec := serveUsage(t, &capturingUsageRepo{}, "organization_id=o&start=2026-01-01&end=2026-04-02")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestUsageHandlerPagesAndEncodesNulls(t *testing.T) {
	repo := &capturingUsageRepo{rows: []analytics.UsageRow{
		{Date: "2026-11-20", Email: stringPtr("a@example.com"), Requests: 2, CostUSDMicros: 1_500_000, APIRateCostUSDMicros: 1_700_000},
		{Date: "2026-11-21", Requests: 1},
		{Date: "2026-11-22", Requests: 1},
	}}
	rec := serveUsage(t, repo, "organization_id=o&start=2026-11-01&end=2026-11-30&limit=2")
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Rows       []map[string]any `json:"rows"`
		NextCursor *string          `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Rows, 2)
	require.NotNil(t, body.NextCursor)
	assert.Equal(t, "a@example.com", body.Rows[0]["email"])
	assert.Nil(t, body.Rows[0]["router_user_id"], "ungrouped dimensions serialize as null")
	assert.EqualValues(t, 1_500_000, body.Rows[0]["cost_usd_micros"])

	next := serveUsage(t, repo, "organization_id=o&start=2026-11-01&end=2026-11-30&limit=2&cursor="+*body.NextCursor)
	require.Equal(t, http.StatusOK, next.Code)
	assert.Equal(t, 2, repo.query.Cursor.Offset)
	assert.Equal(t, usageTestNow.Add(-analytics.Holdback), repo.query.Cursor.IngestedBefore, "every page reads the first page's ingest cutoff")
	var last struct {
		Rows       []map[string]any `json:"rows"`
		NextCursor *string          `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(next.Body.Bytes(), &last))
	require.Len(t, last.Rows, 1)
	assert.Equal(t, "2026-11-22", last.Rows[0]["date"])
	assert.Nil(t, last.NextCursor, "the last page has no cursor")
}
