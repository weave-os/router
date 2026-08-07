# Test report — PR #879: Anthropic-compatible gateway provider (Bearer) + per-installation content-capture ceiling

- **Revision under test:** `221c3b4` on `devin/1786108000-anthropic-gateway-provider` (PR https://github.com/workweave/router/pull/879)
- **Method:** runtime only, against the router built from this working tree and run in docker compose. Shell evidence (no recording — this PR has no GUI surface; the content-capture control is admin-API-only and the gateway is a data-plane path).
- **Result:** all 7 test groups passed. 1 assertion is reported **untested-at-runtime** by design (see T3), and 1 pre-existing product gap is re-flagged (see T6).
- **No real vendor endpoint or credential was contacted.** Both upstreams are local fakes, and `api.anthropic.com` is pinned to `127.0.0.1` inside the server container so any accidental fallback would fail loudly on-box rather than leave it.

---

## Harness

Router in docker compose against a **wiped** Postgres volume, so the renamed `0045` migration applies from scratch. Router on `:8080`, Postgres on `:5433`.

`.env.local` (gitignored):

```
ANTHROPIC_GATEWAY_BASE_URL=http://host.docker.internal:8099
ANTHROPIC_GATEWAY_TOKEN=gw-DEPLOY-7f3a9c2b1e
WV_CAPTURE_CONTENT=full
OTEL_EXPORTER_OTLP_ENDPOINT=http://host.docker.internal:4318
ROUTER_ADMIN_PASSWORD=testadminpw
ROUTER_COOKIE_INSECURE=true
```

`ANTHROPIC_API_KEY` is deliberately **unset**: Anthropic then becomes passthrough-only and is dropped from the enabled set on router-key-authed requests (`internal/proxy/service.go`), so `resolveProviderFor` falls through to the trailing `anthropic_gateway` catalog binding. That is how every turn below reaches the gateway.

`docker-compose.override.yml`:

```yaml
services:
  pubsub-emulator:
    ports: !reset []
  server:
    extra_hosts:
      - "host.docker.internal:host-gateway"
      - "api.anthropic.com:127.0.0.1"   # blackhole the real endpoint (T7 safety)
```

Verified live on the container:

```
$ docker inspect router-server-1 --format '{{json .HostConfig.ExtraHosts}}'
["host.docker.internal:host-gateway","api.anthropic.com:127.0.0.1"]
```

Fakes (temporary, in `/tmp`):
- `/tmp/fake_cortex.py` on `:8099` (tag `DEPLOY`) and `:8098` (tag `BYOK`) — serves the Anthropic Messages shape (JSON + SSE) at **`/v1/messages` at the root**, 404s any other path, and appends `{path, headers, body}` per request to `/tmp/cortex_{deploy,byok}.jsonl`.
- `/tmp/fake_otlp.py` on `:4318` — stores raw OTLP `/v1/logs` and `/v1/traces` protobuf bodies under `/tmp/otlp/`.

Router key `rk_wqqS7dtSh7WI5cbGFFxZBVoM` seeded onto `__router_admin__` — the same installation the admin cookie resolves to, so an admin override actually applies to these data-plane turns. Every turn carries a unique `SENTINEL-*` so a semantic-cache hit cannot masquerade as an upstream call and each OTLP record maps to exactly one turn.

---

## T1 — Migration `0045` up → down → up  ✅ PASSED

Baseline on the fresh DB:

```
column_content_capture_mode=PRESENT (text)
provider_check_has_anthropic_gateway=true
version=45 dirty=false
```

Down:

```
$ docker compose run --rm --entrypoint migrate migrate ... down 1
2026/08/07 09:30:55 Read and execute 45/d anthropic-gateway-and-capture-ceiling
2026/08/07 09:30:55 Finished 45/d anthropic-gateway-and-capture-ceiling (read 3.291974ms, ran 4.593923ms)

column_content_capture_mode=ABSENT
provider_check_has_anthropic_gateway=false
version=44 dirty=false
```

Re-up:

```
45/u anthropic-gateway-and-capture-ceiling (5.979ms)

column_content_capture_mode=PRESENT (text)
provider_check_has_anthropic_gateway=true
version=45 dirty=false

CHECK (((provider)::text = ANY ((ARRAY['anthropic', 'openai', 'google', 'openrouter',
 'fireworks', 'bedrock', 'makora', 'together', 'xai', 'anthropic_gateway'])::text[])))
```

The down genuinely reverses both halves (column dropped **and** CHECK narrowed), and the re-up restores both. No errors, never dirty.

## T2 — Gateway wire contract  ✅ PASSED

**Boot line — base URL logged verbatim, no path suffix, no token:**

```
{"severity":"INFO","message":"Anthropic gateway provider enabled","base_url":"http://host.docker.internal:8099"}
```

This is the discriminating evidence that the old `/api/v2/cortex` rewriting is gone: the configured value is used as-is.

**Token never logged:**

```
$ docker compose logs server | grep -c "gw-DEPLOY-7f3a9c2b1e"
0
```

**Both turns as recorded by the fake upstream** (non-streaming, then streaming):

```
path=/v1/messages   host=host.docker.internal:8099   stream=False  model=claude-haiku-4-5
   authorization= Bearer gw-DEPLOY-7f3a9c2b1e
   x-api-key present? False | header keys: ['accept', 'accept-encoding', 'anthropic-version',
                                            'authorization', 'content-length', 'content-type',
                                            'host', 'user-agent']
path=/v1/messages   host=host.docker.internal:8099   stream=True   model=claude-opus-5
   authorization= Bearer gw-DEPLOY-7f3a9c2b1e
   x-api-key present? False | header keys: [same as above]
```

Exact path `/v1/messages`, Bearer auth, and no `x-api-key` anywhere in the upstream header set.

**Client responses:**

```
X-Router-Decision: cluster:v0.75 top_p=[4,9,11,15] model=claude-haiku-4-5 provider=anthropic_gateway
X-Router-Model: claude-haiku-4-5
X-Router-Provider: anthropic_gateway
```

Non-streaming body reached the client intact with `GATEWAY-FAKE-DEPLOY-REPLY…`.

Streaming relayed the full event sequence, not just an opened stream:

```
      1 event: message_start
      2 event: content_block_start
      3 event: content_block_delta
      2 event: content_block_stop
      1 event: message_delta
      1 event: message_stop

reconstructed from text_delta:
'✦ **Weave Router** → claude-opus-5 · best pick for this turn\n\nGATEWAY-FAKE-DEPLOY-REPLY served by the fake Anthropic-compatible gateway.'
```

The concatenated deltas reconstruct the fake's complete sentence (the router's own decision prelude precedes it, which is existing behavior).

## T3 — Inbound caller credential is not relayed upstream  ✅ PASSED (observable property) / ⚠️ one branch untested-at-runtime

One router-keyed turn was sent that **also** carried `authorization: Bearer sk-ant-INBOUND-LEAK-DO-NOT-FORWARD`. The fake recorded:

```
last request authorization: Bearer gw-DEPLOY-7f3a9c2b1e
requests containing the inbound leak literal: 0
inbound literal anywhere in last body? False
```

The gateway received the configured gateway credential; the caller's inbound value never left the router.

**Untested at runtime (by design, not a gap):** the literal branch in `setAuth` where an *empty* Bearer client sends no credential at all cannot be reached through normal routing — `anthropic_gateway` can only enter the enabled provider set via a deployment token or a non-empty BYOK credential; `passthroughEligibleProviders` holds only `{anthropic, openai}`, and BYOK rows with empty plaintext are discarded (`internal/proxy/service.go`). That branch has unit coverage in `internal/providers/anthropic/bearer_auth_test.go` (`TestBearerScheme_DoesNotRelayInboundAnthropicAuth`). Useful reviewer context: the runtime-reachable surface is exactly the one proven above.

## T4 — Content-capture matrix under a `full` deployment  ✅ PASSED (8/8)

| # | Action | Observed | Result |
|---|---|---|---|
| 4a | `GET` | `{"deployment":"full","installation":null,"effective":"full"}` | ✅ |
| 4b | turn `SENTINEL-FULL-A` | `router.call=1 io.request_body=1 io.response_body=1 RAW_SENTINEL=1 RAW_REPLY=1` | ✅ |
| 4c | `PUT {"mode":"off"}` | `200` → `{"full","off","off"}` | ✅ |
| 4d | turn `SENTINEL-OFF-B` | **new OTLP log payloads: 0** · new trace payloads: 2 | ✅ |
| 4e | `PUT {"mode":"hashed"}` + turn | `200` → `{"full","hashed","hashed"}`; hashes only | ✅ |
| 4f | `PUT {"mode":null}` + turn | `200` → `{"full",null,"full"}`; full bodies back | ✅ |
| 4g | `PUT {"mode":"bogus"}` | `400`, override unchanged | ✅ |
| 4h | admin route without cookie | `401 admin_session_required` | ✅ |

**4d — the ceiling is enforced on the data path, not merely stored.** The turn still succeeded end-to-end (`X-Router-Provider: anthropic_gateway`, reply reached the client) yet emitted **zero** log payloads, while **2 trace payloads still arrived in the same window** — the collector-live control proving the absence is the feature rather than a dead sink.

**4e — `hashed` really strips raw text**, extracted from the raw OTLP protobuf:

```
io.request_sha256    -> 95057a150bf725d2be05c2a169038fc80e6c35c538209596c96460f7a2fb7ed2 (64-hex)
io.response_sha256   -> 5e7eebdecbcb3a4ae0b1c6bc597826f10e05d3160577784e371b1424e055d57a (64-hex)
io.request_bytes     -> present
io.response_bytes    -> present
io.truncated         -> present

raw prompt sentinel present?  False
raw reply marker present?     False
io.request_body key present?  False
```

**4f — clearing with JSON `null` works through to the DB:**

```
PUT {"mode":null} -> {"deployment":"full","installation":null,"effective":"full"} http=200
GET readback      -> {"deployment":"full","installation":null,"effective":"full"}
DB: Dashboard -> NULL
next turn: router.call=1 io.request_body=1 io.response_body=1 RAW_SENTINEL=1
```

**4g — invalid modes rejected, and the bad write does not land.** Three variants tried:

```
{"mode":"bogus"} -> {"error":"auth: invalid content capture mode: \"bogus\""} http=400
{"mode":"FULL"}  -> {"error":"auth: invalid content capture mode: \"FULL\""}  http=400
{"mode":""}      -> {"error":"auth: invalid content capture mode: \"\""}      http=400
GET afterwards   -> {"deployment":"full","installation":null,"effective":"full"}
```

Note `"FULL"` is correctly rejected rather than case-folded — worth knowing, but it matches the documented lowercase-only contract.

**4h — the admin route is not reachable with a data-plane key** (guards against a leaked `rk_` widening capture):

```
PUT with  authorization: Bearer rk_…   -> {"error":"admin_session_required"} http=401
PUT with  X-Weave-Router-Key: rk_…     -> {"error":"admin_session_required"} http=401
GET with  no auth at all               -> {"error":"admin_session_required"} http=401
state afterwards (admin cookie)        -> {"deployment":"full","installation":null,"effective":"full"}
```

## T5 — `full` installation under a `hashed` deployment stays `hashed`  ✅ PASSED

Installation override set to `full`, then the server restarted with `WV_CAPTURE_CONTENT=hashed`:

```
{"message":"Router content capture configured","mode":"hashed","max_bytes":1048576}

GET -> {"deployment":"hashed","installation":"full","effective":"hashed"}

turn SENTINEL-CEIL-E:
  router.call=1 io.request_body=0 io.response_body=0
  io.request_sha256=1 io.response_sha256=1 RAW_SENTINEL=0 RAW_REPLY=0
```

The override cannot widen the deployment ceiling — not in the reported `effective`, and not on the data path either.

## T6 — BYOK token *and* BYOK base URL override the deployment values  ✅ PASSED

Restart with the deployment token unset produced the BYOK-only registration branch (and **no** `enabled` line):

```
{"message":"Anthropic gateway provider registered (BYOK only — set ANTHROPIC_GATEWAY_TOKEN and ANTHROPIC_GATEWAY_BASE_URL for deployment-level use)"}
```

Row created through the admin API — this is what exercises the widened CHECK from `0045`:

```
POST /admin/v1/provider-keys {"provider":"anthropic_gateway","key":"gw-BYOK-11d4e6f0a2"}
-> {"id":"65925930-…","provider":"anthropic_gateway","key_prefix":"gw-BYOK-",…} http=201
```

Then `base_url` set via psql (see the gap note below), and the server restarted **with** the deployment token restored, so both credentials are live simultaneously. One turn:

```
--- DEPLOY fake (:8099) request count: 0
--- BYOK fake  (:8098) request count:  1

path= /v1/messages
host= host.docker.internal:8098
authorization= Bearer gw-BYOK-11d4e6f0a2
x-api-key present? False
deployment token literal anywhere in request? False
```

Client received `GATEWAY-FAKE-BYOK-REPLY…`. Both BYOK values won: the token *and* the base URL.

**Product gap re-flagged (pre-existing, accepted out of scope by the lead):** `POST /admin/v1/provider-keys` passes `nil` for `base_url` (`internal/api/admin/keys.go`) and 409s while the provider's env var is set, so the row's `base_url` had to be set with direct SQL:

```sql
UPDATE router.model_router_external_api_keys
SET base_url='http://host.docker.internal:8098'
WHERE provider='anthropic_gateway' AND deleted_at IS NULL;
```

This is documented as a workaround, not a test shortcut. It matters slightly more for this provider than for others: with the path rewriting removed, the **entire** endpoint (host *and* path) now lives in `base_url`, so the BYOK-only mode the provider advertises is not configurable through the admin API or dashboard today — it needs DB access. Worth a follow-up decision; it does not affect the correctness of anything in this PR.

## T7 — Empty base URL must not fall back to `api.anthropic.com`  ✅ PASSED

Deployment base URL and token both unset, BYOK row's `base_url` set to `NULL` — so the effective base URL resolves to `""`. One turn:

```
http=502
{"error":{"message":"Upstream call failed.","type":"api_error"},"type":"error"}
X-Router-Provider: anthropic_gateway
--- fake request counts: deploy=0 byok=0
```

Server log:

```
"provider":"anthropic_gateway", "err":"upstream call: Post \"/v1/messages\": unsupported protocol scheme \"\""
```

The request failed on an **empty URL**, exactly as intended. Because `api.anthropic.com` resolves to `127.0.0.1` inside the container, a fallback would instead have surfaced as a connection error naming `api.anthropic.com:443` — that never appeared. Across the entire server log there is exactly **one** occurrence of that hostname, and it is the unrelated native-Anthropic passthrough boot line:

```
$ docker compose logs server | grep -c "api.anthropic.com"
1
$ docker compose logs server | grep "api.anthropic.com"
{"message":"Anthropic provider enabled (client auth passthrough)","base_url":"https://api.anthropic.com"}
```

Two observations worth noting for reviewers (neither is a defect in this PR):
- The router retried the same binding twice on this error (`same_binding_retry: 1`, then `2`) before failing, i.e. an empty base URL is classified as a *transient* error. Harmless here, but it means a misconfigured gateway costs 3 attempts per request rather than failing fast.
- The final client status is a generic `502 Upstream call failed.`; the operator has to read the server log to learn the base URL is empty. Fine, just not self-diagnosing.

---

## Restoration

The harness was returned to its deployment-configured state after T7 and re-verified live (BYOK row soft-deleted, env restored):

```
final smoke http=200
restored path= /v1/messages   auth= Bearer gw-DEPLOY-7f3a9c2b1e
```

## Summary

| Test | Assertion | Result |
|---|---|---|
| T1 | `0045` up → down → up round-trips (column + CHECK both reversed and restored, never dirty) | ✅ passed |
| T2 | Boot logs base URL verbatim with no path suffix and no token | ✅ passed |
| T2 | Token literal appears 0 times in the server log | ✅ passed |
| T2 | Non-streaming request hits fake at exactly `/v1/messages`, `Bearer …`, no `x-api-key` | ✅ passed |
| T2 | SSE request hits fake the same way; full event sequence + complete text relayed to client | ✅ passed |
| T2 | Responses carry `X-Router-Provider: anthropic_gateway` | ✅ passed |
| T3 | Inbound caller `authorization` never reaches the gateway; gateway token used instead | ✅ passed |
| T3 | Literal no-credential Bearer branch | ⚠️ untested at runtime (unreachable by design; unit-covered) |
| T4a | `GET` reports `full`/`null`/`full` | ✅ passed |
| T4b | `full` turn emits `router.call` with full bodies + raw prompt | ✅ passed |
| T4c/d | `off` → zero log records emitted, traces still flowing (collector-live control) | ✅ passed |
| T4e | `hashed` → 64-hex request/response hashes, zero raw text, no body attrs | ✅ passed |
| T4f | `{"mode":null}` clears to DB NULL; full bodies return | ✅ passed |
| T4g | Invalid mode → 400, override unchanged (3 variants) | ✅ passed |
| T4h | Admin route rejects `rk_` bearer / router-key header / no auth → 401 | ✅ passed |
| T5 | `full` installation under `hashed` deployment → `effective: hashed`, hashes only on the wire | ✅ passed |
| T6 | BYOK row accepted by the widened CHECK via admin API (201) | ✅ passed |
| T6 | BYOK-only registration branch logged when token unset | ✅ passed |
| T6 | BYOK token **and** base URL beat the deployment values; DEPLOY fake sees 0 requests | ✅ passed |
| T7 | Empty base URL fails locally on `unsupported protocol scheme ""`, no `api.anthropic.com` fallback | ✅ passed |

Nothing in the runtime matrix contradicted the intended behavior. The only non-passing line is the deliberately unreachable branch in T3, and the only open item is the pre-existing BYOK `base_url` admin-API gap in T6.
