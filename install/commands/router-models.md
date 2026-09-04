---
description: List the models the Weave Router may route to, and turn them on or off.
argument-hint: [model or provider to enable/disable]
allowed-tools: Bash(npx:*)
---

Show me which models this installation lets the Weave Router pick from, and
change that selection when I ask. This is the same list — and the same stored
setting — as the checkboxes on the router dashboard's settings page.

Start by running:

`npx @weave-os/router models --claude{{SCOPE}}`

That prints every deployed model grouped by provider, with `[x]` for models the
router may pick and `[ ]` for models it may not. Present it back to me as a
compact checklist in that same `[x]` / `[ ]` form, keeping the provider
grouping and the exact model ids — I select models by id.

Then:

- If I named models or providers in `$ARGUMENTS`, work out whether I want them
  on or off from how I phrased it, and apply it with
  `npx @weave-os/router models enable <id>... --claude{{SCOPE}}` or
  `npx @weave-os/router models disable <id>... --claude{{SCOPE}}` (add
  `providers` before `enable`/`disable` to switch a whole provider). Several
  ids can go in one call. Then re-run the list and show me the result.
- If I named nothing, stop after the list and ask which ones I want to change.
  Don't change anything I didn't ask for.

Other things I might ask for:

- Rank models by preference:
  `npx @weave-os/router models prefer <id> <id>... --claude{{SCOPE}}` (order
  matters), or `npx @weave-os/router models prefer clear --claude{{SCOPE}}` to
  drop the ranking.
- Providers only: `npx @weave-os/router models providers --claude{{SCOPE}}`.

If the command reports that this router doesn't expose model selection, that's
a Weave-hosted router: model selection belongs to the whole organization there,
so tell me to change it at https://router.workweave.ai/dashboard/settings —
don't try to work around it. Its listing carries no on/off state, so present it
as a plain list; don't infer which models are enabled.

Disabling a model takes effect on the router's next routing decision; no
restart is needed.
