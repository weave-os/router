# Token-efficiency savings: reproduction kit

How much of the router's naive cost savings survives once you account for how
many tokens each model needs for the same work, and how often it actually
solves the task? This kit is the full experiment behind the router's adjusted
savings figure: the method, the task lists, runnable code for both benchmarks,
the analysis, and Weave's own published evidence tables. With your own router
deployment and your own model access you can rerun every step and compare.

The code lives in [`weave_bench/efficiency/`](../weave_bench/efficiency/) and runs
as `weave-bench efficiency …`; install the bench package first
([`bench/README.md`](../README.md#install)).

## The question

"Naive" savings price the tokens the served model actually used at the
comparison model's rates and subtract what the request cost. That assumes:

1. every model needs the same number of tokens for the same work, and
2. every model solves the same tasks.

Neither holds. A model that writes twice as much output for the same change
makes "the comparison model on these tokens" a wrong counterfactual, and a
cheaper model that fails more often costs more per solved task than per
attempt. The kit measures both effects and applies them as factors:

- **Work ratios** (SWE-Together): the comparison model's token volume over the
  served model's, on the same tasks in the same harness.
- **Quality factors** (Terminal-Bench 4): the router arm against an always-on
  arm, as a pass-rate penalty and a kept rate on cost per solved task.

The two benchmarks are scenarios, not bounds. SWE-Together adjusts token work
on realistic, interactive sessions but its reward does not separate frontier
from cheap models; Terminal-Bench measures success on hard, single-shot,
pass/fail work. A given workload can fall outside the range they span.

## Cells and pinning

A **cell** is one coding harness with one model pinned through the router:
`(harness, model)`, e.g. `(Claude Code, gpt-6-luna)`. The harness asks for its
usual model (Claude Code: `claude-opus-5-5`, Codex: `gpt-6-astra`; that model
is also the harness's **comparison model**), and every request carries

```
x-weave-force-model: <model>
```

which pins the session to that model exactly as the `/force-model` command does
([CONFIGURATION.md](../../docs/CONFIGURATION.md)). Everything else, from the
harness, its prompts and tools to the router's translation layer, is identical
across a harness's cells, so the only thing that differs is the model.

Each request also carries `x-weave-rollout-id`, which the router records in
the analytics export's `rollout_id` column. That is how trial tokens are read
back: from the router's own telemetry, never from the harness's estimates.

- **SWE-Together** agents take only a base URL, so trials point it at the
  **pin proxy** (`weave-bench efficiency pin-proxy`), which forwards to the
  router and stamps both headers from the URL path
  (`/pin/<rollout id>/<model>/…`). Every trial gets its own rollout id.
- **Terminal-Bench** runs through Harbor, whose Claude Code and Codex agents
  take a settings layer / `config.toml` that carries the headers directly.

A model outside its own family's harness (a GPT model in Claude Code, Claude in
Codex) is **non-native**. It is measured the same way but applied at a more
conservative bound, below.

### The guard

A trial counts toward its cell only if its router rows show the pinned model
did the work (`estimates.trial_violations`):

- at least one non-utility row (the router's own title, compaction,
  classifier, probe and recap turns are excluded from both the check and the
  tokens);
- at most half the requests failed upstream;
- every row served the pinned model, except Claude Code's web-search side
  requests, which only Anthropic can serve, when they are at most 10% of rows;
- the harness process exited cleanly: a crashed trial's partial tokens would
  make its model look cheaper than it is.

## Tasks and attempts

| | SWE-Together (work ratios) | Terminal-Bench 4 (quality) |
|---|---|---|
| Tasks | 25 ([manifest](../weave_bench/manifests/token_efficiency_swe_together_tasks.json)) | 33 ([manifest](../weave_bench/manifests/token_efficiency_terminalbench4_tasks.json)) |
| Digest | `6b4c99b225a3f967e66a6541c3dad7c4aae4e7cbacb696f6dd6fa0e1cb067685` | `7e92e0a388041e88dee47c9627445bb8ad6dfd092e3e846ae4434f2c17d04afd` |
| Source | [Togetherbench/SWE-Together](https://github.com/Togetherbench/SWE-Together) @ `811a70a2` | Harbor `terminal-bench/terminal-bench@4.0.0` |
| Attempts | 2 per task per cell | 4 per task per arm (two runs of 2, pooled) |
| Harness versions | Claude Code 2.1.280, Codex 0.133.0 | Claude Code 2.1.280, Codex 0.150.0 |

A digest is the sha256 of the sorted task ids joined by newlines
(`weave-bench efficiency tasks` prints both). Results carry the digest of the
set they measured; only pool or compare numbers with matching digests.

**SWE-Together tasks** are banded into three tiers by pass rate (low ≈ 1.0,
medium ≈ 0.5–0.8, high ≤ 0.4) and spread over repositories and languages.
Every cell ran the first three of each tier (the 9 **core** tasks); the cells
that carry most production traffic were extended to all 25 (two to 15). Tasks,
not repeat attempts, tighten the intervals: on the 9-task data the run-to-run
log-SD of a cell's tokens was ≈ 0.38 and the task-to-task ≈ 0.70.

**Terminal-Bench tasks** are a stratified 22-task subset of the full 66
(`fixed22`) plus 11 tasks gpt-6.1-sol solved in a full run (`sol_solved`), so
the strongest comparison arms are not floored at zero.

## Work ratios

For comparison cell C and served cell M in the same harness, over the tasks
both ran (each task's usage is the mean over its attempts):

```
input ratio  = Σ_tasks C(fresh input + cache write + cache read) / Σ_tasks M(same)
output ratio = Σ_tasks C(output)                                / Σ_tasks M(output)
```

A ratio of sums, not a mean of per-task ratios: per-task volumes are
heavy-tailed, and one task would dominate a mean of ratios. The interval is a
**task bootstrap**: 10,000 resamples of the tasks with replacement, 90%
percentile interval, seed 11. Below 1 the comparison model needed fewer tokens
than the served one, so naive savings overstate.

Cache behaviour is left out of the ratios on purpose: eval sessions are short
and cache poorly, while each production request already carries its real cache
mix in the cost the ratio scales.

### Bound rules

Each side's ratio is applied at a bound (`EfficiencyEstimate.bound_rule`):

| Estimate | Native model | Non-native model |
|---|---|---|
| ≤ 1 | point estimate | lower bound |
| > 1, lower bound > 1, ≥ 9 tasks | lower bound | lower bound |
| > 1 otherwise | held at 1 | held at 1 |

"Estimate" is the point for a native model and the lower bound for a
non-native one. A ratio above 1 means the comparison model would have needed
*more* tokens, which raises savings, so it is credited only when the whole
interval is above 1 on at least the 9 core tasks.

Two bound sets come out:

- **Capped** (the floor): every bound held at ≤ 1, so applying it can only
  lower savings.
- **Credited** (the estimate): the table above, so a comparison model that
  needs more tokens than the served one raises savings, conservatively.

## Quality factors

Terminal-Bench runs a **router arm** (no pin; the router's default policy) and
an **always-X arm** (every request pinned to X) per harness, both through the
same router and key. Each attempt is scored pass/fail by the task's tests and
priced at **catalog cost**: its router rows' tokens at the served model's
catalog rates (`prices.generated.json`). The export's own `actual_*` cost is $0
for subscription-served rows, so it cannot price an arm that ran on
subscriptions.

An attempt with no verifier reward is a **failure** when the agent itself
failed (Harbor's `AgentTimeoutError` or `NonZeroAgentExitCodeError`), and
**ungraded** otherwise: a sandbox or infrastructure error keeps its cost but is
neither a pass nor a failure. An attempt with no router rows (no session id, or
no matching export rows) has no known cost and is left out entirely; the report
warns and counts it as `missing_telemetry`. All statistics are per task (mean
over its graded attempts), over the tasks both arms graded, and intervals are
task bootstraps as above.

- **Pass rate**: mean over tasks of each task's solve rate.
- **Kept rate**: savings on cost per solved task over savings on cost per
  attempt,

  ```
  naive    = 1 − router cost / always-X cost
  realized = 1 − (router cost / router solved) / (always-X cost / always-X solved)
  kept     = realized / naive
  ```

  1 means task success changes nothing; 0.65 means 65% of the per-attempt
  savings survive once failures are paid for. It is undefined (null) when the
  router solved nothing or the arms cost the same.
- **Pass-rate penalty**: always-X pass rate over the router's, as a one-sided
  95% upper bound and never below 1 (`quality.capped_pass_rate_penalty`). Each
  arm's standard error comes from its bootstrap interval (half-width ÷ 1.645),
  combined on the log scale by the delta method. A router that passes more
  often earns no extra credit.

## Adjusted savings

Per request served by model M in harness H, against comparison model X:

```
adjusted comparison cost = (X input cost × input ratio + X output cost × output ratio) ÷ penalty
adjusted savings         = adjusted comparison cost − actual cost
```

- *X input / output cost* is M's actual tokens (with their cache mix and
  context tier) priced at X's catalog rates: the naive comparison cost.
- *input / output ratio* is the `(H, X, M)` cell's work ratio under the capped
  or credited bound set.
- *penalty* is H's Terminal-Bench penalty against always-X; 1 for a harness
  Terminal-Bench has not measured.
- A request whose served model has no measured cell gets **no** savings: it
  compares at its own cost. The same holds for a model measured only in another
  harness.

`savings.adjusted_savings` implements exactly this; the router's dashboard
applies the same formula with the credited bound set.

### Pooling and resets

Measurements repeat. Each cell pools its trials over a **period** of runs, so
a refresh tightens the estimate instead of replacing it
([`pooling.py`](../weave_bench/efficiency/pooling.py)). A run starts a new
period, measured from that run alone, when one of its intervals does not
overlap the pooled period's (**drift**), or when the harness version, a
provider revision behind either model, or (for quality) the router's policy
version changed (**harness change**).

## Reproduce

You need:

- a router deployment and a routing key (`rk_…`) for it, plus a read-only
  analytics key (`ra_…`) for the same installation
  ([ANALYTICS_EXPORT.md](../../docs/ANALYTICS_EXPORT.md));
- provider access for every model you pin, linked to the router as API keys or
  subscriptions;
- Docker (Harbor's default sandbox) and the bench package with the `harbor`
  and `tap` extras;
- for SWE-Together: a checkout of the pinned upstream, set up per its own
  README, and a Gemini key for its user simulator (`gemini-3.1-pro-preview`,
  billed to you directly).

**Spend.** Weave's own runs used subscriptions. At catalog prices each
Terminal-Bench arm (33 tasks × 4) cost $91–$678 in Weave's runs, depending on
the model; a SWE-Together trial's cost depends on the model and the task, so
estimate from a small run first. Start with `--core-only --n-attempts 1` and a
single cell, and use `--dry-run` to see every command before it runs.

### 0. Configure

```bash
cd bench
cp bench.example.toml bench.toml   # set [router] base_url
export WEAVE_ROUTER_API_KEY=rk_… WEAVE_ANALYTICS_KEY=ra_…
weave-bench efficiency tasks       # digests must match the table above
```

The sandbox backend is `[harbor] environment` (`docker` by default; `modal` is
optional). Nothing in the kit needs any other hosted service.

### 1. Work ratios (SWE-Together)

```bash
git clone https://github.com/Togetherbench/SWE-Together.git vendor/SWE-Together
git -C vendor/SWE-Together checkout 811a70a28ff20bfbeabf9a8b5ec42152d16c9b4f
git -C vendor/SWE-Together apply "$PWD/weave_bench/efficiency/swe_together_versions.patch"
# set up the checkout's own environment per its README; pass its interpreter as --python

weave-bench efficiency pin-proxy --host 172.17.0.1 --port 8788 &
weave-bench efficiency swe-together run --run-id eff-$(date +%Y%m%d) \
  --cells claude-code:claude-opus-5-5,claude-code:gpt-6-luna --core-only \
  --python vendor/SWE-Together/.venv/bin/python --dry-run
```

Drop `--dry-run` to run. The pin proxy needs the `tap` extra. The patch only makes the upstream harness's pinned
Claude Code and Codex versions settable (Opus 5.5 rejects Claude Code before
2.1.280). Trials are recorded in `jobs/token-efficiency/<run id>/trials.jsonl`;
rerunning the same run id skips trials that finished cleanly and reruns the
rest under a fresh rollout id (`<trial id>-r<n>`), so a failed attempt's rows
never join its retry. Then:

```bash
weave-bench efficiency swe-together collect --run-id eff-<date>
```

This reads the export for the trials' window, keeps each trial's latest
attempt's rows by rollout id (saved under `rows/`, and the whole export window as NDJSON), applies
the guard, and writes `usage.json`: per trial, its cell, task, attempt, token
usage and any violations. That file is the raw evidence the ratios come from;
`--analytics-ndjson` replays a saved export instead of fetching.

### 2. Quality factors (Terminal-Bench 4)

```bash
weave-bench efficiency terminal-bench run --run-id tb4eff-$(date +%Y%m%d) --dry-run
```

Default arms: `cc-router`, `cc-opus-5-5`, `cx-router`, `cx-astra`,
`cx-sol-6-1` (`cc-fable-5-1` is available). Each arm is one Harbor job at
`jobs/<run id>--<arm>/`; resume an interrupted one with
`harbor jobs resume jobs/<run id>--<arm>`.

### 3. Compute the tables

```bash
weave-bench efficiency report \
  --swe-together eff-<date> [more run ids to pool] \
  --terminal-bench tb4eff-<date> \
  --measured 2026-10-08/2026-10-22 --status preliminary \
  --out token-efficiency/results/my_results.json
```

`--swe-together` takes every run a cell was measured in and applies the
pooling rules above per cell, so a run that drifted or changed harness version
starts a new period instead of being blended in (the section note names each
cell's pooled runs and why its period started); by default each harness's cells are compared against its requested model
(`--all-pairs` compares every ordered pair). The output follows the schema
below and is printed as Markdown; `weave-bench efficiency render <file>`
re-renders any results file, including the published one.

## Published results

[`results/token_efficiency_results.json`](results/token_efficiency_results.json)
holds Weave's own evidence, measured 2026-10-08 to 2026-10-22 on Weave's
production router:

- **Work ratios: final.** Ten cells against each harness's comparison model.
  Values are rounded to two decimals as published.
- **Quality factors: final.** Three router-vs-always-X comparisons from the
  combined run (33 tasks × 4 attempts per arm; Claude Code 2.1.280, Codex
  0.150.0). Catalog costs are rounded to whole dollars.

```bash
weave-bench efficiency render token-efficiency/results/token_efficiency_results.json
```

Two observations from the same runs: gpt-5.6-sol needed about 2.5×
gpt-6-astra's tokens on every task measured, so routing its requests elsewhere
saves more than naive (only the credited set counts that); and across harnesses,
which the ratio table does not compare, Codex completed the same tasks with
roughly 3–4× fewer tokens than Claude Code whatever the model.

### Schema (version 1)

```jsonc
{
  "schema_version": 1,
  "measured": "2026-10-08/2026-10-22",           // free-form measurement window
  "work_ratios": {                                // optional section
    "benchmark": "swe-together",
    "status": "final" | "preliminary" | "pending", // pending sections have no rows
    "task_set_digest": "<sha256>",                // must match the SWE-Together manifest
    "interval": "task bootstrap, 90%",
    "note": "<free text>",
    "rows": [{
      "harness": "claude-code" | "codex",
      "comparison_model": "claude-opus-5-5",      // X: the numerator
      "served_model": "gpt-6-luna",               // M: the denominator
      "native": false,                            // M in its own family's harness
      "tasks": 25,                                // tasks both cells ran
      "input":  {"point": 0.99, "lower": 0.70, "upper": 1.33},
      "output": {"point": 1.47, "lower": 1.04, "upper": 1.94},
      "input_rule": "point" | "lower" | "held",   // BoundRule per side
      "output_rule": "lower",
      "input_credited": 0.70, "output_credited": 1.04,
      "input_capped": 0.70,   "output_capped": 1.00
    }]
  },
  "quality_factors": {                            // optional section
    "benchmark": "terminal-bench-4",
    "status": "...", "task_set_digest": "<sha256>", "interval": "task bootstrap, 90%", "note": "...",
    "rows": [{
      "harness": "codex",
      "baseline_model": "gpt-6-astra",            // X of the always-X arm
      "router_arm": "cx-router", "baseline_arm": "cx-astra",
      "tasks": 33,                                // tasks both arms graded
      "kept_rate":          {"point": …, "lower": …, "upper": …},   // null when undefined
      "router_pass_rate":   {"point": …, "lower": …, "upper": …},
      "baseline_pass_rate": {"point": …, "lower": …, "upper": …},
      "pass_rate_penalty": …,                     // null when the router solved nothing
      "router":   {"solved": …, "graded": …, "attempts": …, "catalog_cost_usd": …, "missing_telemetry": …},
      "baseline": {"solved": …, "graded": …, "attempts": …, "catalog_cost_usd": …, "missing_telemetry": …},
      "router_served_mix": {"<model>": …}          // router rows per served model
    }]
  }
}
```

`results.validate_results` enforces it (keys, interval shape, digests, empty
pending sections); the tests run it over the committed file.

## How this kit differs from the published runs

The published numbers came from Weave's internal launcher, which ran the same
harness versions, tasks, attempts and headers on hosted sandboxes. The kit
reproduces the method, not that infrastructure:

- The internal SWE-Together launcher patched the harness to send the pin
  header itself; the kit's pin proxy sends the same headers from outside the
  harness.
- The internal guard excluded the router's utility turns by an internal flag;
  the kit uses the export's `turn_type`.
- The internal Terminal-Bench analysis priced each trial from the router
  sessions whose start and end sit nearest the trial's, within 60 s (matching
  by greatest overlap mis-attributed cost when trials ran concurrently); the
  kit matches by the session id Harbor records. A graded trial with no matched
  session stayed in the pass rate at $0 cost (one `cc-router` attempt, reward
  0, counted in `missing_telemetry`), where the kit drops it.
- The analysis code (`estimates`, `quality`, `pooling`) is a direct port and
  reproduces the internal implementation's numbers exactly, bootstrap draws
  included; the runners have been checked offline (dry runs and tests) and not
  yet end to end against a router outside Weave's.

## Caveats

- SWE-Together's reward scores collaboration with a simulated user and has a
  floor (≈ 0.2 for a session with no edits), so it is used for tokens only.
- Some cells cannot complete some tasks (no edits in 39–152 turns); those
  slots are missing and the ratio uses the tasks both cells ran.
- Pass-rate intervals on 33 tasks × 4 attempts are wide (≈ ±0.12 per arm);
  the penalty's one-sided bound is deliberately conservative.
- In Weave's runs a routed Opus 5.5 scored like a direct one on SWE-Together
  (0.55 vs 0.56 mean reward on 10 tasks × 2): an observation on a small,
  Opus-only sample, not an established equivalence.
- Catalog prices throughout. Prices change; regenerate `prices.generated.json`
  (`make generate` at the repo root) and record its `pricing_version` with
  your results.
