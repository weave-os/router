Created: 2026-11-15
Last edited: 2026-11-15

# Proxy turn loop, session keys, force-model and handover

Contributor reference for `internal/proxy/turnloop.go`, `session_key.go`,
`force_model*.go`, `force_cluster.go` and `handover.go`. Operator-facing
behavior of the same features is in [CONFIGURATION.md](CONFIGURATION.md#forcing-a-model-or-a-routing-cluster).

## Session keys

- The thread session key hashes the API key identity, the client session id
  (header or body) or `metadata.user_id`, and the opening user message. The
  opening message is what keeps concurrent sub-agent threads that share one
  parent session id on separate pins. System text stands in for an empty
  opening message only when no client session id exists, because harnesses
  that lead with a system message rewrite it every turn.
- The key hashes the opening message **as the client sent it**, before router
  commands such as `/force-model` are stripped from the body.
  `StripHistoricalCommands` captures that text before stripping, and the
  request paths derive the key before `ExtractForceModelCommand` runs. Keying a
  command turn on its stripped text would write the pin under a key the next
  turn (which carries the unstripped message in history) never reads.
  `TestForceModelCommand_PinKeyIsDerivedFromUnstrippedOpeningMessage` pins this
  on both the Anthropic and OpenAI chat paths.
- Force-model and `/beta` keys omit the opening message so they cover every
  thread in one client session. Codex full-history forks are re-keyed by agent
  path so they do not share the parent's automatic pin.
- `bindRequestLogger` promotes the request logger (not just attaches it) so the
  access log carries `session_key`. `shortKey` (16 hex) is for logs;
  `sessionKeyHex` (32 hex) is for telemetry joins.

## Turn-loop precedence

`runTurnLoop` resolves one turn in this order; an earlier rule wins:

1. An honoured `x-weave-policy-pin` scores the turn fresh with exactly the
   pinned artifact and roster or fails closed with `ErrPolicyPinUnavailable`.
   Session state (force pins, sticky pins, usage bypass, passthrough, planner
   stays) is not consulted. Utility hard pins stay exempt and report
   `policy_pin_honoured=false`.
2. A user force (`/force-model`, `x-weave-force-model`) outranks every
   automatic fast path, including hard pins and deployment-wide model disables.
   It is still dropped per request when the model is excluded, its provider is
   not enabled for the request, or the turn carries images the model cannot
   read. A dropped force sets `ForcedPinDropped` and constrains the fresh
   decision to the forced model's tier.
3. Utility hard pins (probes, title generation, configured sub-agent
   overrides) bypass pin lookup, pin writes, planner and scorer. They never
   write a session pin, so the cheap utility decision cannot leak into the
   conversation that follows. A utility turn with no servable model is a
   configuration fault, not a retryable outage.
4. Classifier, recap and title turns are scored but never read or anchor a
   session pin.
5. Automatic session pins are reused only while eligible: provider enabled,
   model not excluded or deployment-disabled, image-capable when needed, not
   saturated at the output cap on the previous turn, and fitting the context
   window. A failing guard treats the pin as missing; it is left in storage.
6. Usage bypass, tool-result stickiness, planner-disabled stickiness, the HMM
   EV stay and the expired-pin re-anchor follow, then the scorer and planner.

## Session strikes and exclusions

- Providers struck out by repeated overloads and models demoted after a
  committed stream failure are carried on the context so the in-turn rescue
  walk honors them too. Session cooldowns readmit through one half-open probe
  per request; models whose probe another request holds stay excluded.
- `AutomaticExcludedModels` is the layer that reaches the scorer, the HMM
  authoritative pick, sibling failover and automatic pin reuse at once; an
  explicit force of the same model still routes through it.
- A model saturated at the output cap is excluded from both sticky reuse and
  usage bypass, with no expiry gate, so an auto-continue loop cannot reselect
  it. The check keys on the last served model, not the pin model, because band
  swap can serve the paired member.
- A context-overflow eviction re-verifies with the same `÷4` estimate as the
  pre-filter, and only lifts exclusions that the context filter added.

## Policy deadlines and authority gates

- A policy sidecar deadline or cancellation degrades to the session pin or the
  tier-3 default with a distinct reason. Contract violations fail closed;
  `isPolicyDeadlineErr` requires both the deadline/cancel error and
  `hmm.ErrHMMUnavailable` because contract violations also wrap the latter.
- An authoritative-per-turn upgrade that costs more than the pin keeps the pin
  when the fresh decision carries a confidence score below the configured
  `hmmUpgradeConfidenceThreshold` (`ROUTER_HMM_UPGRADE_CONFIDENCE_THRESHOLD`,
  default `DefaultHMMUpgradeConfidenceThreshold`). The check runs only while the
  upgrade gate is on and neither the evidence path nor the sliding threshold
  served the turn; an upgrade without a confidence score passes. The downgrade
  guards are off by default; an unconfident cheaper vote is discarded before hysteresis
  counts it. The authority cache shadow and downgrade shadow are
  observation-only and never change the served decision.
- Only HMM-written pins are HMM stay candidates.

## Model switches

- Thinking blocks are stripped on the switch turn and on every later turn of a
  session that has ever switched serving identity (an effort change counts),
  because clients resend stale-signed blocks for the life of the transcript.
- On a switch the planner may summarize history. Any summarizer failure,
  timeout or empty summary keeps the full history.

## Handover summarizer and the credential boundary

The model-switch handover in `turnloop.go` and the compaction handover in
`handover.go` run the summarizer on the caller's own credentials for the
summarizer's provider when the request carries them. Otherwise the summarizer
runs on deployment credentials, with request credentials cleared, only when
the request itself uses deployment credentials. A BYOK or client-keyed request
without matching summarizer credentials skips summarization and keeps the full
(or client-compacted) history, so a tenant's history never reaches the
summarizer on the deployment key. Claude subscription OAuth tokens cannot fund
the synthetic summarizer call and are ignored for it.

Both copies of the guard are pinned:
`TestTurnLoop_HandoverSkippedWhenStoredBYOKKeyIsForAnotherProvider` (switch
handover) and `TestRunCompactionHandover_SummarizerNeverCrossesTenantCredentialBoundary`
(compaction handover).

## Force-model name resolution

- Matching is exact up to separator spelling: no prefix, substring or nearest
  match. Separator folding only reaches keys that differ from a real name in
  separator spelling; a folded key shared by two models is dropped
  (`TestFoldedForceModelNames_Unambiguous`), as are ambiguous bare names
  (`TestBareCatalogNames_Unambiguous`).
- "Did you mean" suggestions are display-only and never feed resolution; a
  candidate must keep the input's digits, since a near-miss version is a
  different model.
- Family aliases follow the current release of each family. Retired models
  keep their own-name aliases so existing direct pins still resolve as
  passthrough.
- A forced model resolves to its primary binding; when the primary is excluded
  and a fallback is permitted, the pin targets the fallback. Under
  gateway-exclusive routing a force pins the gateway that aliases the model and
  is refused when no gateway names it.
- `/unforce-model` writes a durable tombstone so a pre-session-scope force row
  on a child thread cannot revive.
- `x-weave-force-cluster` is checked only for strategy here; whether the label
  exists is decided against the roster the sidecar reports on the same request.
