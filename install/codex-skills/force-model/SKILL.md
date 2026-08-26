---
name: force-model
description: "Pin this Codex session to a specific model through the Weave Router."
---

<!-- weave-router managed force-model skill -->

When the user invokes `$force-model <model-id>` (or asks to use the `$fm` alias), send a normal user message whose first character is one literal space, followed by `/force-model ` and the requested model id. Do not use a Codex slash command and do not omit the leading space. For example, send exactly:

```text
 /force-model gpt-5.6-terra
```

Preserve any model id exactly as provided. Report the router's response after it returns.
