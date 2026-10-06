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
exhausted, authorized API capacity serves the originally selected model. Committed
streams are never replayed, and terminal upstream failures are accounted as errors.

A subscription transport must prove provider-enforced included-only dispatch.
Neither production OpenAI Codex nor Anthropic OAuth adapter currently establishes
that guarantee: both providers can enable paid extra usage. Therefore those
adapters are excluded before subscription dispatch, including direct tokens,
bypasses and last-resort paths. Quota snapshots cannot substitute for enforcement.
Authorized API fallback remains available. Synthetic transports can declare the
capability only when their provider enforces it. Production subscription utilization
cannot approach the target until an enforceable included-only protocol is available.

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
but the included-only transport gate described above still applies to serving.
OAuth exchange can rotate the refresh token even if persistence fails; repeat a
fresh provider login after a failed enrollment rather than reusing an old token.

Rollback disables Codex rows before restoring workspace-only indices. If one
owner has enrolled multiple provider users in the same workspace, the old unique
index cannot represent them: rollback fails transactionally until an operator
reconciles those registrations. It never chooses a seat, deletes credentials, or
reassigns ownership automatically.
