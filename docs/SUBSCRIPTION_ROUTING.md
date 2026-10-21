# Subscription routing

Every newly registered subscription has a verified user owner. Account management
uses the verified personal key subject and a live installation membership check;
client identity headers cannot grant management rights. Unassigned historical
accounts remain visible for administrator assignment but never enter serving.

Each request reads candidates from the primary database: the requester's personal
accounts first, then accounts registered to the installation whose owners remain
active members. Organization sharing defaults on; disabling sharing retains
personal capacity. A personal key borrows shared capacity only while its owner
is still an active member; a live installation-shared key (no credential
subject) borrows it whenever sharing is on. Enrollment keys are attribution only and do not partition
serving pools. Refresh, quota, cooldown, and rotation use physical account identity.
The ten-second admission budget is shared across account and model alternatives,
including executor re-entry. Only credential leasing consumes it; time spent
running provider inference does not prevent another eligible seat from being tried.
An admitted attempt finishes under provider timeouts and the caller deadline.
Linked account/model pairs are attempted once per request, within its admission snapshot.
Budget expiry refuses further admission; it never grants permission to buy API.

Automatic alternatives reuse the existing policy's request-compatible ordering
and preserve the selected quality tier, tool/context exclusions and provider
allowlist. Explicit models stay fixed. Boost follows stable source ordering; Max
retains its existing subscription exclusion. After eligible included capacity is
positively quota-exhausted, authorized API capacity serves the originally selected model unless
subscription-aware model sets are configured. Each set is stored as the
models unchecked for that state, so a newly deployed model joins both sets
until someone excludes it. With those sets enabled, the
active set is a preference for included Claude/Codex targets and the exhausted
set limits paid fallback. Any request-compatible model a live subscription can
serve stays eligible on included capacity, so a failed active-set model is
rescued by another included model before paid fallback. Exhausting Claude does
not make its models eligible for paid use while Codex remains available. Every
selected included target must actually use a subscription; after included
targets are unavailable, the router selects a request-compatible model from the
exhausted set, subject to the same funding authorization. The installation allowlist and request safety constraints still
apply to both lanes. An empty exhausted set refuses paid fallback. Disabling the sets retains
ordinary routing; Max continues to ignore linked subscriptions and these sets.
Paid switch and compaction handover summaries respect the exhausted set instead
of silently buying an active-only model. If a switch summary is rejected, the
original history is preserved; if a compaction summary is rejected, the already
compacted body remains unchanged. Committed streams are never replayed, and
terminal upstream failures are accounted as errors.

Native OpenAI Codex and Anthropic adapters accept subscription OAuth credentials.
Anthropic-compatible bearer gateways do not advertise native Claude subscription
support.
An adapter's subscription capability describes transport support, not a guarantee
that the provider cannot charge extra usage. Codex checks the authenticated
read-only `/backend-api/wham/usage` endpoint
before each inference attempt. Missing, unavailable, malformed, or exhausted
included quota skips that account even when purchased OpenAI credits are present,
by default. A self-hosted operator can explicitly set
`ROUTER_CODEX_ALLOW_EXTRA_CREDITS=true` to permit available subscription credits.
For attempts that require extra credits, the authenticated usage response must
report `has_credits=true` (the numeric
balance may be hidden), or unlimited credits. When a balance is reported,
it must be a positive finite number. Unknown quota, exhausted model-specific
quota, workspace/member spending limits, and provider authentication or quota
rejections still block the attempt. Unavailable credits block attempts that need
extra credits; they do not block remaining included usage. This setting affects both enrolled Codex
accounts and caller-presented Codex subscription logins on that self-hosted
deployment. Available subscription credits are tried before any separately
authorized OpenAI API-key fallback. It neither buys credits nor authorizes
API-key fallback. Provider-side balances and spending limits remain authoritative.
The same check covers matching model-specific limits using the final upstream
model after aliases. A model-specific rejection rotates the attempt without
marking the whole account exhausted or placing it on cooldown. Claude accounts with no
quota observation may serve; known exhausted or paid-overage accounts are
excluded. Quota failures rotate to another eligible account before authorized
Weave-funded API capacity serves the selected model. Depleted Weave credits
prohibit paid API fallback, not subscription use.

## Paid inference authorization

Immediately before every provider inference call, the dispatch executor checks
the final credentials against credential-free admission facts retained from the
primary candidate read. OAuth suppression, API-only model sets, BYOK, gateway
bindings, and auxiliary calls cannot erase those facts. A non-OAuth call is
permitted only when no compatible authorized pool exists, or every account in
that pool has positive included-quota exhaustion evidence with an unexpired reset.
Presented direct subscriptions require the same proof. One exception is scoped
to the turn: a buffered upstream 5xx on a plan attempt marks that pool as unable
to serve the turn for a bounded cooldown, and paid rescue of the same turn (the
same model or a scored sibling) is admitted while the mark is live. The mark is
not quota evidence and is never persisted to the account. Model compatibility is
catalog-based; an unrelated provider's available subscription does not block an
API-only model.

An enrollment-read error, lease contention, refresh/auth failure, model denial,
third-party refusal, generic 429, transport failure, or spent admission budget
does not prove pool quota exhaustion. These failures rotate eligible included
seats when possible, otherwise return an error without paid rescue. Generic 429s
are cooldowns, not exhausted state. Rotation retains the original upstream error
alongside the pool-unavailable classification. The guard is terminal within an
executor operation even when its underlying cause would otherwise be retryable.

Anthropic third-party refusals are classified narrowly per account and client
app. The router does not infer that all non-Claude-Code clients are incompatible
or forge provider attestation headers; a refusal cannot authorize paid rescue.
A forced Claude turn whose seats all refused the client app fails with 403.

The one exception is an admin opt-in on a single router key. A key with
`paid_fallback_on_subscription_refusal` set serves those forced turns on paid
API capacity. Anthropic's refusal applies to every subscription's plan limits,
so once any Claude seat refuses the turn's client app, the whole Claude pool,
shared seats included, counts as spent. The router leases no further seats, so a
colleague's extra usage is never spent, and seats it never leased cannot block
the paid attempt. Routed turns and every other failure keep the
rules above, and no request header can opt in. There is no admin API for the
flag; an operator sets it directly and it takes effect within the five-minute
API-key cache TTL:

```sql
UPDATE router.model_router_api_keys
SET paid_fallback_on_subscription_refusal = TRUE
WHERE id = '<api-key-id>'::uuid AND deleted_at IS NULL;
```

Providers control extra usage through account settings and credits. Quota headers
are observations, not an atomic included-only reservation: an unobserved Claude
account or
an in-flight request can cross into provider-enabled extra usage before the router
observes it. Disable extra usage at the provider to require a strict no-extra-usage
boundary. The router never enables extra usage on the provider account. Except for the
explicit self-hosted Codex credit opt-in above, it does not use known overage as a
last-resort account, and observed paid overage is excluded from included-capacity
billing and coverage. Replay tests exercise real adapters against synthetic HTTP
upstreams without replacing their subscription capability.

Migration 0121 preserves historical registration through the enrolling key,
including soft-deleted keys. Duplicate provider/external-account identities are
quarantined together without reallocating owners or overwriting tokens. An active
physical-identity unique index prevents concurrent duplicate serving enrollment.
Ordinary owner assignment does not resolve a quarantined conflict: administrators
must deliberately reconcile duplicate identities first. The down migration keeps
quarantined rows disabled and restores the older owner-present constraint as
NOT VALID, so existing ownerless rows stay inert without reassignment while new
ownerless rows are rejected.

## Codex workspace and user identity

Codex `chatgpt_account_id` identifies the ChatGPT workspace, not its members.
The router preserves it as `external_account_id`, the `ChatGPT-Account-ID`
request header, and the encryption binding. Enrollment performs a server-side
OAuth refresh and stores the provider-returned `chatgpt_user_id` (or `user_id`)
separately as `provider_user_id`. Display names, email headers, and owner assignment
never establish this provider identity. Two verified users in one workspace are
separate seats; the same user/workspace cannot be enrolled for another owner,
even when its existing row is disabled. Refresh rejects a changed or missing
verified user or workspace before publishing credentials.

Migration 0122 disables historical Codex rows until their provider user is
verified. To recover an assigned row, use that owner's verified personal routing
key and run `npx @weave-os/router login codex` with a fresh provider login. The
verified reconnect updates that owner's legacy row, retaining its ID and workspace
binding, and leaves other owners' rows untouched. An unresolved row cannot be
restored with Enable. Unassigned rows require administrator assignment before
reconnect; assignment alone does not enable them. Reconnect restores registration,
but quota, cooldown, and model eligibility checks still apply to serving.
OAuth exchange can rotate the refresh token even if persistence fails; repeat a
fresh provider login after a failed enrollment rather than reusing an old token.

Rollback disables Codex rows before restoring workspace-only indices. If one
owner has enrolled multiple provider users in the same workspace, the old unique
index cannot represent them: rollback fails transactionally until an operator
reconciles those registrations. It never chooses a seat, deletes credentials, or
reassigns ownership automatically.

### Enabling Codex subscription credits locally

Add `ROUTER_CODEX_ALLOW_EXTRA_CREDITS=true` to the server environment (for the
standard Docker Compose setup, `.env.local` is loaded through `env_file`). Recreate
only the `server` service using your existing Compose files and project name;
restarting an existing container does not reload its environment. No harness
settings or provider API keys change. Set it back to `false` and recreate the
server to restore included-only admission. The startup log reports the effective
`allow_extra_credits` value.

Existing authentication failures and durable account cooldowns are preserved;
enabling credits does not clear a previously recorded cooldown. Wait for its
expiry or use the existing account-management flow after resolving the provider
rejection. Included-quota observations alone no longer suppress an opted-in
Codex account: every admitted attempt verifies fresh quota/credit metadata.
Configure the spending allowance with OpenAI; this switch is spending consent,
not a router-enforced monetary cap. No inference is needed to test the option;
regression tests use synthetic quota responses and a local provider server.
