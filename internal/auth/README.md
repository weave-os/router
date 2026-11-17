# internal/auth

Installations, API keys, BYOK upstream keys, subscription accounts, delegated
session tokens and the admin login. Operator configuration lives in
[CONFIGURATION.md](../../docs/CONFIGURATION.md); session tokens in
[DELEGATED_SESSION_TOKENS.md](../../docs/DELEGATED_SESSION_TOKENS.md);
subscription serving in [SUBSCRIPTION_ROUTING.md](../../docs/SUBSCRIPTION_ROUTING.md).

## Credential scopes

- Only routing-scoped keys (`rk_`, and the zero-value scope) authenticate
  inference. Analytics and session-issuer records are rejected before any BYOK
  fetch, including when served from cache.
- Session tokens (`rs_`) verify through `VerifySessionToken`, never as a hashed
  bearer. An issuer's cache key is `auth.SessionIssuerKeyHash`, which can never
  equal a hex SHA-256 bearer hash. Tokens over `auth.SessionTokenMaxBytes` are
  refused before parsing.
- An unknown issuer `kid` is not negatively cached: registration cannot evict a
  negative entry on every replica, so a freshly registered issuer would 401.
- Caller-supplied provider identity is cleared on subscription enrollment; the
  provider verifier owns `ProviderUserID`.
- `auth.FundingPriorityKeyFirst` spends a BYOK key before any subscription.

## BYOK secrets

- Secrets at rest go through `auth.Encryptor`. The AEAD associated data is
  `externalID + "\x00" + provider` and must stay byte-identical with the Weave
  backend's `weaverouter` crypto helper; `TestAADFor_GoldenBytes` pins it.
- Derived credentials (key-pair JWTs, WIF, Entra, Anthropic federation) are set
  on a copy, so the cached stored secret is never overwritten. A key whose
  credential cannot be derived is never sent as stored: it is dropped, except
  an Anthropic federation key, which stays with `CredentialError` set so the
  turn cannot fall back to deployment credentials.
- Key-pair JWTs backdate `iat` by 30s and cap the signed window under the
  upstream one-hour limit. The issuer binds the public-key fingerprint, so a
  rotated key stops validating at once.

## Caches

- Positive API-key entries live 5 minutes, negative ones shorter so a new key is
  not refused for long. Deleting a key or changing installation settings
  invalidates the installation on every replica.
- A record is cached only when it was loaded for inference (`VerifyAPIKey`, not
  `VerifyPlatformAPIKey`) and its cluster model lists loaded cleanly, so a
  platform lookup or a transient list miss cannot strip keys or restrictions for
  the whole TTL. A failed BYOK key fetch is only logged; that record is still
  cached with no BYOK keys.
- `LRUAPIKeyCache` and `LRUUserClusterListCache` index entries by installation.
  The LRU eviction callback takes the index mutex, so `Set` cannot hold it
  across the LRU insert; an invalidation generation drops an entry inserted
  while its installation was being invalidated.
- `last_used_at` and first-request updates are fire-and-forget off the request
  path and must not inherit the request's cancellation.

## Fail direction

- A blind-experiment lookup error treats the installation as enrolled, so an
  unidentified subject is never shown routing surfaces. Cohort fetch failures
  are cached only for the short retry window.
- A zero-row installation update returns `auth.ErrInstallationNotFound` instead
  of looking like success.
- Admin login locks an IP out after 5 failures within 5 minutes; the failing
  attempt itself still reports invalid credentials.
