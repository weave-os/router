# internal/router/cache — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Cross-request semantic response cache. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## What it does

Short-circuits near-duplicate non-streaming requests by cosine similarity on the cluster scorer's prompt embedding. Captured wire-format bytes are replayed without invoking upstream.

- **Isolation:** per-(installation, inbound-format, verified provenance). Provenance includes credential subject, product, serving profile/revision, selected model/provider, and upstream credential scope. Missing identity fails closed. Only ordinary unrestricted prompts participate; tools, structured output, media, and subscription credential walks bypass reuse. Fallback or identity-changing attempts cannot populate the initial target’s bucket.
- **Streaming bypasses cache entirely.**
- Singleton is constructed by `buildSemanticCache` in `cmd/router/main.go`.

## What NOT to do

- **Don't cache streaming responses.** Captured bytes would be post-translation SSE frames + lookup latency budget is hostile to first-token-time. If you think we should change this, write a doc first.
- **Don't share entries across installations.** Post-translation bytes are caller-shaped.
- **Don't make cache lookup blocking on a slow store.** The lookup happens on the request path; budget it tightly.

`Cache.Stats` exposes aggregate hit/miss and eviction counts without identity labels. Response bodies and headers are copied at storage and lookup boundaries. Existing installation, bucket, body-size, and TTL limits also bound provenance cardinality.
