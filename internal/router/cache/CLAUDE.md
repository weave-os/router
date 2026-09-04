# internal/router/cache — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Cross-request semantic response cache. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## What it does

Short-circuits near-duplicate non-streaming requests by cosine similarity on the cluster scorer's prompt embedding. Captured wire-format bytes are replayed without invoking upstream.

- **Isolation:** per-(installation, inbound-format) — captured bytes are post-translation, so reusing across installations or wire formats would corrupt.
- **Streaming bypasses cache entirely.**
- Singleton is constructed by `buildSemanticCache` in `cmd/router/main.go`.

## What NOT to do

- **Don't cache streaming responses.** Captured bytes would be post-translation SSE frames + lookup latency budget is hostile to first-token-time. If you think we should change this, write a doc first.
- **Don't share entries across installations.** Post-translation bytes are caller-shaped.
- **Don't make cache lookup blocking on a slow store.** The lookup happens on the request path; budget it tightly.
