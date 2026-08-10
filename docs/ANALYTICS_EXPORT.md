# Routing decision export

The analytics export streams the router's **raw routing decisions** — one row
per upstream action, nothing aggregated, nothing rounded — so your team can
load them into a warehouse and do its own analysis.

It is a pull API. You issue a read-only key, poll a cursor, and append what
comes back. There is no dashboard in the loop and no pre-aggregation: every
number the router's own dashboard shows is derived from these same rows.

Read [SEMANTICS.md](SEMANTICS.md) first for the difference between a session, a
round, a turn, and an action. The distinction decides what a row *is*.

---

## 1. Issue an analytics key

Analytics keys are read-only. They carry the `ra_` prefix, they authenticate
**only** the `/v1/analytics/*` endpoints, and they cannot route inference,
resolve provider credentials, or spend money. A key that is stolen from an ETL
config can read telemetry and nothing else.

**Self-hosted:** Settings → API keys → *Issue key* → scope **Analytics
(read-only)**.

**Managed:** ask your Weave contact, or issue one from the dashboard's API-keys
page the same way.

```bash
export WEAVE_ANALYTICS_KEY=ra_...
export WEAVE_ROUTER_URL=https://your-router.example.com
```

Routing keys (`rk_`) are rejected here with `401`, and analytics keys are
rejected on every routing endpoint. The separation is enforced on both sides,
not by convention.

---

## 2. Pull a page

```bash
curl -sS --compressed \
  -H "Authorization: Bearer $WEAVE_ANALYTICS_KEY" \
  "$WEAVE_ROUTER_URL/v1/analytics/routing-decisions?since=2026-01-01T00:00:00Z&limit=1000" \
  -D headers.txt -o page.ndjson
```

The body is [NDJSON](https://ndjson.org): one JSON object per line, no
envelope, no trailing array. Pipe it straight into a loader.

| Parameter | Meaning |
|---|---|
| `since` | RFC3339, inclusive. Required on the first page; ignored once you pass `cursor`. |
| `until` | RFC3339, exclusive. Optional. Use it to close a backfill window. |
| `cursor` | Opaque resume token from the previous page's `X-Weave-Next-Cursor`. |
| `limit` | Rows per page. Default 1000, max 10000. |
| `format` | `ndjson` (the only supported value today). |

| Response header | Meaning |
|---|---|
| `X-Weave-Next-Cursor` | Pass as `cursor` to get the next page. |
| `X-Weave-Has-More` | `true` while more rows are ready; `false` means you have caught up. |

Send `Accept-Encoding: gzip` (curl's `--compressed`) — these rows are
repetitive JSON and compress by roughly an order of magnitude.

### Paging to the end and staying caught up

```bash
cursor=""
while :; do
  url="$WEAVE_ROUTER_URL/v1/analytics/routing-decisions?limit=10000"
  if [ -z "$cursor" ]; then url="$url&since=2026-01-01T00:00:00Z"; else url="$url&cursor=$cursor"; fi

  curl -sS --compressed -H "Authorization: Bearer $WEAVE_ANALYTICS_KEY" \
    "$url" -D headers.txt >> routing_decisions.ndjson

  cursor=$(grep -i '^x-weave-next-cursor:' headers.txt | tr -d '\r' | cut -d' ' -f2)
  [ "$(grep -i '^x-weave-has-more:' headers.txt | tr -d '\r' | cut -d' ' -f2)" = "true" ] || break
done
```

Persist `cursor` between runs. A job that stores its cursor and polls every few
minutes is a complete incremental pipeline — the same loop does the initial
backfill and the steady-state tail.

**Rate limit:** 60 requests per minute per key, returning `429` with
`Retry-After` when exceeded. At the maximum page size that is 600k rows a
minute, so it constrains only pathological polling.

---

## 3. What a row is

**One row per upstream action.** An action is a single API call the agent makes
to a model. That is the router's finest routing grain, and it is deliberately
*not* "one row per user message". A single thing a person would call one
request commonly produces several rows:

- a retry after an upstream error,
- a failover to a different provider,
- a context-compaction call,
- a title-generation call,
- a classifier call,
- a sub-agent turn.

So `COUNT(*)` is a count of **model calls**, not of user requests. To count
user-visible turns, filter `turn_type = 'main_loop'`. To collapse an action's
retries and failovers, group by `request_id`.

```sql
-- Model calls per day (what the router actually did)
SELECT date_trunc('day', recorded_at) AS day, count(*)
FROM routing_decisions GROUP BY 1;

-- User-visible turns per day
SELECT date_trunc('day', recorded_at) AS day, count(*)
FROM routing_decisions WHERE turn_type = 'main_loop' GROUP BY 1;

-- Distinct routed requests (retries and failovers collapsed)
SELECT count(DISTINCT request_id) FROM routing_decisions;
```

### Rows are immutable, so replays are free

A row is written once and never updated. `id` is unique. Deduplicate on `id`
and a replayed page is a no-op merge — safe to re-run any page, any time.

```sql
MERGE INTO routing_decisions t USING staging s ON t.id = s.id
WHEN NOT MATCHED THEN INSERT ...;
```

### Ordering and the 60-second holdback

The export is ordered by **`recorded_at` (ingest time)**, then `id` — not by
`requested_at` (event time). Telemetry is written after a turn completes, so
event time is not monotonic: within one page `requested_at` can move backwards.
Order by ingest time is what makes "resume at this cursor, miss nothing" true.

For the same reason the export withholds the most recent **60 seconds**. A row
committing right now could land just behind a cursor you already passed. The
tail you can read is always about a minute behind live; that is the price of
never losing a row. If you need a fixed window, an explicit `until` narrower
than the holdback boundary is honored as-is.

Consequence worth designing around: **partition your warehouse table on
`requested_at`, but bookmark on `recorded_at`.** They are different clocks.

---

## 4. Fields

`GET /v1/analytics/schema` returns the machine-readable field dictionary —
name, type, nullability, description — plus the grain, ordering, and holdback
semantics above. Generate your DDL from it rather than hand-transcribing this
page.

```bash
curl -sS -H "Authorization: Bearer $WEAVE_ANALYTICS_KEY" \
  "$WEAVE_ROUTER_URL/v1/analytics/schema" | jq .
```

Field groups:

| Group | Fields |
|---|---|
| Identity | `id`, `recorded_at`, `requested_at`, `request_id`, `trace_id`, `session_id`, `device_id`, `client_app`, `turn_type` |
| End user | `user_id`, `user_email`, `user_account_uuid` |
| Decision | `requested_model`, `decision_model`, `decision_provider`, `candidate_models`, `chosen_score`, `decision_reason`, `sticky_hit`, `failover_used`, `cross_format` |
| Tokens | `estimated_input_tokens`, `input_tokens`, `output_tokens`, `cache_creation_tokens`, `cache_read_tokens` |
| Economics | `actual_input_cost_usd`, `actual_output_cost_usd` |
| Performance | `route_latency_ms`, `upstream_latency_ms`, `total_latency_ms`, `ttft_ms` |
| Outcome | `upstream_status_code`, `upstream_finish_reason`, `stop_reason`, `tool_use_blocks`, `invalid_tool_args_blocks` |

The export deliberately omits the scorer's internals (cluster assignments,
per-candidate scores, exploration propensities, policy artifact identifiers),
shadow and counterfactual evaluations, and every credential-bearing field.

Schema changes are additive within a version; `version` in the `/schema`
response bumps only if a field is removed or changes meaning. Write your loader
to tolerate new columns. Version `2` dropped `requested_input_cost_usd`,
`requested_output_cost_usd`, and `savings_usd` — see
[Computing savings yourself](#computing-savings-yourself).

### `decision_reason` is prose, not an enum

It is free-form diagnostic text written for humans debugging a decision, and
its format changes between router versions. **Do not parse it** and do not
group by it — a dashboard built on string matching will break silently on a
router upgrade. Group on the stable fields instead: `decision_model`,
`sticky_hit`, `failover_used`, `candidate_models`.

### Computing savings yourself

The export reports only what each turn actually cost (`actual_*`). It does not
ship a counterfactual cost or a precomputed `savings_usd`, because the
counterfactual depends on assumptions you should own: which baseline model you
would otherwise have run, whether its prompt cache would have been as warm, and
which price you hold it to.

Build the baseline yourself from the token columns and a price book:

```sql
WITH priced AS (
    SELECT d.actual_input_cost_usd + d.actual_output_cost_usd AS actual_usd,
           (d.input_tokens  / 1e6) * p.input_usd_per_1m
         + (d.output_tokens / 1e6) * p.output_usd_per_1m       AS baseline_usd
    FROM routing_decisions d
    JOIN model_prices p ON p.model = d.requested_model
    WHERE d.recorded_at >= now() - interval '30 days'
      AND d.actual_input_cost_usd IS NOT NULL
)
SELECT sum(baseline_usd - actual_usd) AS savings_usd FROM priced;
```

`GET /v1/analytics/models` publishes the price book that populates
`model_prices` — per model, per provider, input and output USD per 1M tokens
plus the cache-read multiplier. Note it reports **current** prices, not the
prices in force when an old row was recorded; pin your own price table if you
need the historical ones.

Cost fields are null for rows the router could not price (an unknown model, an
upstream error before any usage was reported). They are null rather than `0` so
they drop out of an average instead of dragging it down — filter explicitly, as
above, if you want the priced subset.

---

## 5. Operating notes (self-hosted)

- The export reads the same `router.model_router_request_telemetry` table the
  dashboard reads, through a dedicated partial index on
  `(installation_id, created_at, id) WHERE span_type = 'router.upstream'`. Pages
  are index scans with no sort at any page depth; a deep backfill does not
  degrade into a growing `OFFSET` scan.
- The export is read-only and safe to point at a read replica.
- Rows are scoped to the key's installation. There is no cross-installation
  query surface.
- If the endpoints return `404`, the router was started without an analytics
  service — check that the process has database wiring; the routes mount only
  when it is present.
