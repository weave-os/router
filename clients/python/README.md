# weave-router-client

Typed Python client for the Weave Router's route-decision API. Answers "which
model would the router pick for this request?" without proxying a completion.

Wraps two endpoints (wire contract:
[`docs/ROUTE_DECISION_API.md`](../../docs/ROUTE_DECISION_API.md)):

- `POST /v1/route` — the decision the proxy would act on.
- `POST /v1/route/preview` — the full HMM policy trace behind that decision.

Neither call spends upstream tokens or dispatches a completion.

## Install

Local development against the router repo:

```bash
cd clients/python && poetry install
```

To consume it from another Poetry project in the monorepo, add a path
dependency:

```toml
weave-router-client = { path = "../router-internal/router/clients/python", develop = true }
```

## Use

```python
from weave_router_client import RouteClient, RouteOptions

body = {
    "model": "claude-sonnet-4-5",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "add a null check to parseConfig"}],
}

with RouteClient("http://localhost:8080", "rk_...") as client:
    decision = client.route(body)
    print(decision.model, decision.provider)  # claude-haiku-4-5 anthropic
```

`body` is an Anthropic Messages request payload — the same JSON you would POST
to `/v1/messages`. Routing reads the envelope, so a body that routes a given way
here routes identically when proxied.

Issue the `rk_` key with `wv mr seed-key`. This is the **router** key, not an
upstream provider key.

### Async

`AsyncRouteClient` has the same contract and errors; prefer it in eval harnesses
that fan many decisions out concurrently.

```python
from weave_router_client import AsyncRouteClient

async with AsyncRouteClient(base_url, api_key) as client:
    decision = await client.route(body)
```

### Per-request overrides

`RouteOptions` fields map to the `x-weave-*` headers the router's middleware
already reads. Unset fields inherit the deployment default; knob bounds are
validated client-side to match the server's.

```python
decision = client.route(
    body,
    options=RouteOptions(strategy="hmm", alpha=0.25, effort="high"),
)
```

Some overrides additionally require the installation to be authorized for policy
header overrides. An unauthorized value is **ignored** by the server rather than
rejected, so the response reflects the deployment default — it does not error.

### Preview

```python
preview = client.preview(body, options=RouteOptions(strategy="hmm"))
print(preview.hmm_state_id, preview.class_probabilities)
print(preview.selected_group, preview.eligible_roster_ids)
for excluded in preview.resolver_exclusions:
    print(excluded.catalog_id, excluded.reason)
```

Preview is HMM-shaped and requires an HMM strategy; a non-HMM strategy is
rejected. Its `schema_version` carries the policy-sidecar contract version
(`policy_router_v1`), which versions independently of the `/v1/route` response's
`router_route_v1`.

## Errors

| Exception | Cause |
| --------- | ----- |
| `UnauthorizedError` | 401 — missing, malformed, or revoked `rk_` token. |
| `InvalidRequestError` | 400/413 — bad body, invalid knobs, preview on a non-HMM strategy, over-limit body. |
| `RoutingFailedError` | 502 (or any other unexpected status) — the strategy errored or its sidecar is unreachable. Also raised on a non-JSON response body. |
| `UnexpectedSchemaError` | The response carried an unrecognized `schema_version`. |

All inherit `RouteClientError`.

The client **does not retry and does not cache**. A `502` means the routing
strategy is unhealthy, not that the request was transiently unlucky; and a
decision is per-request by construction, so a cached one is a wrong one.

## Versioning

`/v1/route` responses carry `schema_version: "router_route_v1"`, and the client
rejects an unrecognized value rather than silently misparsing. To read a newer
server without upgrading the client, pass `check_schema_version=False` and
inspect `decision.schema_version` yourself.

Within a version, fields may be added but never removed or redefined. The
preview models keep unknown fields (available via pydantic's `model_extra`), so
an additive server change stays readable without a client release.

## Test

```bash
poetry run pytest -q
poetry run black --check weave_router_client tests
```

No external services required — `httpx` is mocked with `respx`.
