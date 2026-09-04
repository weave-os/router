---
name: router-off
description: "Route Codex to its default provider again (turn the Weave Router off)."
---

<!-- weave-router managed router-off skill -->

# Turn Weave routing off

When the user invokes `$router-off`, switch this Codex installation off the
Weave Router without logging out or deleting its router configuration.

Run exactly:

```bash
npx --package @weave-os/router -y -- weave-router off --codex{{SCOPE}}
```

Then report the result and tell the user the change takes effect on their next
`codex` launch — Codex reads its provider config at startup, so the current
session keeps routing as it already was. It can be reversed with
`$router-on`. Do not uninstall the router or alter any other Codex settings.
