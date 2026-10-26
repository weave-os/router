# Routing identity and automatic state

Automatic routing history belongs to an agent thread, not to a prompt, role,
model, HTTP protocol, or shared client session. Missing reliable thread identity
does not reject normal inference: routing continues without persistent automatic
pins, votes, model history, cooldowns, trim tracking, or prompt-cache affinity.

## Client contract

Send these headers on every request, or the equivalent top-level body object:

| Header | `weave_routing` field | Meaning |
| --- | --- | --- |
| `X-Weave-Conversation-Id` | `conversation_id` | Root conversation shared by descendants |
| `X-Weave-Thread-Id` | `thread_id` | Stable identity of this agent lifecycle |
| `X-Weave-Parent-Thread-Id` | `parent_thread_id` | Immediate spawning thread, when applicable |

```json
{"weave_routing":{"conversation_id":"root","thread_id":"child","parent_thread_id":"parent"}}
```

Identifiers are nonempty, at most 128 UTF-8 bytes, and contain no control
characters or leading/trailing whitespace. Duplicate, malformed, or conflicting
identity signals disable automatic persistence. The router removes
`weave_routing` before provider translation/dispatch. This contract applies to
Anthropic Messages, OpenAI Chat Completions, OpenAI Responses, and Gemini.
Identifiers select state only within authenticated credential/installation and
managed serving-generation boundaries; they do not grant authorization.

Keep the thread ID across compaction and resume. Allocate a new thread ID for
every spawn or fork, including identical prompts and inherited transcripts.
Forward the root conversation ID through every generation; an immediate parent
is not sufficient to identify a nested conversation's root.

## Harness compatibility

| Client | Automatic-state identity |
| --- | --- |
| Pi extension | Lifecycle session ID; children inherit root/parent IDs through dispatch; signed tickets remain credential, installation, thread, and serving-generation bound |
| OpenCode | Native session ID identifies the thread; native parent identifies immediate ancestry; nested children need an explicit root ID for conversation-wide controls |
| Codex, Claude Code, Cursor, Gemini CLI | Explicit contract above; legacy shared session/role signals alone are safely stateless |
| Generic API client | Explicit contract above, otherwise safely stateless |

These are adapter contracts, not claims of live native-client verification.
Session headers and Claude metadata may still identify a conversation for
explicit controls without establishing an automatic thread identity.

## Force controls and migration

`/force-model` applies to the whole identified conversation, including descendants
whose automatic identity is unavailable. `/force-parent` targets only the issuing
thread, even when issued by a child. It does not apply to that thread's children.
Commands missing their required conversation/thread identity are rejected with
instructions to supply it; existing controls remain unchanged.

Automatic state uses the `automatic-thread-v2` namespace. Legacy automatic rows
are not read or migrated. Existing conversation-force keys remain unchanged.
Legacy targeted force rows remain stored but cannot match the new thread digest:
reapply `/force-parent` from the intended thread rather than guessing a mapping.
Old rows are left to existing expiration/cleanup mechanisms.

Deploying this change requires separate release authorization. Legacy clients
without reliable thread signals lose automatic persistence, not inference access.
