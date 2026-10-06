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
