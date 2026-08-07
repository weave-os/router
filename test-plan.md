# Test plan — PR #879 (Anthropic-compatible gateway provider + per-installation content-capture ceiling)

Revision under test: `221c3b4` on `devin/1786108000-anthropic-gateway-provider`. Runtime-only; `make check` is out of scope.

## Harness (stood up during setup — not a test step)

- Router built from this working tree and run in docker compose against a **wiped** Postgres volume, so the renamed `0045_anthropic-gateway-and-capture-ceiling` applies from scratch. Host ports: router 8080, Postgres 5433.
- `.env.local` (gitignored): `ANTHROPIC_GATEWAY_BASE_URL=http://host.docker.internal:8099`, `ANTHROPIC_GATEWAY_TOKEN=gw-DEPLOY-7f3a9c2b1e`, `WV_CAPTURE_CONTENT=full`, `OTEL_EXPORTER_OTLP_ENDPOINT=http://host.docker.internal:4318`, `ROUTER_ADMIN_PASSWORD`, `ROUTER_COOKIE_INSECURE=true`. **`ANTHROPIC_API_KEY` deliberately unset**, so Anthropic is passthrough-only and is dropped from the enabled set on router-key-authed requests (`internal/proxy/service.go:3833-3843`); `resolveProviderFor` (`internal/router/cluster/scorer.go:348`) then falls through to the trailing `anthropic_gateway` binding (`internal/router/catalog/catalog.go:164-225`).
- `docker-compose.override.yml`: `host.docker.internal` → host gateway, and **`api.anthropic.com` → 127.0.0.1 inside the container**, so any fallback to Anthropic's real endpoint surfaces as a loud connection error and cannot leave the box.
- `/tmp/fake_cortex.py` on :8099 (tag `DEPLOY`) and :8098 (tag `BYOK`): Anthropic Messages shape (JSON + SSE) served at **`/v1/messages` at the root**, 404 + recorded for any other path, every request appended as `{path, headers, body}` to `/tmp/cortex_{deploy,byok}.jsonl`.
- `/tmp/fake_otlp.py` on :4318: stores raw OTLP `/v1/logs` and `/v1/traces` protobuf bodies under `/tmp/otlp/`.
- Router key `rk_wqqS7dtSh7WI5cbGFFxZBVoM` seeded onto `__router_admin__` — the same installation the admin cookie resolves to, so an admin override applies to these data-plane turns. Admin cookie jar `/tmp/cookies.txt`.
- Every turn carries a unique `SENTINEL-*` string so semantic-cache hits can't masquerade as upstream calls and each OTLP record maps to exactly one turn.

## T1 — Migration 0045 up → down → up

1. Inspect schema at the fresh baseline; 2. `migrate down 1`; 3. `migrate up`; re-inspect after each.

Pass criteria:
- Baseline: `content_capture_mode` present on `router.model_router_installations`; `pg_get_constraintdef(model_router_external_api_keys_provider_check)` **contains `'anthropic_gateway'`**; `schema_migrations` = `45`, `dirty=false`.
- After down: column **absent**; constraint definition **does not** contain `anthropic_gateway`; version `44`, `dirty=false`.
- After up: identical to baseline. No migrate errors in either direction.

Broken-implementation signal: a no-op/mismatched down leaves the column or the widened CHECK behind at step 2.

## T2 — Gateway wire contract: exact URL, Bearer, no `x-api-key`, response intact

1. Truncate `/tmp/cortex_deploy.jsonl`; 2. non-streaming `POST /v1/messages` (`SENTINEL-NONSTREAM`); 3. streaming (`"stream": true`, `SENTINEL-STREAM`, `curl -N`); 4. grep the whole server log for the token literal.

Pass criteria:
- Boot log: `Anthropic gateway provider enabled base_url=http://host.docker.internal:8099` — **verbatim**, with no `/api/v2/cortex` (or any other) suffix appended, and no token substring anywhere on the line.
- Both requests recorded by the fake at `path == "/v1/messages"` exactly.
- Both carry `authorization: Bearer gw-DEPLOY-7f3a9c2b1e`; **no `x-api-key`** key present in the recorded header map.
- Both client responses carry `X-Router-Provider: anthropic_gateway`.
- Non-streaming client body contains `GATEWAY-FAKE-DEPLOY-REPLY`.
- Streaming: client receives `event: message_start`, ≥1 `content_block_delta`, `event: message_stop`, and the concatenated `text_delta`s reconstruct the full sentence `GATEWAY-FAKE-DEPLOY-REPLY served by the fake Anthropic-compatible gateway.` (proves relay, not just stream open).
- `docker compose logs server | grep -c gw-DEPLOY-7f3a9c2b1e` = **0**.

Broken-implementation signal: a re-added path rewriter shows up as a 404 at `/api/v2/cortex/v1/messages`; a missing `AuthBearer` shows up as `x-api-key` on the recorded request.

## T3 — Inbound caller credential is not relayed to the gateway

Send one router-keyed turn (`SENTINEL-LEAK`) that also carries `authorization: Bearer sk-ant-INBOUND-LEAK-DO-NOT-FORWARD`.

Pass criteria:
- The fake's recorded `authorization` is exactly `Bearer gw-DEPLOY-7f3a9c2b1e`.
- The literal `sk-ant-INBOUND-LEAK-DO-NOT-FORWARD` appears in **zero** requests the fake recorded (searched across the whole JSONL, headers and body).

Scope note: the literal no-credential Bearer branch (`client.go` `setAuth` returning with no header set) is **not reachable at runtime** — `anthropic_gateway` cannot enter the enabled set without a credential, since `passthroughEligibleProviders` holds only `{anthropic, openai}` (`cmd/router/main.go:528-533`) and BYOK rows with empty plaintext are dropped (`internal/proxy/service.go:3791-3797`). It will be reported untested-at-runtime, referencing `internal/providers/anthropic/bearer_auth_test.go`.

## T4 — Content-capture matrix under a `full` deployment

Admin calls use the admin cookie; each turn uses a fresh sentinel; after each turn wait for the OTLP batch and inspect raw payloads under `/tmp/otlp/logs/`.

| # | Action | Pass criteria |
|---|---|---|
| 4a | `GET /admin/v1/content-capture` | exactly `{"deployment":"full","installation":null,"effective":"full"}` |
| 4b | turn `SENTINEL-FULL-A` | new `router.call` record containing `io.request_body` **and** the literal `SENTINEL-FULL-A` |
| 4c | `PUT {"mode":"off"}` | 200, `{"deployment":"full","installation":"off","effective":"off"}` |
| 4d | turn `SENTINEL-OFF-B` | **zero** new `router.call` records and zero payloads containing the sentinel; **control:** new `/v1/traces` payloads still arrive in the same window, proving the sink is live and the absence is the feature |
| 4e | `PUT {"mode":"hashed"}` + turn `SENTINEL-HASH-C` | 200 `{"full","hashed","hashed"}`; new `router.call` carries 64-hex `io.request_sha256` + `io.response_sha256` and contains **neither** `io.request_body` **nor** the sentinel **nor** `GATEWAY-FAKE` |
| 4f | `PUT {"mode":null}` + turn `SENTINEL-FULL-D` | 200 and `GET` readback both `{"full",null,"full"}`; new `router.call` contains `io.request_body` and the literal `SENTINEL-FULL-D` |
| 4g | `PUT {"mode":"bogus"}` | HTTP **400**; follow-up `GET` still reports `installation:null` (bad write did not land) |
| 4h | `PUT {"mode":"off"}` with the `rk_` key as bearer and **no** cookie | HTTP 401/403 — data-plane key must not reach the admin route |

Broken-implementation signal: 4d proves the ceiling is enforced on the data path rather than merely stored; 4e proves `hashed` strips raw text instead of silently staying `full`.

## T5 — `full` installation under a `hashed` deployment stays `hashed`

Set installation `full`, restart with `WV_CAPTURE_CONTENT=hashed`, `GET`, then run turn `SENTINEL-CEIL-E`.

Pass criteria:
- `GET` → `{"deployment":"hashed","installation":"full","effective":"hashed"}`.
- The turn's `router.call` contains `io.request_sha256` and **not** the literal `SENTINEL-CEIL-E` — the override cannot widen capture on the data path either.

## T6 — BYOK token and base URL override the deployment values

`POST /admin/v1/provider-keys` cannot set `base_url` (passes `nil`, `internal/api/admin/keys.go:226`) and 409s while the env token is set (`keys.go:222`), so the row is created via the admin API with the token unset and its `base_url` is then set with `psql`. Reported as a product gap, not a shortcut.

1. Restart with `ANTHROPIC_GATEWAY_TOKEN` unset (base URL still :8099). 2. `POST /admin/v1/provider-keys {"provider":"anthropic_gateway","key":"gw-BYOK-11d4e6f0a2"}`. 3. `psql`: set that row's `base_url='http://host.docker.internal:8098'`. 4. Restart **with** the deployment token set again. 5. Truncate both fake logs; run turn `SENTINEL-BYOK-F`.

Pass criteria:
- Step 1 boot log: `Anthropic gateway provider registered (BYOK only …)` and **no** `Anthropic gateway provider enabled` line.
- Step 2: HTTP 2xx with `"provider":"anthropic_gateway"` — the widened CHECK accepts the row (pre-0045 schema would fail the INSERT).
- Step 5: the request is recorded by the **BYOK** fake (:8098) at `path == "/v1/messages"` with `authorization: Bearer gw-BYOK-11d4e6f0a2`, no `x-api-key`; the DEPLOY fake (:8099) records **zero** requests; the deployment token literal appears in **zero** BYOK-fake requests.

Broken-implementation signal: deployment values winning shows up as traffic on :8099 or a `gw-DEPLOY-…` bearer on :8098.

## T7 — Bearer client with an empty base URL must not fall back to api.anthropic.com

Restart with `ANTHROPIC_GATEWAY_BASE_URL` **and** `ANTHROPIC_GATEWAY_TOKEN` unset, leaving only the BYOK row with `base_url` cleared to NULL (via `psql`) — i.e. `effectiveBaseURL` resolves to `""`. Run turn `SENTINEL-NOBASE-G`.

Pass criteria:
- The client gets an error (non-200), and the router's error/log text indicates an **unusable/empty upstream URL** (e.g. `unsupported protocol scheme ""`).
- The server log contains **no** mention of `api.anthropic.com` for this turn, and neither fake records a request.
- Since `api.anthropic.com` resolves to 127.0.0.1 inside the container, a fallback would instead appear as a connection error naming `api.anthropic.com:443` — its absence is the discriminating evidence.

## Out of scope

- Real vendor gateway endpoint/token (none available; the wire contract is proven against the fake's recorded requests).
- Unit tests / `make check` / `genprices` (lead confirmed green).
- Catalog price values and RL roster mapping beyond the provider resolution that T2/T6 already exercise.
