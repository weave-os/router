# Route decision API

Two endpoints return a routing decision without proxying anything upstream.
They cost no upstream tokens and never dispatch a completion, which makes them
the supported way to ask "which model would the router pick for this request?"
from outside the proxy path — dashboards, offline eval harnesses, and the Go and
Python route SDKs all consume these.

Both take an **Anthropic Messages request body**: the same JSON you would `POST`
to `/v1/messages`. Routing reads the envelope (messages, tools, `max_tokens`,
model hint), so a body that routes well here routes identically when proxied.

## Authentication

Both endpoints require a router key as a bearer token:

```
Authorization: Bearer rk_...
```

Issue one with `wv mr seed-key` for local and staging work. This is the **router**
key (`rk_`), not an upstream provider key (`sk-...`).

## `POST /v1/route`

Returns the decision the proxy would act on.

```bash
curl -sS http://localhost:8080/v1/route \
  -H "Authorization: Bearer rk_..." \
  -d '{"model":"claude-sonnet-4-5","max_tokens":256,
       "messages":[{"role":"user","content":"add a null check to parseConfig"}]}'
```

```json
{
  "schema_version": "router_route_v1",
  "model": "claude-haiku-4-5",
  "provider": "anthropic",
  "reason": "cheap_and_cheerful"
}
```

| Field | Meaning |
| ----- | ------- |
| `schema_version` | Wire-contract version. Pin on it; see [Versioning](#versioning). |
| `model` | Catalog model id the router selected. |
| `provider` | Provider that would serve it (`anthropic`, `openai`, `google`, …). |
| `reason` | Short decision tag, for logs and debugging. Not a stable enum — do not branch on it. |

Works under every strategy, including the legacy `cluster` scorer.

## `POST /v1/route/preview`

Returns the full decision trace instead of just the outcome: classifier state,
class probabilities, ranked fallback groups, the eligible roster, and the
candidate set with per-candidate exclusion diagnostics. Use it to explain *why*
a decision happened; use `/v1/route` when you only need the outcome.

Preview is **HMM-shaped** — it reports `hmm_state_id` and `class_probabilities`,
which only exist under an HMM strategy. A request on a non-HMM strategy returns
`400`. Send `x-weave-router-strategy: hmm` if the deployment's default is not
already HMM.

```bash
curl -sS http://localhost:8080/v1/route/preview \
  -H "Authorization: Bearer rk_..." \
  -H "x-weave-router-strategy: hmm" \
  -d '{"model":"claude-sonnet-4-5","max_tokens":256,
       "messages":[{"role":"user","content":"add a null check to parseConfig"}]}'
```

The response is `policy.PreviewResult`; its `schema_version` carries the
policy-sidecar contract version (`policy_router_v1`), not
`router_route_v1`. The two endpoints version independently.

Preview evaluates the policy without recording serving state: no outcome
report, no session pin write, no billing debit.

## Request shaping headers

Both endpoints sit behind the same request-shaping middleware as the proxy, so
the same headers apply. All are optional.

| Header | Effect |
| ------ | ------ |
| `x-weave-router-strategy` | Pick the routing strategy (`hmm`, `cluster`, …). |
| `x-weave-cluster-version` | Pin a cluster artifact version (`v0.70`). Legacy `cluster` strategy only. |
| `x-weave-effort` | Force a reasoning-effort tier. |
| `x-weave-embed-only-user-message` | Embed only user-role text (`true`) or the concatenated action stream (`false`). |
| `x-weave-routing-alpha` &nbsp;·&nbsp; `-speed-weight` &nbsp;·&nbsp; `-output-cost-ratio` &nbsp;·&nbsp; `-expected-output-tokens` &nbsp;·&nbsp; `-per-model-verbosity` | Per-request routing knobs. Invalid values return `400`. |

Some headers additionally require the installation to be authorized for policy
header overrides; unauthorized values are ignored rather than rejected, so the
response reflects the deployment default.

## Errors

| Status | Cause |
| ------ | ----- |
| `400` | Body is not a JSON object, invalid routing knobs, or preview requested on a non-HMM strategy. |
| `401` | Missing, malformed, or revoked `rk_` bearer token. |
| `413` | Request body exceeds the 10 MiB body limit. |
| `502` | Routing failed — the strategy errored, or its policy sidecar is unreachable or returned no valid selection. |

A `502` here means the routing strategy is unhealthy, not that the request was
transiently unlucky: there is no silent fallback to a default model by design, so
an unavailable policy surfaces as an error rather than a quietly degraded
decision. **Do not blanket-retry**; surface the failure instead.

Error bodies use the Anthropic error envelope:

```json
{"type": "error", "error": {"type": "invalid_request_error", "message": "..."}}
```

## Versioning

`/v1/route` responses carry `schema_version: "router_route_v1"`. Within a
version, fields may be **added** but never removed or redefined; a removal or
semantic change bumps the version. Clients should read the field and fail loudly
on an unrecognized value rather than silently misparsing.

`/v1/route/preview` versions separately, through the policy-sidecar contract
version in its own `schema_version`.

## Clients

A typed Python client for both endpoints ships in this repo at
[`clients/python/`](../clients/python) (`weave_router_client`). It handles header
shaping, the schema-version guard, and status-to-typed-error mapping, in sync and
async flavors.

## Choosing between these and the catalog endpoints

These endpoints answer "what would you pick for *this request*". To ask what the
router *could* pick at all — the deployed roster, pricing, the projected model
mix across the quality/price dial — use the unauthenticated catalog endpoints
(`GET /v1/router/models`, `/v1/router/routing-distribution`,
`/v1/router/policies`, `/v1/router/hmm-roster`) instead. Those are cacheable;
decisions are per-request by construction and must not be cached.
