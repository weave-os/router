---
name: force-parent
description: "Pin the current parent thread to a model through the Weave Router while subagents route automatically."
---

<!-- weave-router managed force-parent skill -->

When the user requests `$force-parent <model-id>[:effort]`, run this skill's
`scripts/emit.sh` using the absolute path of this skill directory. Pass the
model and optional effort as one quoted argument. If no model was supplied,
ask which model to pin.

The script prints a leading-space `/force-parent` directive. The Weave Router
reads that exec output and pins only the invoking thread, then continues this
turn. Run it once, do not repeat its output as a user message, and report the
router's response. The directive requires a router version supporting
`force-parent`; do not claim success without its acknowledgment.

`$force-parent` replaces any conversation-wide `$force-model` pin. Children
keep their normal routing policy. `$force-model` restores conversation-wide
pinning; `$unforce-model` clears either mode. Model aliases and `:effort`
suffixes follow `$force-model` syntax. A compaction that changes the thread's
first user message changes its identity; reapply the pin in that thread.
