# Subscription routing

Every newly registered subscription has a verified user owner. Account management
uses the verified personal key subject and a live installation membership check;
client identity headers cannot grant management rights. Unassigned historical
accounts remain visible for administrator assignment but never enter serving.

Each request reads candidates from the primary database: the requester's personal
accounts first, then accounts registered to the installation whose owners remain
active members. Organization sharing defaults on; disabling sharing retains
personal capacity. Shared capacity requires a verified requester who is still an
active member; routing keys without a credential subject receive no subscription
capacity. Enrollment keys are attribution only and do not partition
serving pools. Refresh, quota, cooldown, and rotation use physical account identity.
The ten-second rotation budget is shared across account and model alternatives.

Automatic alternatives reuse the existing policy's request-compatible ordering
and preserve the selected quality tier, tool/context exclusions and provider
allowlist. Explicit models stay fixed. Boost follows stable source ordering; Max
retains its existing subscription exclusion. After eligible included capacity is
exhausted, authorized API capacity serves the originally selected model unless
subscription-aware model sets are configured. With those sets enabled, the
active set limits included Claude/Codex targets and the exhausted set limits
paid fallback. Exhausting Claude does not make its active-only models eligible
for paid use while Codex remains available. Every selected included target must
actually use a subscription; after eligible active targets are unavailable,
the router selects a request-compatible model from the exhausted set. The
installation allowlist and request safety constraints still apply to both sets.
An empty enabled state refuses that funding lane. Disabling the sets retains
ordinary routing; Max continues to ignore linked subscriptions and these sets.
Paid handover summaries also respect the exhausted set; an excluded summary
preserves full history instead of silently buying an active-only model. Committed
streams are never replayed, and terminal upstream failures are accounted as errors.

Native OpenAI Codex and Anthropic adapters accept subscription OAuth credentials.
Anthropic-compatible bearer gateways do not advertise native Claude subscription
support.
An adapter's subscription capability describes transport support, not a guarantee
that the provider cannot charge extra usage. Codex checks the authenticated
read-only `/backend-api/wham/usage` endpoint
before each inference attempt. Missing, unavailable, malformed, or exhausted
included quota skips that account even when purchased OpenAI credits are present.
The same check covers matching model-specific limits using the final upstream
model after aliases. A model-specific rejection rotates the attempt without
marking the whole account exhausted or placing it on cooldown. Claude accounts with no
quota observation may serve; known exhausted or paid-overage accounts are
excluded. Quota failures rotate to another eligible account before authorized
Weave-funded API capacity serves the selected model. Depleted Weave credits
prohibit paid API fallback, not subscription use.

Providers control extra usage through account settings and credits. Quota headers
are observations, not an atomic included-only reservation: an unobserved Claude
account or
an in-flight request can cross into provider-enabled extra usage before the router
observes it. Disable extra usage at the provider to require a strict no-extra-usage
boundary. The router neither enables extra usage nor uses known overage as a
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
