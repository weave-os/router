# Session cost

`GET /v1/sessions/{session_id}/cost` returns the committed router cost of one
client session. The Codex status hook reads it for the `saved $X.XX` title
clause; scripts and CI can read it with an analytics key.

## Authentication

Send an active routing key (`rk_`) or analytics key (`ra_`) as
`Authorization: Bearer <key>` or `X-API-Key: <key>`. Both key types read the
same data: the sessions of the installation that owns the key. The lookup never
spans installations, so a session id from another installation returns `404`.

The call is read-only. It runs no serving admission, balance check or spend
cap, and an `ra_` key accepted here still cannot call any inference endpoint.

```bash
curl -sS -H "Authorization: Bearer $WEAVE_ANALYTICS_KEY" \
  "$WEAVE_ROUTER_URL/v1/sessions/$SESSION_ID/cost" | jq .
```

## Response

```json
{
  "session_id": "0f5c…",
  "request_count": 12,
  "actual_cost_usd_micros": 250000,
  "actual_cost_usd": 0.25,
  "requested_cost_usd_micros": 570000,
  "requested_cost_usd": 0.57,
  "savings_usd_micros": 320000,
  "savings_usd": 0.32,
  "input_tokens": 1200,
  "output_tokens": 340,
  "cache_creation_tokens": 56,
  "cache_read_tokens": 7800,
  "last_recorded_at": "2026-09-28T19:30:45.123456789Z"
}
```

The `*_usd_micros` integers are authoritative ($1.00 = 1,000,000); the decimal
fields are derived from them for display. *Actual* is what the router's chosen
models cost, and *requested* is what the client's requested model would have
cost. `savings_usd*` is requested minus actual and may be negative.
`last_recorded_at` is RFC 3339 with nanoseconds, in UTC.

Apart from the savings fields, the response matches the Weave public API's
router session cost.

## Errors

Error bodies produced by the endpoint are `{"message": "…", "description": "…"}`,
with `description` included only when it adds detail. Authentication failures are
rejected before the endpoint runs and use the auth boundary's envelope instead:
the worker returns `{"error": "invalid_key"}` (or `{"error": "auth_unavailable"}`
if key verification is unavailable). A managed gateway rejects credentials with a
nested `error` object (`invalid_request_error`; `api_error` if verification is
unavailable). If the gateway forwards a request that the worker then rejects,
the worker's envelope is passed through. Authentication failures carry no
rate-limit headers.

| Status | When |
| --- | --- |
| `400` | The session id is empty or longer than 128 bytes. |
| `401` | The key is missing, unknown, revoked, has a disabled or incomplete personal-key subject, or is neither a routing nor an analytics key. |
| `404` | No committed telemetry for this session in the key's installation, whether it does not exist, belongs elsewhere or has not been recorded yet. |
| `429` | The installation's rate limit is exhausted. |
| `503` | This deployment has no telemetry storage, or key verification is temporarily unavailable. |
| `500` | Unexpected failure. |

Telemetry commits after a request finishes, so a session's newest turn can take
a moment to appear.

## Rate limit

Each installation nominally gets 500 requests per minute, with a burst of 500,
shared by all of its keys on each router replica. The replicas have independent
buckets, so the aggregate allowance grows with replica count. Buckets are held
in a 1,024-entry LRU per replica; evicting an installation drops its refill
state, so a later request starts with a full burst. Under enough installation
churn, the nominal per-installation allowance can therefore be exceeded
temporarily. Every authenticated response carries `X-RateLimit-Limit`,
`X-RateLimit-Remaining`, `X-RateLimit-Used` and `X-RateLimit-Reset` (Unix seconds
when the bucket is full again). A `429` also carries `Retry-After` in whole
seconds.
