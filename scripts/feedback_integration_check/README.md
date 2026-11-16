# feedback_integration_check

Drives the real `internal/api/feedback` gin handlers through the real
`proxy.Service` and the real Postgres `FeedbackRepo` against a live database —
the full router-side feedback-link path minus the LLM proxy itself.

This exercises actual SQL behavior (natural-key upsert dedup, blank-comment
collapse to `NULL`, cascade delete on installation removal) that the
in-memory `fakeFeedbackRepo` used by `internal/api/feedback/feedback_test.go`
can't cover. DB-backed integration tests stay out of `internal/`, so this
lives here as a script rather than
`internal/api/feedback/integration_test.go`.

It is **not** a test (`package main`), so `go test ./...` never touches
Postgres. It is gated on `ROUTER_TEST_DATABASE_URL` and is a no-op without it.

## Checks

1. **Feedback link end to end.** Seeds an installation and one `router.upstream`
   telemetry row, then drives GET → POST → GET → POST: the first GET returns the
   routing context with no feedback, a thumbs-down with a comment persists trimmed,
   and revising to thumbs-up with a blank comment keeps one row (natural key) with
   the comment collapsed to `NULL`. Cleanup deletes the installation; feedback rows
   cascade.
2. **Feedback without telemetry.** Regression guard: a saved rating must still be
   returned when its request has no telemetry row (telemetry disabled or pruned, or
   still in flight through the async telemetry write), with an empty routing context.

## Usage (from the repo root)

Bring up the local `docker compose` Postgres stack with router migrations
applied, then:

```bash
ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5432/router?search_path=router" \
    go run ./scripts/feedback_integration_check
```

Exits non-zero and prints which check failed on any mismatch.
