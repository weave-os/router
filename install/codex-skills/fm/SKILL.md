---
name: fm
description: "Alias for force-model — pin this Codex session to a specific model through the Weave Router."
---

<!-- weave-router managed fm skill -->

When the user invokes `$fm <model-id>`, send a normal user message whose first
character is one literal space, followed by `/force-model ` and the requested
model id. Do not use a Codex slash command or omit the leading space. Preserve
the model id exactly, then report the router's response.

```text
 /force-model gpt-5.6-terra
```
