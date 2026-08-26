---
name: router-feedback
description: "Submit feedback about a Weave Router decision or model performance."
---

<!-- weave-router managed router-feedback skill -->

When the user invokes `$router-feedback <feedback>` (or asks to use the `$rf` alias), send a normal user message whose first character is one literal space, followed by `/router-feedback ` and the feedback text. Do not use a Codex slash command and do not omit the leading space. Preserve the feedback text exactly. For example:

```text
 /router-feedback the selected model struggled with this task
```

Report the router's response after it returns.
