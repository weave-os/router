package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"weave-os/router/cmd/logconfig"
	feedbackapi "weave-os/router/internal/api/feedback"
	token "weave-os/router/internal/feedback"
	"weave-os/router/internal/postgres"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logconfig.Configure()
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		fmt.Println("ROUTER_TEST_DATABASE_URL not set; skipping live-DB feedback check (see file header for usage)")
		return
	}
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fail("connect to database", err)
	}
	defer pool.Close()

	if err := checkFeedbackLinkEndToEnd(ctx, pool); err != nil {
		fail("feedback link end-to-end check", err)
	}
	fmt.Println("ok: feedback link end-to-end")

	if err := checkFeedbackWithoutTelemetry(ctx, pool); err != nil {
		fail("feedback without telemetry check", err)
	}
	fmt.Println("ok: feedback without telemetry")
}

func fail(step string, err error) {
	fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", step, err)
	os.Exit(1)
}

func checkFeedbackLinkEndToEnd(ctx context.Context, pool *pgxpool.Pool) error {
	externalID := "org_e2e_" + uuid.NewString()[:8]
	requestID := "req_e2e_" + uuid.NewString()[:8]
	var installID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO router.model_router_installations (external_id, name)
		 VALUES ($1, 'E2E feedback test') RETURNING id`, externalID,
	).Scan(&installID); err != nil {
		return fmt.Errorf("insert installation: %w", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_request_telemetry WHERE installation_id = $1`, installID)
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_installations WHERE id = $1`, installID)
	}()

	_, err := pool.Exec(ctx,
		`INSERT INTO router.model_router_request_telemetry
		   (installation_id, request_id, span_type, trace_id, timestamp,
		    requested_model, decision_model, decision_provider, client_app)
		 VALUES ($1, $2, 'router.upstream', 'trace-e2e', NOW(),
		    'claude-sonnet-4', 'claude-haiku-4-5', 'anthropic', 'claude-code')`,
		installID, requestID)
	if err != nil {
		return fmt.Errorf("insert telemetry: %w", err)
	}

	signer := token.NewSigner("e2e-secret", time.Hour)
	svc := token.NewLinks(postgres.NewFeedbackRepo(pool), signer, "https://router.example.com", nil, time.Now)

	engine := gin.New()
	engine.GET("/v1/feedback/link/:token", feedbackapi.GetContextHandler(svc))
	engine.POST("/v1/feedback/link", feedbackapi.SubmitHandler(svc))

	tok := signer.Mint(installID, externalID, requestID, "")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/feedback/link/"+tok, nil))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("initial GET: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var ctxResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ctxResp); err != nil {
		return fmt.Errorf("unmarshal initial GET: %w", err)
	}
	if err := requireEqual("request_id", requestID, ctxResp["request_id"]); err != nil {
		return err
	}
	if err := requireEqual("chosen_model", "claude-haiku-4-5", ctxResp["chosen_model"]); err != nil {
		return err
	}
	if err := requireEqual("chosen_provider", "anthropic", ctxResp["chosen_provider"]); err != nil {
		return err
	}
	if ctxResp["feedback"] != nil {
		return fmt.Errorf("feedback: want nil before any submission, got %v", ctxResp["feedback"])
	}

	body := `{"token":"` + tok + `","rating":"down","comment":"  routed too low  "}`
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/feedback/link", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("first POST: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/feedback/link/"+tok, nil))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("second GET: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ctxResp); err != nil {
		return fmt.Errorf("unmarshal second GET: %w", err)
	}
	fb, ok := ctxResp["feedback"].(map[string]any)
	if !ok {
		return fmt.Errorf("feedback: want present after submit, got %v", ctxResp["feedback"])
	}
	if err := requireEqual("feedback.rating", "down", fb["rating"]); err != nil {
		return err
	}
	if err := requireEqual("feedback.comment", "routed too low", fb["comment"]); err != nil {
		return err
	}

	body = `{"token":"` + tok + `","rating":"up","comment":"   "}`
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/feedback/link", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("revision POST: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var rowCount int
	var rating string
	var comment *string
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM router.request_feedback WHERE installation_id = $1 AND request_id = $2`,
		installID, requestID).Scan(&rowCount); err != nil {
		return fmt.Errorf("count rows: %w", err)
	}
	if rowCount != 1 {
		return fmt.Errorf("row count: want 1 (upsert must not duplicate rows), got %d", rowCount)
	}
	if err := pool.QueryRow(ctx,
		`SELECT rating, comment FROM router.request_feedback WHERE installation_id = $1 AND request_id = $2`,
		installID, requestID).Scan(&rating, &comment); err != nil {
		return fmt.Errorf("select revised row: %w", err)
	}
	if rating != "up" {
		return fmt.Errorf("rating: want up, got %s", rating)
	}
	if comment != nil {
		return fmt.Errorf("comment: want nil (blank collapses to NULL), got %q", *comment)
	}
	return nil
}

func checkFeedbackWithoutTelemetry(ctx context.Context, pool *pgxpool.Pool) error {
	externalID := "org_nt_" + uuid.NewString()[:8]
	requestID := "req_nt_" + uuid.NewString()[:8]
	var installID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO router.model_router_installations (external_id, name)
		 VALUES ($1, 'E2E feedback no-telemetry') RETURNING id`, externalID,
	).Scan(&installID); err != nil {
		return fmt.Errorf("insert installation: %w", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM router.model_router_installations WHERE id = $1`, installID)
	}()

	_, err := pool.Exec(ctx,
		`INSERT INTO router.request_feedback (installation_id, external_id, request_id, rating, comment, source)
		 VALUES ($1, $2, $3, 'down', 'no telemetry yet', 'link')`,
		installID, externalID, requestID)
	if err != nil {
		return fmt.Errorf("insert feedback: %w", err)
	}

	signer := token.NewSigner("nt-secret", time.Hour)
	svc := token.NewLinks(postgres.NewFeedbackRepo(pool), signer, "https://router.example.com", nil, time.Now)
	engine := gin.New()
	engine.GET("/v1/feedback/link/:token", feedbackapi.GetContextHandler(svc))

	tok := signer.Mint(installID, externalID, requestID, "")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/feedback/link/"+tok, nil))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("GET: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return fmt.Errorf("unmarshal GET: %w", err)
	}
	if err := requireEqual("request_id", requestID, resp["request_id"]); err != nil {
		return err
	}
	if resp["chosen_model"] != nil {
		return fmt.Errorf("chosen_model: want nil (no telemetry => empty routing context), got %v", resp["chosen_model"])
	}
	fb, ok := resp["feedback"].(map[string]any)
	if !ok {
		return fmt.Errorf("feedback: want present even without telemetry, got %v", resp["feedback"])
	}
	if err := requireEqual("feedback.rating", "down", fb["rating"]); err != nil {
		return err
	}
	if err := requireEqual("feedback.comment", "no telemetry yet", fb["comment"]); err != nil {
		return err
	}
	return nil
}

func requireEqual(field string, want, got any) error {
	if want != got {
		return fmt.Errorf("%s: want %v, got %v", field, want, got)
	}
	return nil
}
