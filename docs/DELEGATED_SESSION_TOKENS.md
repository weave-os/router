# Delegated session tokens

Session token verification is always on. A deployment accepts an `rs_` token
only when its signing key is registered as a session issuer, and the issuer
endpoints require `ROUTER_INTERNAL_SERVICE_TOKEN`.

A delegated session token lets a trusted service give each of its client
sessions a short-lived router credential without minting a key row per
session. The service holds an Ed25519 private key. Its public key is registered
on an installation as a **session issuer**, a `model_router_api_keys` row with
scope `session_issuer`. For each session the service signs a standard JWT
locally.

## Token

The bearer is `rs_` followed by a compact JWS. The prefix keeps the token apart
from provider JWTs and makes the upstream header scrubbers drop it.

| Part | Value |
| --- | --- |
| Format | JWT access token, RFC 7519 and RFC 9068 |
| Header `alg` | `EdDSA` (Ed25519, RFC 8037); nothing else is accepted |
| Header `typ` | `at+jwt` |
| Header `kid` | RFC 7638 SHA-256 thumbprint of the issuer JWK, base64url |
| `iss` | `session-issuer` |
| `aud` | `weave-router` |
| `https://weave.ai/issuer_id` | Required ID returned when registering the issuer; binds the token to that registration generation |
| `sub` | Session ID |
| `iat`, `exp` | Required; `exp - iat` is at most 2 hours; issuers refresh tokens for longer sessions |
| `jti` | Optional token ID |
| `https://weave.ai/task` | Optional task ID |
| `https://weave.ai/caps` | Optional `{session_usd_micros, task_usd_micros}` |

Verification uses `github.com/golang-jwt/jwt/v5` with the RFC 8725 checks:
a fixed algorithm, issuer, audience, required expiry and issued-at, and 30
seconds of clock leeway. Cap values must be positive, and a task cap requires a
task claim. Session and task IDs cannot contain NUL. A token's issuer ID must
match the active registration, so revoking and re-registering the same signing
key does not revive old tokens.

A request authenticates as the issuer key. Spend, attribution, BYOK and cluster
lists are the issuer's; the session and task IDs travel on the request context
as `auth.DelegatedSession`.

## Spend caps

Each billed request adds its cost to `router.delegated_token_spend`, keyed by
issuer, scope (`session` or `task`) and ID, in the same transaction as the
credit debit. Before proxying, the router reads the current totals fresh from
Postgres (no cache) and rejects a request whose session or task total has
reached its cap with 402 `key_spend_cap_reached` and the exhausted `scope`. A
failed read returns 503. Shadow evals, allowance and subscription traffic keep
their existing exemptions. Caps are checked before a request and spend is
recorded after it settles, so a cap can be overshot by the cost of every request
in flight when it is reached. A session that sends requests one at a time
overshoots by at most one request.

Tokens carrying caps are rejected when billing is not configured, since that
worker cannot enforce or record their limits.

## Issuers

The control plane registers and revokes issuers through the internal API, which
is mounted only when `ROUTER_INTERNAL_SERVICE_TOKEN` is set.

- `POST /internal/v1/session-issuers` with
  `{"installation_id", "name", "public_jwk"}` returns `{"issuer_id", "kid"}`.
  Only an OKP Ed25519 public JWK is accepted. Registering the same key again
  returns the same issuer; a key already registered to another installation
  returns 409.
- `DELETE /internal/v1/session-issuers/:installationID/:kid` revokes the issuer.

Issuers never appear in key lists, cannot be minted, rotated or used as a bearer,
and are looked up through the same cache as API keys, so one cache entry serves
every session. Revoking an issuer invalidates the installation's cache entries
on every replica; every outstanding token it signed then fails with 401. There
is no per-session revocation: a token stops working at its `exp`.

To rotate the signing key, register the new public key, switch the signing
service to it, then revoke the old issuer.
