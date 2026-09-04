// Package analytics serves the read-only routing-decision export
// (/v1/analytics/*). ra_ analytics keys only; reads telemetry, never routes.
package analytics

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"weave-os/router/internal/analytics"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// Headers rather than an envelope so the NDJSON body is a clean stream a
// warehouse loader can ingest without unwrapping.
const (
	nextCursorHeader = "X-Weave-Next-Cursor"
	hasMoreHeader    = "X-Weave-Has-More"
)

const contentTypeNDJSON = "application/x-ndjson"

// RoutingDecisionsHandler serves GET /v1/analytics/routing-decisions: one
// cursor-paginated page of raw, unaggregated routing decisions as NDJSON.
func RoutingDecisionsHandler(svc *analytics.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		if installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}

		params, err := parseExportParams(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
			return
		}
		params.InstallationID = installation.ID

		page, err := svc.ExportRoutingDecisions(c.Request.Context(), params)
		if err != nil {
			switch {
			case errors.Is(err, analytics.ErrInvalidCursor), errors.Is(err, analytics.ErrWindowRequired):
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": err.Error()})
			default:
				observability.FromGin(c).Error("Analytics export failed", "err", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "export_failed"})
			}
			return
		}

		// Page is fully materialized before the first byte goes out; a mid-stream
		// repository error can't leave a consumer with a truncated page.
		c.Header(nextCursorHeader, page.NextCursor)
		c.Header(hasMoreHeader, strconv.FormatBool(page.HasMore))
		writePage(c, page)
	}
}

func writePage(c *gin.Context, page analytics.Page) {
	body, closeBody := responseWriter(c)
	defer closeBody()

	c.Status(http.StatusOK)
	enc := json.NewEncoder(body)
	for _, decision := range page.Decisions {
		if err := enc.Encode(decision); err != nil {
			// Headers are already out; the consumer sees a short read and
			// retries the same cursor, which is safe because rows are immutable.
			observability.FromGin(c).Error("Analytics export write failed", "err", err)
			return
		}
	}
}

// responseWriter negotiates gzip: export pages compress well, which matters
// for large backfills.
func responseWriter(c *gin.Context) (io.Writer, func()) {
	c.Header("Content-Type", contentTypeNDJSON)
	if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		return c.Writer, func() {}
	}
	c.Header("Content-Encoding", "gzip")
	c.Header("Vary", "Accept-Encoding")
	gz := gzip.NewWriter(c.Writer)
	return gz, func() {
		if err := gz.Close(); err != nil {
			observability.FromGin(c).Error("Analytics export gzip close failed", "err", err)
		}
	}
}

func parseExportParams(c *gin.Context) (analytics.ExportParams, error) {
	params := analytics.ExportParams{Cursor: strings.TrimSpace(c.Query("cursor"))}

	since, err := parseTimeParam(c, "since")
	if err != nil {
		return analytics.ExportParams{}, err
	}
	params.Since = since

	until, err := parseTimeParam(c, "until")
	if err != nil {
		return analytics.ExportParams{}, err
	}
	params.Until = until

	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return analytics.ExportParams{}, errors.New("limit must be a positive integer")
		}
		if limit > analytics.MaxLimit {
			return analytics.ExportParams{}, errors.New("limit exceeds " + strconv.Itoa(analytics.MaxLimit))
		}
		params.Limit = limit
	}

	if format := strings.TrimSpace(c.Query("format")); format != "" && format != "ndjson" {
		return analytics.ExportParams{}, errors.New("format must be ndjson")
	}
	return params, nil
}

func parseTimeParam(c *gin.Context, name string) (time.Time, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New(name + " must be an RFC3339 timestamp")
	}
	return parsed, nil
}
