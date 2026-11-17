# Endpoint reference

| Endpoint                       | Format                                   |
| ------------------------------ | ---------------------------------------- |
| `POST /v1/messages`            | Anthropic Messages, routed               |
| `POST /v1/chat/completions`    | OpenAI Chat Completions, routed          |
| `POST /v1beta/models/:action`  | Gemini `generateContent`, routed         |
| `POST /v1/route`               | Returns the decision, no upstream call   |
| `GET /v1/models` &nbsp;·&nbsp; `POST /v1/messages/count_tokens` | Anthropic passthrough |
| `GET /livez` &nbsp;·&nbsp; `GET /startupz` | process responsive check + completed initialization |
| `GET /health` &nbsp;·&nbsp; `GET /readyz` &nbsp;·&nbsp; `GET /validate` | responsive-check alias + dependency diagnostics + key check |
| `POST /v1/client-events`       | Harness CLI's `off`/`on`/`uninstall` report, or the Claude Desktop proxy's one-time `connected` report (`harness` = `claude_code`/`codex`/`opencode`/`pi`/`copilot`/`claude_desktop`); logged + exported as `router.harness_lifecycle` (`connected` as `router.harness_connected`), nothing stored |
| `GET /v1/sessions/:session_id/cost` | One session's committed cost + savings, scoped to your key's installation; `rk_` or `ra_` key ([docs](SESSION_COST.md)) |
| `GET /v1/analytics/routing-decisions` | Raw routing decisions as cursor-paginated NDJSON ([docs](ANALYTICS_EXPORT.md)) |
| `GET /v1/analytics/schema` &nbsp;·&nbsp; `GET /v1/analytics/models` | Export field dictionary + price book |

Use `/startupz` for Cloud Run startup and `/livez` for its process responsive
check. The listener starts after artifact/client initialization and bounded
connection preparation. `/startupz` also pings PostgreSQL with a two-second
budget: failure returns 503, so Cloud Run withholds traffic and retries while
the process stays running. Provider preparation failures only log warnings.
`/livez` does not contact the database. Startup sends no prompts and performs no
embedding or HMM inference; it cannot guarantee the latency of first inference.
`/readyz` retains dependency diagnostics.

`POST /v1/messages/count_tokens` is a pre-flight the Anthropic SDK issues
before `/v1/messages`; a hard failure here blocks the call behind it.
A routing model ID, or a request with no reachable Anthropic credential, gets a
local byte-length estimate without an upstream call. Otherwise the upstream
answer is authoritative and gets five seconds, well inside the route timeout.
A transport error, that deadline, or a 408, 429 or 5xx (including 529) is
answered with the local estimate; client-fault statuses such as 400, 401 or
404 are replayed verbatim, and a deployment fault (provider not configured or
not implemented) stays an error.

Routed non-stream responses include `x-router-cost-usd`,
`x-router-cost-input-usd`, `x-router-cost-output-usd`,
`x-router-cache-read-tokens`, and `x-router-cache-creation-tokens`. Streaming
responses cannot carry the final cost in HTTP headers because headers flush
before usage is known; stream clients should read the `weave_cost` object on
the final Anthropic `message_delta` usage event.
