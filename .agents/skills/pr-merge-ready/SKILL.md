---
name: pr-merge-ready
description: Addresses all review comments on a PR automatically. Escalates genuine human decisions. Use when asked to /pr-merge-ready, create and babysit a PR, batch-fix PR comments, or address review feedback quickly.
---

# PR Merge Ready (one pass, then validate once)

It follows the same merge-ready loop (threads resolved, reviewers done, CI green) with a **speed contract**:

1. **Triage every open thread before editing any file.**
2. **Apply every Fix in one pass** (group by file; one file is opened/edited once).
3. **Validate once**, after all edits, scoped to the files you touched — never after each comment.
4. **One commit, one push** per iteration. Extra pushes cost ~6–7 min of CI each and re-trigger bot reviewers.
5. **Comments first, CI second.** Never sit in a CI wait while unresolved actionable threads exist.

Do **not** auto-fix decisions that belong to a human. Product/architecture/scope/intent trade-offs are **Escalate** — pause, ask with options grounded in existing patterns, wait. Never guess.

**This repo is public.** Commit messages, code comments, and fixtures written while fixing feedback follow the root `CLAUDE.md` rule: no customer/org names, org IDs, emails, or private ticket/Slack links — describe triggers generically.

## Absolute rule: never reply on PR threads

**Never post a reply or comment on any PR review thread** — no `addPullRequestReviewThreadReply`, no `gh pr comment`, no posted text of any kind. Where you would otherwise reply (declines, decided escalations), **resolve the thread silently** and **surface the rationale to the user in chat**. Resolving a thread is a status change, not a reply, and is allowed.

## Which reviewers auto-resolve their own threads

Not every reviewer needs a manual `resolveReviewThread` call. Some bots re-scan the pushed commit and close their own thread once the flagged issue is gone; calling `resolveReviewThread` on those threads yourself is redundant, and for weave-checks it's actively wrong — it marks a thread resolved against a code state the bot never re-verified.

| Reviewer                                                         | Auto-resolves?                              | Action                                                                                                                                              |
| ---------------------------------------------------------------- | ------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `cursor[bot]` (Cursor Bugbot)                                    | Yes, once it re-scans and the issue is gone | **Fix:** push the code change only — do not call `resolveReviewThread`. **Decline:** resolve manually (there's nothing new for the bot to re-scan). |
| `cubic-dev-ai[bot]` (Cubic)                                      | Yes, same as above                          | Same as Cursor Bugbot.                                                                                                                              |
| `weave-checks[bot]` (body carries `<!-- weave-check:<slug> -->`) | Yes, same as above                          | Same as Cursor Bugbot.                                                                                                                              |
| `greptile-apps[bot]`                                             | No                                          | Always resolve manually — Fix and Decline both.                                                                                                     |
| Human reviewers                                                  | No                                          | Always resolve manually — Fix and Decline both.                                                                                                     |
| Any other/unrecognized author                                    | Assume no                                   | Always resolve manually. Only skip the mutation for the three named auto-resolving bots above.                                                      |

Identify the author(s) from each `unresolved_comments` entry's `authors` field (or the `<!-- weave-check: -->` marker in `body`) during triage, and carry that classification into the resolve step.

**Reading a weave-checks finding against its own criteria.** The marker's `<slug>` maps to a repo-local `.weave-checks/<slug>.md` if one exists, else to the default check `starter-checks/<slug>.md` in `weave-os/checks` at the release tag pinned in `.github/workflows/weave-checks.yml` (this repo runs with `use-default-checks: true`). When triaging a `weave-checks[bot]` thread, read that file — "Do Not Flag"/"Exclusions" is the fastest false-positive check:

```bash
REF=$(sed -nE 's#.*weave-os/checks/.github/workflows/weave-checks.yml@(v[0-9]+\.[0-9]+\.[0-9]+).*#\1#p' .github/workflows/weave-checks.yml)
gh api "repos/weave-os/checks/contents/starter-checks/<slug>.md?ref=$REF" -H 'Accept: application/vnd.github.raw'
```

**Collapsed threads and mixed authorship:** `pr-fix-plan` collapses same-location threads into one entry with a deduplicated `authors` list and a separately deduplicated `thread_ids` list — the two are **not positionally paired**. Apply the auto-resolving-bot exception only when **every** entry in `authors` is one of the three auto-resolving bots. If mixed, resolve **all** of that entry's `thread_ids` manually.

## Prerequisites

- GitHub CLI authenticated: `gh auth status`
- `jq` 1.6+ installed
- Stacked PRs: `gh stack` extension
- On the PR branch locally, or provide PR number
- Use the bundled analyzer at `./.claude/skills/pr-merge-ready/scripts/pr-fix-plan.sh`; do not assume `pr-fix-plan.sh` is installed on `PATH`
- Go toolchain; `golangci-lint` is optional locally (reported as blocked by `scripts/agent_checks.py` when missing — CI still runs it)

## Commit workflow

**NEVER `git add -A`.** Stage only files you changed.

1. Lint/format autofix on changed files (included in the one validation pass, Step 4)
2. `git add <specific files>`
3. `git commit -m "message"`
4. `git push` (in a stack: `gh stack submit --auto --open`)

If the branch is out of date: `git push --force-with-lease` only after verifying you don't overwrite someone else's work. Never raw `git push --force`.

## High-level loop

```
0. Identify or create PR; align checkout with the PR's base repository and head SHA
0b. WEAVE-CHECKS PREFLIGHT: read weave-checks criteria for the files in play
1. Fetch fresh state
2. If DONE → exit
3. If actionable threads:
   a. TRIAGE ALL threads first (Fix / Decline / Skip / Escalate)
   b. Surface Escalates in one AskQuestion batch; wait if they block
   c. APPLY ALL Fixes in one pass (group by file)
   d. Resolve all Declines (and decided Escalates) silently
   e. Validate ONCE, scoped to touched files (Step 4) + re-run weave checks
   f. One commit + one push
   g. Go to step 1 — do NOT wait for CI yet
4. Else (zero actionable threads):
   a. Foreground CI-wait with cheap comment sentinel (Step 6)
   b. New comment → abort CI wait, go to Step 1
   c. CI all green + DONE → exit
```

### DONE conditions (ALL must hold against the LATEST head SHA)

Exit only when every condition is true on the same poll, _after CI has dispatched for the current head SHA_:

1. **No actionable review comments** — `unresolved_comments` is empty, or every remaining entry is Skip, `is_outdated: true`, or the issue no longer applies (`code_context`). For any thread in the skip ledger (an auto-resolving-bot thread left open under the exception): if the bot has closed it, count DONE; if the bounded fallback has **not** yet fired (≥2 pr-fix-plan polls AND ≥5 min), **do not count DONE and do not force-resolve** — wait for the bot. Only after that window, if the thread is still open, force-resolve, surface the deviation, then count DONE. Escalate threads awaiting a user decision **block DONE**. Unapplied Fix nits from `top_level_comments` (including advisory review bodies) also block DONE. If `reviews_fetch_failed` or `conversation_comments_fetch_failed` is true, feedback is incomplete and DONE is blocked.
2. **All required CI checks for the head SHA are present and concluded.**
3. **No CI pending/running** — `checks_summary.pending == 0` and `checks_fetch_failed` is absent. If `checks_fetch_failed: true`, CI is unknown — do not count as done.
4. **No `REVIEW_REQUESTED`** — `reviewRequests` is empty.
5. **No `CHANGES_REQUESTED`** — `reviewDecision` is not `CHANGES_REQUESTED`; each `latestReviews` state is `APPROVED`, `COMMENTED`, or `DISMISSED`.

## Execution

### Step 0: Identify or create the PR and align the local branch

Single-PR scoped. Use the user's PR number, else infer from the current branch. If the branch has
no PR yet, create one before fetching review state:

1. Ensure the intended changes are committed. Stage only the changed files; **never** use `git add -A`.
2. Push the branch, using `git push -u origin HEAD` when it has no upstream.
3. Open a ready-for-review PR with `gh pr create --base main` (do not pass `--draft` unless the user explicitly asks).
4. Capture the resulting PR number and continue with the same workflow below.

If there are no changes to publish and no PR exists, stop and ask the user what should be reviewed.

Resolve `OWNER/REPO` to the PR's **base repository** from the current repository context. The head owner/repository identify the fork containing the code; do not pass those values to the analyzer, which queries the PR in its base repository.

```bash
read -r OWNER REPO < <(gh repo view --json owner,name --jq '[.owner.login, .name] | @tsv')
gh pr view "${PR_NUMBER:-}" --repo "$OWNER/$REPO" \
  --json number,headRepositoryOwner,headRepository,headRefName,headRefOid \
  -q '{number: .number, head_owner: .headRepositoryOwner.login, head_repo: .headRepository.name, branch: .headRefName, sha: .headRefOid}'
```

If the base repository cannot be resolved from the checkout, use the base repository explicitly; never substitute the fork's head repository.

For a mid-stack PR (`gh stack view --short`), run `gh stack checkout <headRefName>` when needed, then verify `git rev-parse HEAD` equals the PR's `headRefOid`. For a non-stack PR, use the PR number and base repository so GitHub CLI can select the correct fork ref; do not check out by branch name alone:

```bash
# Stop rather than switching away from uncommitted work.
test -z "$(git status --porcelain)" || { echo "Working tree is dirty" >&2; exit 1; }
gh pr checkout "$PR_NUMBER" --repo "$OWNER/$REPO" --branch "pr-${PR_NUMBER}-${HEAD_SHA:0:12}"
test "$(git rev-parse HEAD)" = "$HEAD_SHA" || { echo "PR head SHA mismatch" >&2; exit 1; }
```

If that unique local branch already exists at a different commit, choose another unused local name rather than resetting it. Before editing or pushing, verify the local branch tracks the PR head ref from `head_owner/head_repo`. If the checkout or upstream cannot be verified safely, stop and ask. Fixes always go on the PR's own head branch.

### Step 0b: Weave-checks preflight (once, before any triage or edit)

Read the weave-checks criteria (repo-local `.weave-checks/*.md`, else the pinned `starter-checks/*.md` — see "Reading a weave-checks finding" above) that apply to the files this PR touches, plus the `CLAUDE.md` guide of each touched package. Reviewer asks that contradict those guides (layering, magic provider strings, logging keys, tautological tests) are Decline or Escalate, not Fix.

### Step 1: Fetch fresh PR state (every iteration)

Re-fetch every iteration. Never reuse stale data. **Do not hand-roll GraphQL for review threads.**

```bash
./.claude/skills/pr-merge-ready/scripts/pr-fix-plan.sh "$PR_NUMBER" --owner "$OWNER" --repo "$REPO" --max-comments 0 --json
gh pr view "$PR_NUMBER" --json headRefOid,reviewDecision,reviewRequests,latestReviews
```

`--max-comments 0` = no display cap. pr-fix-plan paginates all review threads (fails loudly past 20 pages / 2,000 threads). GitHub orders threads oldest-first and resolved threads never leave the connection — a single-page `first:100` fetch goes permanently blind past 100 total threads.

`pr-fix-plan --json` returns:

- `head_branch`, `head_sha`, and `is_checked_out` — `is_checked_out` means the local `HEAD` SHA equals the PR head SHA; if false, use Step 0's safe checkout rather than checking out by branch name alone
- `changed_files` — do NOT re-derive via `git diff`
- `unresolved_comments` — unresolved, collapsed. Each has `file`, `line`, `start_line`, `authors`, `body`, `urls`, `thread_ids`, `is_outdated`, `collapsed_count`, `code_context`, `related_lines_in_pr`, `thread_comments`
- `top_level_comments` — omitted when empty
- `pattern_summary`
- `checks` / `checks_summary` — if `checks_fetch_failed: true`, the empty list is NOT trustworthy
- `reviews_fetch_failed` / `conversation_comments_fetch_failed` — if true, feedback is incomplete and DONE is blocked

Skip `is_outdated: true` entries when counting actionable work. Also skip any whose `code_context` shows the issue is already addressed.

`top_level_comments` are not line-anchored. Triage them the same Fix/Decline/Escalate/Skip way, but they have no `thread_ids` to GraphQL-resolve — Fix by editing, Decline/Skip by noting in chat only.

**Advisory review bodies are nitlists.** Bots such as `workweave-bot` post `COMMENTED` reviews with zero threads and a body listing several nits across files; these arrive as `top_level_comments` entries with `source: "review"`. Split each body into **one item per nit** and triage each individually — an empty `unresolved_comments` does not mean "no feedback". Track which nits you've applied across iterations (by review URL + nit) so an already-applied body isn't re-triaged; it stays on the PR forever.

**Comment-first:** any non-Skip entry whose issue still applies → Steps 2–5 immediately. Do not wait for CI, even if checks are running or failed.

**CI-wait:** only when zero actionable comments → Step 6.

### Step 2: Triage ALL unresolved threads before touching code

Walk every actionable `unresolved_comments` entry **before any edit**. Produce a batch plan:

```
Fix     : [thread ids / files]
Decline : [thread ids + one-line rationale]
Escalate: [thread ids]
Skip    : [thread ids]
```

Announce this list to the user, then execute. Do not drip-triage (classify one, edit, classify next).

| Category | Type                                                                 | Action                                                   |
| -------- | -------------------------------------------------------------------- | -------------------------------------------------------- |
| Fix      | Bug, security, style, clear refactor, nit                            | Implement in the Step 3 pass                             |
| Decline  | False positive, already handled, would make code worse, out of scope | No code change; resolve silently; note rationale in chat |
| Escalate | Genuine human decision                                               | Do not change code. Ask (Step 3.5)                       |
| Skip     | Pure questions / discussion                                          | Leave unresolved                                         |

**Fix vs Escalate.** Fix = one objectively-correct resolution matching existing patterns. Escalate signals:

- Product / UX / behavior change
- Architectural trade-off with lasting consequences
- Scope expansion beyond the PR
- Ambiguous intent only the author knows
- Conflicting reviewers
- Risk / blast radius (security, data integrity, billing, migrations)

When in genuine doubt between Fix and Escalate, **Escalate**. (Low-stakes mechanical nits still default to Fix.)

**Bot `suggestion` blocks are authoritative.** When a bot (`workweave-bot`, `greptile-apps[bot]`, `cubic-dev-ai[bot]`, …) offers a fenced ```` ```suggestion ```` replacement — especially comment-length / doc-brevity nits — it is a Fix: apply the replacement **verbatim**. Don't paraphrase or skip because the existing wording seems fine. Only decline if applying it would break the build or contradict a package `CLAUDE.md` rule. Free-form bot feedback without a `suggestion` block is triaged like any other comment.

### Step 3: Apply the whole Fix set in one pass

**Do not validate, commit, or push inside this step.**

1. Group Fix threads by file. Open each file once.
2. Use `code_context` and `related_lines_in_pr` instead of re-reading the diff / grepping. Open the file only for surrounding context.
3. Apply every requested change in that file, including every `related_lines_in_pr` site (fix the pattern once, everywhere it appears in the PR).
4. Move to the next file.
5. Independent files may be edited in parallel (multiple Edit/Write calls in one turn). Dependent edits (same file, or A must land before B compiles) stay serial.

Guidelines: only the changes requested; no unrelated refactors.

#### Decline

No code change, no PR reply. Resolve in Step 5. Collect one concise chat line each: what was asked, why you're declining (1–3 sentences, specific).

#### Skip

Leave alone.

#### Escalate → Step 3.5

Never change code or resolve on your own.

1. Investigate first (referenced file/line, surrounding code, how similar cases are handled).
2. Frame each thread: reviewer ask + link; why it needs a human; 2–4 options grounded in existing patterns with trade-offs; your recommendation.
3. Present **all** escalations in one `AskQuestion` batch.
4. If the user has not responded and the only remaining work is escalations, **stop the loop** — do not spin or sit in CI wait. Resume from Step 1 when they answer.
5. Once they choose, treat the chosen option as a Fix (fold into the current pass if you haven't validated yet; otherwise a new iteration). Resolve silently. If they defer, leave unresolved — the PR is blocked on it.

Do not auto-resolve an escalated thread. Do not let it slip through as a Fix because asking felt slower.

### Step 4: Validate ONCE, scoped to what you touched

Run this **after every Fix in this iteration is applied**, and **never per comment**. Skip the whole step if this iteration was all-Decline (no files changed).

Scope to **this iteration's uncommitted edits**.

```bash
CHANGED_FILES=()
while IFS= read -r -d '' file; do
  [[ -f $file ]] && CHANGED_FILES+=("$file")
done < <({
  git diff --name-only -z
  git diff --cached --name-only -z
  git ls-files --others --exclude-standard -z
})
```

#### Autofix first (so formatting lands in the same commit)

Format only existing Go paths from this iteration's `CHANGED_FILES`; do not run a repository-wide write formatter here.

```bash
GO_FILES=()
for f in "${CHANGED_FILES[@]}"; do [[ $f == *.go ]] && GO_FILES+=("$f"); done
((${#GO_FILES[@]})) && gofmt -w -- "${GO_FILES[@]}"
```

#### Regenerate if a generator input changed

Generated files must land in the same commit, or CI fails on drift. Never hand-edit `internal/sqlc/` or `AGENTS.md`.

| Touched                                                       | Run                              |
| ------------------------------------------------------------- | -------------------------------- |
| `db/queries/`, `db/migrations/`                               | `make generate`                  |
| `internal/router/policy/inference_registry.go`                | `make generate-inference-policy` |
| `internal/router/catalog/` model/pricing data                 | `make generate-statusline`       |
| Any `CLAUDE.md`                                               | `make generate-agent-guides`     |
| `install/directives.tsv`, `install/registry.sh`               | `make embed-registry`            |

#### Then run the scoped checks

```bash
# Tests for the touched Go packages only (whole-module `go test ./...` is CI's job)
if ((${#GO_FILES[@]})); then
  GO_PKGS=($(for f in "${GO_FILES[@]}"; do echo "./$(dirname "$f")"; done | sort -u))
  go vet "${GO_PKGS[@]}" && go test -count=1 "${GO_PKGS[@]}"
fi
# Request-path / dispatch / policy / provider edits:
make inference-boundary
# Discover any other suites the touched paths select (docs, lint, install, frontend, smoke):
python3 scripts/agent_checks.py plan --paths "${CHANGED_FILES[@]}"
```

Run the non-Go suites the planner selects that are cheap and local (`docs`, `install`, `lint` if `golangci-lint` is installed; `cd frontend && npm run typecheck && npm run lint` for `frontend/`). Suites the planner marks `integration: true` (smoke, database) are left to CI — say so in chat; never report them as passed.

### Step 5: One commit, one push, then resolve

**Push before resolving human/Greptile Fix threads.** Resolving first and then failing the push leaves threads closed against code that never landed. Declines have no code change, so they can resolve immediately.

Resolve all Decline threads now. After a successful push, resolve Fix threads and decided Escalates — **except** the auto-resolving-bot exception below. Use each entry's `thread_ids` (collapsed comments may list more than one — resolve every id).

**Auto-resolving-bot exception (Fix only):** for a Fix whose `authors` are entirely `cursor[bot]`, `cubic-dev-ai[bot]`, or `weave-checks[bot]`, do **not** call `resolveReviewThread` — push and let the bot close it. Declines on those same threads still need a manual resolve. Greptile and humans always get a manual resolve.

**Bounded fallback:** carry a skip ledger `(thread_id, first_seen_unresolved_at)`. On every **full** pr-fix-plan poll inside Step 6 (~2 min cadence, not the cheap sentinel), if a skipped bot thread has been open across **≥2 full pr-fix-plan polls AND ≥5 min** since first seen, force-resolve it and tell the user the bot never closed it. Fire this from Step 6 too — Step 5 may never run again during a CI wait.

```bash
gh api graphql -f query='
  mutation($threadId: ID!) {
    resolveReviewThread(input: {threadId: $threadId}) { thread { isResolved } }
  }
' -f threadId='{THREAD_ID}'
```

Then **one** commit and **one** push (skip if no files changed):

```bash
git add path/to/fixed1.go path/to/fixed2.tsx
git commit -m "fix: address PR review feedback (iteration N)

Fixed:
- [summary of fix 1]
- [summary of fix 2]

Declined (with explanation):
- [summary of declined 1]"
git push   # stack: gh stack submit --auto --open
PUSHED_HEAD_SHA=$(git rev-parse HEAD)
# now resolve human/Greptile Fix thread_ids (Declines already resolved above)
```

If push is rejected as out of date: `gh stack sync` (or `git fetch origin && git rebase origin/main`) then `git push --force-with-lease`. Never raw `--force`. Do not resolve Fix threads until the push succeeds.

**After push:** enter the Step 6 watcher. Do not run an in-band `pr-fix-plan`. Auto-reviewers file new threads on the new commit asynchronously — "zero threads right before push" is meaningless for the new SHA.

Skipped bot Fix threads may still show `isResolved: false` until the bot re-scans. If `code_context` shows the issue is gone, treat as non-actionable — but not merge-ready until the bot closes it or the bounded fallback fires.

### Step 6: Foreground CI wait with comment sentinel

**Enter only when Step 1 found zero actionable threads.** The instant the sentinel detects a new unresolved thread, abort and return to Step 1.

A naive `gh pr checks` right after push reports `pending=0` because GitHub has not dispatched yet. Anchor on the head SHA; validate dispatch before treating CI as done.

#### Cheap sentinel + full fetch-on-fire

Do not run `pr-fix-plan` every 15s (~2s/call). Poll a cheap GraphQL sentinel; full `pr-fix-plan` only when something changed.

- **Sentinel** every 15–30s (~0.3s): unresolved count plus newest review-thread, review-body, and conversation-comment timestamps. Tune toward 15s in the first 2 min after push (bots are most active); 30s later.
- **Full fetch** when the sentinel fires, or every ~2 min for CI status.

This pins the session. That is the point: responsiveness.

#### Sentinel query

`CAPTURED_UNRESOLVED_COUNT` / `CAPTURED_THREAD_TOTAL` are taken from **one sentinel read at CI-wait entry**. Baseline and every poll must count the same population — every non-outdated unresolved thread, including Skip and pending auto-resolving-bot threads. Do **not** derive the baseline from pr-fix-plan's actionable count (that excludes those, so every poll looks like "new thread").

The sentinel MUST read the **newest** page (`last:100`, never `first:100`). GitHub orders oldest-first and resolved threads stay forever, so `first:100` reports `unresolved=0` on busy PRs while new comments sit at the tail.

```bash
gh api graphql -f query='
  query($owner: String!, $repo: String!, $pr: Int!) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $pr) {
        headRefOid
        reviewThreads(last: 100) {
          totalCount
          edges {
            node {
              isOutdated
              isResolved
              comments(last: 1) { edges { node { updatedAt } } }
            }
          }
        }
        reviews(last: 10, states: [COMMENTED, APPROVED, CHANGES_REQUESTED]) {
          edges { node { updatedAt } }
        }
        comments(last: 1) { edges { node { updatedAt } } }
      }
    }
  }
' -f owner="$OWNER" -f repo="$REPO" -F pr="$PR_NUMBER" --jq '
  .data.repository.pullRequest | {
    sha: .headRefOid,
    thread_total: .reviewThreads.totalCount,
    unresolved: [.reviewThreads.edges[].node | select(.isResolved == false and .isOutdated == false)] | length,
    latest_thread_timestamp: ([.reviewThreads.edges[].node.comments.edges[].node.updatedAt] | sort | last),
    latest_review_timestamp: ([.reviews.edges[].node.updatedAt] | sort | last),
    latest_conversation_comment_timestamp: ([.comments.edges[].node.updatedAt] | sort | last)
  }
'
```

`thread_total` is the arrival detector (monotonic — resolved threads never leave). `unresolved` is the state detector. Compare **both** to the entry baseline. `unresolved` alone misses a swap (new thread filed in the same interval an old one closed). A **reply on an existing unresolved thread** changes neither count — detect it via `latest_thread_timestamp`. The separate `latest_review_timestamp` and `latest_conversation_comment_timestamp` detect new top-level review bodies and conversation comments even when no thread is unresolved.

#### Watcher loop

```
captured_unresolved_count, captured_thread_total = one sentinel read at entry
last_seen_sha = HEAD_SHA
last_seen_thread_timestamp, last_seen_review_timestamp, last_seen_conversation_comment_timestamp = that same read

loop:
  sleep 15–30s
  run sentinel

  if sentinel.sha != last_seen_sha:
    # Someone else pushed. Watcher is stale.
    break → Step 1

  if sentinel.thread_total > captured_thread_total:
    break → Step 1   # new thread (catches the swap case)

  if sentinel.unresolved > captured_unresolved_count:
    break → Step 1

  if sentinel.latest_thread_timestamp > last_seen_thread_timestamp:
    break → Step 1   # reply on an existing unresolved thread

  if sentinel.latest_review_timestamp > last_seen_review_timestamp:
    break → Step 1

  if sentinel.latest_conversation_comment_timestamp > last_seen_conversation_comment_timestamp:
    break → Step 1

  # Dispatch check ONCE after the first sentinel cycle, not every tick
  if not yet validated dispatch:
    check_run_count = gh api "repos/$OWNER/$REPO/commits/$HEAD_SHA/check-runs" --jq '.total_count'
    if check_run_count == 0: continue  # no runs dispatched yet; don't false-DONE

  # Full CI check every ~5 sentinel cycles (~2 min), not every tick
  if cycles_since_last_ci_check >= 5:
    ./.claude/skills/pr-merge-ready/scripts/pr-fix-plan.sh "$PR_NUMBER" --owner "$OWNER" --repo "$REPO" --max-comments 0 --json
    also run the bounded bot-thread fallback against the skip ledger (force-resolve if ≥2 polls AND ≥5 min)
    if checks_summary.failed > 0:
      # Do not wait for the rest of the matrix. Pull logs, fix, push.
      break → Step 1 with failing checks as extra Fix items
    if checks all concluded and pending == 0:
      fetch reviewer state; if DONE (skip-ledger threads only after bot close or post-window force-resolve) → Step 7
    if actionable comments → Step 1

  if elapsed_watch_minutes > 45: surface to user, stop
```

Use Bash `Monitor` or a `while`/`sleep` loop — foreground, not scheduled wake-ups. If you genuinely must yield (`/loop` pacing, user asked to unpin, wait outlives 45 min), follow "If you must yield" — never schedule a wake-up whose prompt re-invokes `/pr-merge-ready`.

#### If you must yield: state-carrying resume prompt (never `/pr-merge-ready`)

Re-invoking the skill re-injects this whole file on every wake-up. The skill is already in context; a wake-up only needs loop state.

```
Resume the pr-merge-ready loop. The skill is already in context — do NOT re-invoke /pr-merge-ready.
PR #<num> <owner>/<repo>, branch <headRefName>, head SHA <sha>
Phase: <ci-wait | fixing threads | blocked-on-escalation> · Iteration <n>/5
Sentinel baseline (captured unresolved / captured total): <n> / <m>
Pending escalations: <one line each, or "none">
Declines to surface at exit: <count>
Last CI state: <pass/fail/pending counts, or "not yet dispatched for this SHA">
On wake: run the Step 1 fetch (`./.claude/skills/pr-merge-ready/scripts/pr-fix-plan.sh <num> --owner <owner> --repo <repo> --max-comments 0 --json`; `gh pr view <num> --json headRefOid,reviewDecision,reviewRequests,latestReviews`), re-evaluate DONE, then comments-first.
If the skill body is no longer in context (compacted away), Read .claude/skills/pr-merge-ready/SKILL.md once before acting.
```

#### When CI wait completes (no new threads)

1. Read failing check logs for any `bucket: fail` rows and feed them into the next iteration as issues to fix:

   ```bash
   gh run view <run-id> --log-failed 2>&1 | grep -E 'error|Error|##\[error\]|FAIL' | head -40
   ```

2. Re-run `pr-fix-plan` once more (auto-reviewers may have posted at the end of CI).
3. Go back to Step 1.

### Step 7: Stop

When Step 1's DONE check passes against the latest head SHA:

```
PR #<num> ready to merge:
  - Resolved threads this run: <count>
  - Declined threads this run: <count>
  - Escalated → user-decided this run: <count>
  - CI checks: all green (<count> checks)
  - Reviewers: <list>
```

If after **5 full iterations** (5 push cycles) the loop hasn't terminated, stop and surface the blocker. Don't loop forever.

## Anti-patterns

| Anti-pattern                                                                  | Why it bit us                                                                                                                  | Instead                                                                     |
| ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------- |
| Running `make check` / `go test ./...` on every fix iteration                 | Whole-module tests + codegen for a few changed lines                                                                           | Step 4: touched packages + planner-selected suites; CI runs the full matrix |
| Fix comment 1 → validate → commit → fix comment 2 → validate…                 | Multiplies local typecheck/test cost by N comments; N CI cycles (~6–7 min each) and N bot re-reviews                           | Triage all → edit all → validate once → one push                            |
| Scoping `$CHANGED_FILES` to `origin/main...HEAD`                              | Pays lint/typecheck/tests for the whole PR on a one-line review fix                                                            | Uncommitted `git diff` + cached + untracked                                 |
| Resolving Fix threads, then a push that fails                                 | Threads closed against code that never landed                                                                                  | Push first, resolve human/Greptile Fixes after                              |
| Waiting out the full CI matrix after the first red check                      | Burns 5–10 min for jobs that cannot save a failed run                                                                          | Abort CI wait on `failed > 0`, fix, re-push                                 |
| Invoking `make precommit` / `make check` from this skill                      | Those run the whole module matrix; meant for shipping a feature                                                                | Step 4 scoped checks                                                        |
| Treating a bot `COMMENTED` review as "no feedback" because it has no threads  | Missed a whole `workweave-bot` nitlist living in the review body                                                               | Split `top_level_comments` review bodies into one item per nit              |
| Rephrasing a bot's `suggestion` block instead of applying it                  | Lost the brevity the bot was asking for; subjective drift                                                                      | Apply the suggested replacement verbatim                                    |
| Committing a generator-input change without its regenerated output           | CI drift check fails (`internal/sqlc`, `AGENTS.md`, policy docs)                                                               | Step 4 regenerate table                                                     |
| Falling through to another PR review workflow                                 | This skill is the full workflow and validates once per batch                                                                   | This file is the full workflow                                              |
| Waiting for CI before fixing review comments                                  | Reviewers blocked while the agent watches checks                                                                               | Comments first                                                              |
| Sitting in CI wait without polling for new threads                            | Auto-reviewers post during CI                                                                                                  | Sentinel every 15–30s                                                       |
| Polling CI immediately after push                                             | `pending=0` before dispatch → false DONE                                                                                       | Anchor on `commits/$HEAD_SHA/check-runs` count                              |
| Treating "0 threads at push time" as forever                                  | Bots post minutes later                                                                                                        | Sentinel + re-fetch after CI                                                |
| Conflating PRs in a stack                                                     | Comments on PR #2 fixed on PR #1's branch                                                                                      | Verify base repo, head repo, and checked-out head SHA each iteration        |
| Auto-fixing a product/architecture/scope decision                             | Shipped an opinionated change the author didn't want                                                                           | Escalate                                                                    |
| Asking with no research or options                                            | Forces the human to do the legwork                                                                                             | Investigate, then 2–4 grounded options + recommendation                     |
| Running `pr-fix-plan` on every sentinel tick                                  | Wastes ~2s/tick                                                                                                                | Cheap GraphQL sentinel; full fetch on fire or ~2 min                        |
| Passing `/pr-merge-ready` as the `ScheduleWakeup` prompt                      | Re-injects the whole skill per wake-up                                                                                         | ~1KB state-carrying resume prompt                                           |
| Manually resolving a Cursor/Cubic/weave-checks Fix right after push           | weave-checks re-files the same nit                                                                                             | Skip resolve; let the bot re-scan                                           |
| Skipping resolve on a Decline just because the bot auto-resolves Fixes        | Nothing new to re-scan → thread sits open                                                                                      | Always resolve Declines yourself                                            |
| Applying the bot exception to a collapsed entry that mixes a bot with a human | `authors` and `thread_ids` aren't paired — you'd strand the human thread                                                       | Only skip when `authors` is entirely auto-resolving bots                    |
| Declaring merge-ready while a skipped bot thread is still open                | Bot never re-scanned                                                                                                           | Wait the 2-poll / 5 min window; only then force-resolve and count DONE      |
| Sentinel `comments(first: 1)`                                                 | Replies on an open thread never bump counts; oldest timestamp stays put                                                        | `comments(last: 1)` + compare `latest_thread_timestamp`                     |

## Error handling

| Issue                                                             | Solution                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| Comment references deleted line                                   | Check git history, apply to current location                                   |
| File was renamed                                                  | Find new path, apply there                                                     |
| Conflicting comments                                              | Address most recent; note the conflict in chat                                 |
| Fix breaks scoped tests                                           | Revert that fix, try an alternative; don't re-run the whole suite              |
| CI check stuck `IN_PROGRESS` >30min                               | Surface to user, stop the watcher                                              |
| Reviewer keeps re-requesting the same point                       | After 2 declines on the same thread, surface to user                           |
| Push rejected (rebase / merged dependency)                        | `gh stack sync` or rebase onto `origin/main` → `--force-with-lease`            |

## Notes

- Always re-run `pr-fix-plan` (+ `gh pr view` for reviewer state) at the **start** of every iteration.
- A Skip thread does not block DONE — it stays unresolved on purpose.
- Prefer minimal fixes.
- When declining, resolve silently and surface the rationale in chat.
- Cap iterations at **5** push cycles.
- Output discipline: announce the unresolved-thread batch plan before editing. If the sentinel interrupts a CI wait, say so ("sentinel detected new review thread — fixing before CI finishes").
