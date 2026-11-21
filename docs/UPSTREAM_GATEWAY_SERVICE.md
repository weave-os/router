# Upstream gateway service mode

An enterprise AI gateway can put the router behind itself as an inference
provider. Developers keep authenticating to their gateway as they do today.
When one picks a routing model such as `weave-auto` in Claude Code, the gateway
calls the router server-to-server; the router picks a concrete model and calls
back into the same gateway for inference.

```text
Claude Code ──▶ upstream gateway ──(OIDC ID token, /v1/messages, weave-auto)──▶ router
                     ▲                                                          │
                     └──(provider key, e.g. auth_type=wif, concrete model)──────┘
```

Configuration is in [CONFIGURATION.md](CONFIGURATION.md#upstream-gateway-service-mode).

## Inbound authentication

The gateway signs a short-lived JWT as its own OIDC issuer. The router accepts
it when:

- `iss` matches a configured `issuer_url` exactly;
- the signature verifies against a key from that issuer's discovery document
  (`jwks_uri`), using an allowed asymmetric algorithm and a `kid`;
- `aud` contains the configured audience and `sub` is an allowed subject;
- `exp` and `iat` are present and `exp - iat` is within `max_lifetime`.

Key retrieval sits behind the `auth.OIDCKeySet` port (`internal/oidcjwks`
implements it), so another inbound method can plug in without touching the
middleware.

The issuer entry names an existing session issuer (`session_issuer_kid`). That
binds the gateway to one installation: its routing policy, allowed models,
provider keys, spend caps and billing apply, and revoking the issuer turns the
gateway off. Ordinary router keys (`rk_`) and delegated session tokens (`rs_`)
are unaffected and never enter this mode.

## Request handling

- **Surface.** The token is accepted only on `POST /v1/messages`; anything else
  returns `upstream_gateway_surface_rejected`.
- **Credential isolation.** After verification the router removes
  `Authorization`, `x-api-key` and the router-key header from the request, so
  the inbound token can never be copied onto an upstream call.
- **Session.** The session ID comes from the configured `session_headers`, then
  `X-Claude-Code-Session-Id`. A request without one is served unpinned. Session
  pins and blind experiments work as they do for other clients.
- **Identity.** End-user email, account, device and display name are dropped
  from the request context. No identity header and no baggage are sent.
- **Correlation.** The provider key's `forwarded_client_headers` are copied from
  the inbound request. An echoed `traceparent` is kept; the router's own tracing
  injects one only when none is present.
- **Loop protection.** The inner call carries `X-Weave-Router-Hop`, and an
  inbound gateway request carrying it is refused with
  `upstream_gateway_loop_detected`. Dispatch also refuses to send any routing
  model (`weave-*`) to an upstream provider, and a model alias cannot name one.
- **Terminal surfaces.** Routing markers, feedback links, `X-Router-*` response headers and `weave_cost` usage fields are hidden. The response `model` stays the concrete served model.
