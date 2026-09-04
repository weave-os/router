package analytics_test

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/analytics"
	analyticsapi "weave-os/router/internal/api/analytics"
	"weave-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeRepo struct {
	rows []analytics.Decision
	err  error
}

func (f *fakeRepo) GetRoutingDecisions(_ context.Context, q analytics.Query) ([]analytics.Decision, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.rows) > q.Limit {
		return f.rows[:q.Limit], nil
	}
	return f.rows, nil
}

var (
	testBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testNow  = testBase.Add(time.Hour)
)

func newRouter(repo analytics.Repository, installation *auth.Installation) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	svc := analytics.NewService(repo, func() time.Time { return testNow })
	engine.GET("/v1/analytics/routing-decisions", func(c *gin.Context) {
		if installation != nil {
			c.Set("router_installation", installation)
		}
	}, analyticsapi.RoutingDecisionsHandler(svc))
	return engine
}

func rowsAt(n int) []analytics.Decision {
	out := make([]analytics.Decision, 0, n)
	for i := range n {
		out = append(out, analytics.Decision{
			ID:         string(rune('a' + i)),
			RecordedAt: testBase.Add(time.Duration(i) * time.Second),
			RequestID:  "req-" + string(rune('a'+i)),
		})
	}
	return out
}

func get(engine *gin.Engine, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func decodeNDJSON(t *testing.T, body io.Reader) []map[string]any {
	t.Helper()
	var out []map[string]any
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		out = append(out, row)
	}
	require.NoError(t, scanner.Err())
	return out
}

// The body must be one JSON object per line — a warehouse loader streams it
// line by line and would choke on a JSON array or an envelope.
func TestRoutingDecisionsStreamsNDJSON(t *testing.T) {
	engine := newRouter(&fakeRepo{rows: rowsAt(3)}, &auth.Installation{ID: "inst"})

	rec := get(engine, "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	rows := decodeNDJSON(t, rec.Body)
	require.Len(t, rows, 3)
	require.Equal(t, "req-a", rows[0]["request_id"])
}

// Cursor and has-more travel as headers so the body stays pure rows.
func TestRoutingDecisionsReturnsCursorHeaders(t *testing.T) {
	engine := newRouter(&fakeRepo{rows: rowsAt(3)}, &auth.Installation{ID: "inst"})

	rec := get(engine, "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z&limit=2", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", rec.Header().Get("X-Weave-Has-More"))
	cursor := rec.Header().Get("X-Weave-Next-Cursor")
	require.NotEmpty(t, cursor)
	parsed, err := analytics.ParseCursor(cursor)
	require.NoError(t, err)
	require.Equal(t, "b", parsed.ID, "cursor must point at the last row served, not the probe row")
	require.Len(t, decodeNDJSON(t, rec.Body), 2)
}

func TestRoutingDecisionsCompressesWhenAccepted(t *testing.T) {
	engine := newRouter(&fakeRepo{rows: rowsAt(3)}, &auth.Installation{ID: "inst"})

	rec := get(engine, "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z",
		map[string]string{"Accept-Encoding": "gzip"})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	gz, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	defer func() { require.NoError(t, gz.Close()) }()
	require.Len(t, decodeNDJSON(t, gz), 3)
}

func TestRoutingDecisionsPlainWhenGzipNotAccepted(t *testing.T) {
	engine := newRouter(&fakeRepo{rows: rowsAt(1)}, &auth.Installation{ID: "inst"})

	rec := get(engine, "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z", nil)

	require.Empty(t, rec.Header().Get("Content-Encoding"))
	require.Len(t, decodeNDJSON(t, rec.Body), 1)
}

func TestRoutingDecisionsRejectsBadRequests(t *testing.T) {
	cases := map[string]string{
		"no window":      "/v1/analytics/routing-decisions",
		"bad since":      "/v1/analytics/routing-decisions?since=yesterday",
		"bad until":      "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z&until=tomorrow",
		"bad cursor":     "/v1/analytics/routing-decisions?cursor=nonsense",
		"zero limit":     "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z&limit=0",
		"negative limit": "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z&limit=-5",
		"huge limit":     "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z&limit=100000",
		"unknown format": "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z&format=parquet",
	}
	engine := newRouter(&fakeRepo{}, &auth.Installation{ID: "inst"})

	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			rec := get(engine, target, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

// The handler is mounted behind WithAnalyticsKey, but it must not serve
// another installation's telemetry if it is ever mounted without it.
func TestRoutingDecisionsRequiresInstallation(t *testing.T) {
	engine := newRouter(&fakeRepo{rows: rowsAt(3)}, nil)

	rec := get(engine, "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z", nil)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NotContains(t, rec.Body.String(), "req-a")
}

func TestRoutingDecisionsRepositoryFailureIs500(t *testing.T) {
	engine := newRouter(&fakeRepo{err: errors.New("db down")}, &auth.Installation{ID: "inst"})

	rec := get(engine, "/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z", nil)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "db down")
}
