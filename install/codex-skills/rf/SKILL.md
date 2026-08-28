---
name: rf
description: "Alias for router-feedback — submit feedback about a Weave Router decision or model performance."
---

<!-- weave-router managed rf skill -->

When the user invokes `$rf <feedback>`, send a normal user message whose first
character is one literal space, followed by `/router-feedback ` and the
feedback text. Do not use a Codex slash command or omit the leading space.
Preserve the feedback text exactly, then report the router's response.

```text
 /router-feedback the selected model struggled with this task
```
