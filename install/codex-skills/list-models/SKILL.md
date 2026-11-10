---
name: list-models
description: "List the models this Codex session can pin with $force-model through the Weave Router."
---

<!-- weave-router managed list-models skill -->

# Weave Router forceable models

A bare `$list-models` is answered by the Weave Router itself: it replies with every
model `$force-model` accepts, grouped by provider, with `[x]` for models this
installation lets you force and `[ ]` for ones its policy refuses, plus the
shorthands (`opus`, `sol`, `luna`, …) that resolve to each one. When that reply is
already in the conversation, just present it; there is nothing to run.

If you are reading this instead, the router did not answer the directive (an older
router, or the directive was not the whole message). Run:

```bash
npx --package @weave-os/router -y -- weave-router models --codex{{SCOPE}}
```

and present the models it lists by exact id, grouped by provider. That listing
shows which models the router may choose automatically, not the force policy, so
say that `$force-model <id>` also reaches models marked `[ ]` there unless the
installation excludes them outright.

Pin one with `$force-model <id or shorthand>`; add `:low`, `:medium`, `:high` or
`:max` to set reasoning effort, and `$unforce-model` returns to automatic routing.
