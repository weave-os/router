---
name: router-status
description: "Show whether Codex is routing through the Weave Router or using its default provider."
---

<!-- weave-router managed router-status skill -->

# Weave Router status

When the user invokes `$router-status`, report whether this Codex installation
routes through the Weave Router.

Run exactly:

```bash
npx --package @weave-os/router -y -- weave-router status --codex{{SCOPE}}
```

Then summarize the result in one line. Do not change any configuration.
