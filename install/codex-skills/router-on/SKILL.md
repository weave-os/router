---
name: router-on
description: "Route Codex through the Weave Router again (turn it back on)."
---

<!-- weave-router managed router-on skill -->

# Turn Weave routing on

When the user invokes `$router-on`, switch this Codex installation back onto
the Weave Router using the managed configuration already on disk.

Run exactly:

```bash
npx --package @weave-os/router -y -- weave-router on --codex{{SCOPE}}
```

Then report the result and tell the user the change takes effect on their next
`codex` launch — Codex reads its provider config at startup, so the current
session keeps routing as it already was. Do not re-run the installer or alter
any other Codex settings.
