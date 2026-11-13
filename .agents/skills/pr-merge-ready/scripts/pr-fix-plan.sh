#!/usr/bin/env bash
#
# Fetches the PR's review threads, diff, CI checks, and PR-level comments via
# the GitHub CLI (in parallel), then cross-references each unresolved comment's
# location against the diff:
#   - threads on the same file with overlapping line ranges collapse into one
#     entry (authors, bodies, urls, and thread ids are merged);
#   - `code_context` is the commented line's content from the diff;
#   - `related_lines_in_pr` lists identical lines in other changed files with
#     the same extension — places that likely need the same fix.
# Each entry carries the review-thread node IDs, so a caller (human or agent)
# can resolve the threads afterwards without a second query.
#
# Requirements: bash 3.2+, gh (authenticated: `gh auth login`), jq 1.6+.
# If coreutils `timeout`/`gtimeout` is on PATH, each gh call is bounded.

set -euo pipefail

readonly MAX_THREAD_PAGES=20
readonly THREADS_PER_PAGE=100
readonly MAX_THREAD_COMMENT_PAGES=20
readonly MAX_REVIEW_PAGES=20
readonly REVIEWS_PER_PAGE=100
readonly GH_TIMEOUT_SECS=60
readonly CHECK_FIELDS="name,state,bucket,description,link,startedAt,completedAt,workflow"
readonly DEFAULT_MAX_COMMENTS=20

usage() {
  cat <<EOF
Usage: $(basename "$0") PR_NUMBER [--owner OWNER --repo REPO] [--max-comments N] [--json]

Analyze a GitHub PR's unresolved review comments and emit a structured fix plan.

Options:
  --owner OWNER      GitHub org/user (default: repository selected by gh)
  --repo REPO        GitHub repo name (default: repository selected by gh)
  --max-comments N   Max unresolved comments to include; extras are reported as
                     pattern_summary.truncated_count (0 = no cap) [default: $DEFAULT_MAX_COMMENTS]
  --json             Emit structured JSON instead of text
  -h, --help         Show this help

JSON output fields:
  head_branch, head_sha, is_checked_out, changed_files,
  unresolved_comments[]: file, line, start_line, authors, body, urls,
                         comments, thread_ids, is_outdated, collapsed_count,
                         code_context (omitted when unknown),
                         related_lines_in_pr, thread_comments
  top_level_comments[] (omitted when empty): source, author, body, url, created_at
  pattern_summary, checks, checks_summary,
  *_fetch_failed (when a best-effort feedback or checks fetch fails)
EOF
}

usage_error() {
  echo "Error: $1" >&2
  echo "Run '$(basename "$0") --help' for usage." >&2
  exit 2
}

PR_NUMBER=""
OWNER=""
REPO=""
MAX_COMMENTS=$DEFAULT_MAX_COMMENTS
AS_JSON=false

while (($# > 0)); do
  case $1 in
    --owner)
      (($# >= 2)) || usage_error "--owner requires a value"
      OWNER=$2
      shift 2
      ;;
    --owner=*) OWNER=${1#*=}; shift ;;
    --repo)
      (($# >= 2)) || usage_error "--repo requires a value"
      REPO=$2
      shift 2
      ;;
    --repo=*) REPO=${1#*=}; shift ;;
    --max-comments)
      (($# >= 2)) || usage_error "--max-comments requires a value"
      MAX_COMMENTS=$2
      shift 2
      ;;
    --max-comments=*) MAX_COMMENTS=${1#*=}; shift ;;
    --json) AS_JSON=true; shift ;;
    -h | --help) usage; exit 0 ;;
    -*) usage_error "unknown option: $1" ;;
    *)
      [[ -z $PR_NUMBER ]] || usage_error "unexpected argument: $1"
      PR_NUMBER=$1
      shift
      ;;
  esac
done

[[ -n $PR_NUMBER ]] || usage_error "missing PR_NUMBER"
[[ $PR_NUMBER =~ ^[0-9]+$ ]] || usage_error "PR_NUMBER must be a positive integer"
[[ $MAX_COMMENTS =~ ^[0-9]+$ ]] || usage_error "--max-comments must be a non-negative integer"
# Strip leading zeros: both values are passed to jq as JSON numbers.
PR_NUMBER=$((10#$PR_NUMBER))
MAX_COMMENTS=$((10#$MAX_COMMENTS))

die() {
  if [[ $AS_JSON == true ]] && command -v jq >/dev/null 2>&1; then
    jq -n --arg error "$1" '{success: false, error: $error}' >&2
  else
    echo "Error: $1" >&2
  fi
  exit 1
}

warn() {
  echo "Warning: $1" >&2
}

command -v jq >/dev/null 2>&1 || die "jq not found. Install it from https://jqlang.org/download/"
command -v gh >/dev/null 2>&1 || die "GitHub CLI (gh) not found. Install it from https://cli.github.com/"

# ============================================================================
# Owner / repo resolution
# ============================================================================

resolve_github_remote() {
  local repo
  repo=$(gh repo view --json owner,name --jq '[.owner.login, .name] | @tsv') ||
    die "Could not resolve owner/repo with GitHub CLI"
  IFS=$'\t' read -r OWNER REPO <<<"$repo"
  [[ -n $OWNER && -n $REPO ]] || die "Could not resolve owner/repo with GitHub CLI"
}

if [[ -n $OWNER && -z $REPO ]]; then
  die "--owner requires --repo"
elif [[ -z $OWNER && -n $REPO ]]; then
  die "--repo requires --owner"
elif [[ -z $OWNER ]]; then
  resolve_github_remote
fi

# ============================================================================
# gh invocations
# ============================================================================

TIMEOUT_BIN=$(command -v timeout || command -v gtimeout || true)

# Bound each gh call when a timeout binary exists, so a hung network call
# can't stall the whole plan.
gh_bounded() {
  if [[ -n $TIMEOUT_BIN ]]; then
    "$TIMEOUT_BIN" "$GH_TIMEOUT_SECS" gh "$@"
  else
    gh "$@"
  fi
}

# gh_or_fail ERR_FILE GH_ARGS...: stdout passes through; on failure a one-line
# reason is written to ERR_FILE.
gh_or_fail() {
  local err_file=$1 rc=0
  shift
  gh_bounded "$@" 2>"$err_file.stderr" || rc=$?
  if ((rc == 0)); then
    return 0
  fi
  if ((rc == 124)) && [[ -n $TIMEOUT_BIN ]]; then
    echo "gh $1 timed out after ${GH_TIMEOUT_SECS}s" >"$err_file"
  else
    echo "gh $1 failed: $(cat "$err_file.stderr")" >"$err_file"
  fi
  return 1
}

# GitHub orders review threads oldest-first and resolved threads never leave
# the connection, so a single unpaginated page goes permanently blind to new
# threads once a PR accumulates 100 total threads — follow pageInfo until
# exhausted. A null $after fetches the first page.
# shellcheck disable=SC2016
readonly REVIEW_THREADS_QUERY='
query($owner: String!, $repo: String!, $pr: Int!, $after: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      headRefName
      headRefOid
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          startLine
          comments(first: 100) {
            pageInfo { hasNextPage endCursor }
            nodes { body url author { login } }
          }
        }
      }
    }
  }
}'

# Emits "done", "next:<cursor>", or "error:<message>" for one GraphQL page.
# A null pullRequest (wrong owner/repo, missing PR, no access) comes back as
# success, and a missing nodes/pageInfo must not be read as a final page.
# shellcheck disable=SC2016
readonly THREAD_PAGE_VERDICT='
.data.repository.pullRequest as $pull
| if $pull == null then "error:PR #\($pr) not found on \($slug) (or no access)"
  elif $pull.reviewThreads.nodes == null or $pull.reviewThreads.pageInfo == null then
    "error:Malformed reviewThreads connection for PR #\($pr) (missing nodes or pageInfo)"
  elif any($pull.reviewThreads.nodes[]; .comments.nodes == null or .comments.pageInfo == null) then
    "error:Malformed thread comments connection for PR #\($pr) (missing nodes or pageInfo)"
  elif any($pull.reviewThreads.nodes[]; .comments.pageInfo.hasNextPage == true and (.comments.pageInfo.endCursor // "") == "") then
    "error:Thread comments reported hasNextPage without an endCursor"
  elif $pull.reviewThreads.pageInfo.hasNextPage != true then "done"
  elif ($pull.reviewThreads.pageInfo.endCursor // "") == "" then
    "error:PR #\($pr) reviewThreads reported hasNextPage without an endCursor"
  else "next:\($pull.reviewThreads.pageInfo.endCursor)"
  end'

readonly THREAD_PAGES_MERGE='
{
  head_branch: (.[0].data.repository.pullRequest.headRefName // ""),
  head_sha: (.[0].data.repository.pullRequest.headRefOid // ""),
  threads: [
    .[].data.repository.pullRequest.reviewThreads.nodes[]
    | {
        id: (.id // ""),
        is_resolved: (.isResolved == true),
        is_outdated: (.isOutdated == true),
        path,
        line,
        start_line: .startLine,
        comments: [
          .comments.nodes[]
          | {author: (.author.login // "ghost"), body: (.body // ""), url: (.url // "")}
        ],
        comments_has_next_page: (.isResolved != true and .comments.pageInfo.hasNextPage == true),
        comments_end_cursor: (.comments.pageInfo.endCursor // "")
      }
  ]
}'

# shellcheck disable=SC2016
readonly THREAD_REPLIES_QUERY='
query($threadId: ID!, $after: String) {
  node(id: $threadId) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { body url author { login } }
      }
    }
  }
}'

# shellcheck disable=SC2016
readonly THREAD_REPLIES_PAGE_VERDICT='
.data.node as $thread
| if $thread == null then "error:Review thread not found"
  elif $thread.comments.nodes == null or $thread.comments.pageInfo == null then
    "error:Malformed review-thread comments response (missing nodes or pageInfo)"
  elif $thread.comments.pageInfo.hasNextPage != true then "done"
  elif ($thread.comments.pageInfo.endCursor // "") == "" then
    "error:Review-thread comments reported hasNextPage without an endCursor"
  else "next:\($thread.comments.pageInfo.endCursor)"
  end'

readonly THREAD_REPLIES_MERGE='
[.[].data.node.comments.nodes[]
 | {author: (.author.login // "ghost"), body: (.body // ""), url: (.url // "")}]'

fetch_review_threads() {
  local out=$1 err=$2 cursor="" page page_file verdict
  local -a args
  for ((page = 1; page <= MAX_THREAD_PAGES; page++)); do
    args=(api graphql -f "query=$REVIEW_THREADS_QUERY" -f "owner=$OWNER" -f "repo=$REPO" -F "pr=$PR_NUMBER")
    if [[ -n $cursor ]]; then
      args+=(-f "after=$cursor")
    fi
    page_file=$(printf '%s/threads-%03d.json' "$WORK_DIR" "$page")
    gh_or_fail "$err" "${args[@]}" >"$page_file" || return 1
    if ! verdict=$(jq -r --arg pr "$PR_NUMBER" --arg slug "$OWNER/$REPO" "$THREAD_PAGE_VERDICT" "$page_file" 2>"$err.stderr"); then
      echo "Failed to parse GraphQL response: $(cat "$err.stderr")" >"$err"
      return 1
    fi
    case $verdict in
      done)
        jq -s "$THREAD_PAGES_MERGE" "$WORK_DIR"/threads-*.json >"$out" || return 1
        fetch_thread_replies "$out" "$err"
        return $?
        ;;
      next:*) cursor=${verdict#next:} ;;
      *)
        echo "${verdict#error:}" >"$err"
        return 1
        ;;
    esac
  done
  echo "PR #$PR_NUMBER has more than $((MAX_THREAD_PAGES * THREADS_PER_PAGE)) review threads; refusing to paginate further" >"$err"
  return 1
}

fetch_thread_replies() {
  local out=$1 err=$2 thread_index thread_id cursor page page_file replies_file tmp_file verdict complete
  local -a args page_files
  while IFS=$'\t' read -r thread_index thread_id cursor; do
    page_files=()
    complete=false
    for ((page = 1; page <= MAX_THREAD_COMMENT_PAGES; page++)); do
      page_file=$(printf '%s/thread-comments-%03d-%03d.json' "$WORK_DIR" "$thread_index" "$page")
      args=(api graphql -f "query=$THREAD_REPLIES_QUERY" -f "threadId=$thread_id")
      if [[ -n $cursor ]]; then
        args+=(-f "after=$cursor")
      fi
      gh_or_fail "$err" "${args[@]}" >"$page_file" || return 1
      if ! verdict=$(jq -r "$THREAD_REPLIES_PAGE_VERDICT" "$page_file" 2>"$err.stderr"); then
        echo "Failed to parse review-thread comments: $(cat "$err.stderr")" >"$err"
        return 1
      fi
      page_files+=("$page_file")
      case $verdict in
        done) complete=true; break ;;
        next:*) cursor=${verdict#next:} ;;
        *) echo "${verdict#error:}" >"$err"; return 1 ;;
      esac
    done
    if [[ $complete != true ]]; then
      echo "Review thread $thread_id has more than $((MAX_THREAD_COMMENT_PAGES * 100)) comments; refusing to paginate further" >"$err"
      return 1
    fi
    replies_file=$(printf '%s/thread-comments-%03d.json' "$WORK_DIR" "$thread_index")
    tmp_file="$WORK_DIR/threads.updated.json"
    jq -s "$THREAD_REPLIES_MERGE" "${page_files[@]}" >"$replies_file" || return 1
    jq --argjson index "$thread_index" --slurpfile extra "$replies_file" \
      '.threads[$index].comments += $extra[0] | .threads[$index].comments_has_next_page = false | .threads[$index].comments_end_cursor = ""' \
      "$out" >"$tmp_file" || return 1
    mv "$tmp_file" "$out"
  done < <(jq -r '.threads | to_entries[] | select(.value.comments_has_next_page) | [.key, .value.id, .value.comments_end_cursor] | @tsv' "$out")
  jq '.threads |= map(del(.comments_has_next_page, .comments_end_cursor))' "$out" >"$WORK_DIR/threads.cleaned.json" || return 1
  mv "$WORK_DIR/threads.cleaned.json" "$out"
}

# The PR file list includes deletions and binaries absent from the unified diff.
fetch_changed_files() {
  local out=$1 err=$2 raw_file="$WORK_DIR/changed-files.raw"
  gh_or_fail "$err" api --paginate "repos/$OWNER/$REPO/pulls/$PR_NUMBER/files?per_page=100" >"$raw_file" || return 1
  if ! jq -s 'add // [] | map(.filename) | unique | sort' "$raw_file" >"$out" 2>"$err.stderr"; then
    echo "Failed to parse changed files: $(cat "$err.stderr")" >"$err"
    return 1
  fi
}

# Reviews come back oldest-first, so stopping at page one would hide the
# NEWEST reviews once a PR exceeds one page.
fetch_reviews() {
  local out=$1 err=$2 page page_file count
  for ((page = 1; page <= MAX_REVIEW_PAGES; page++)); do
    page_file=$(printf '%s/reviews-%03d.json' "$WORK_DIR" "$page")
    gh_or_fail "$err" api "repos/$OWNER/$REPO/pulls/$PR_NUMBER/reviews?per_page=$REVIEWS_PER_PAGE&page=$page" >"$page_file" || return 1
    if ! count=$(jq 'length' "$page_file" 2>"$err.stderr"); then
      echo "Failed to parse reviews: $(cat "$err.stderr")" >"$err"
      return 1
    fi
    if ((count < REVIEWS_PER_PAGE)); then
      jq -s 'add' "$WORK_DIR"/reviews-*.json >"$out"
      return
    fi
  done
  echo "PR #$PR_NUMBER has more than $((MAX_REVIEW_PAGES * REVIEWS_PER_PAGE)) reviews; refusing to paginate further" >"$err"
  return 1
}

fetch_conversation_comments() {
  local out=$1 err=$2 raw_file="$WORK_DIR/issue-comments.raw"
  gh_or_fail "$err" api --paginate "repos/$OWNER/$REPO/issues/$PR_NUMBER/comments?per_page=100" >"$raw_file" || return 1
  if ! jq -s 'add // []' "$raw_file" >"$out" 2>"$err.stderr"; then
    echo "Failed to parse comments: $(cat "$err.stderr")" >"$err"
    return 1
  fi
}

# `gh pr checks` signals state through its exit code (1 = some failed,
# 8 = some pending) while still printing valid JSON, so stdout is parsed first
# and a non-zero exit is only an error when there is nothing to parse.
fetch_checks() {
  local out=$1 err=$2 rc=0
  gh_bounded pr checks "$PR_NUMBER" --repo "$OWNER/$REPO" --json "$CHECK_FIELDS" >"$out" 2>"$err.stderr" || rc=$?
  if [[ -s $out ]] && jq empty "$out" 2>/dev/null; then
    return 0
  fi
  if ((rc == 124)) && [[ -n $TIMEOUT_BIN ]]; then
    echo "gh pr checks timed out after ${GH_TIMEOUT_SECS}s" >"$err"
  elif ((rc != 0)); then
    echo "gh pr checks failed: $(cat "$err.stderr")" >"$err"
  else
    echo "Failed to parse checks output: $(head -c 200 "$out")" >"$err"
  fi
  return 1
}

failure_reason() {
  cat "$1" 2>/dev/null || echo "unknown error"
}

# ============================================================================
# Fix-plan assembly (pure jq over the fetched data)
# ============================================================================

# shellcheck disable=SC2016
readonly BUILD_FIX_PLAN='
def strip: sub("\\A\\s+"; "") | sub("\\s+\\z"; "");

def extension: if contains(".") then split(".")[-1] else "" end;

# Match the full path or a path-segment-boundary suffix, so a short thread path
# ("foo.sql") matches "backend/foo.sql" but "io.go" does NOT match "bio.go".
def path_matches($path): . == $path or endswith("/" + $path);

# "@@ -old_start,old_count +new_start,new_count @@ context" -> new_start
def hunk_new_start: (split("+")[1] // "") | (capture("^(?<start>[0-9]+)") | .start | tonumber) // null;

# Unified diff -> [{path, lines: {"<new-side line number>": content}}].
def parse_diff:
  split("\n")
  | (if .[-1] == "" then .[:-1] else . end)
  | map(rtrimstr("\r"))
  | reduce .[] as $row (
      {files: [], path: null, lines: {}, line_number: 0, in_hunk: false};
      # A new file section resets hunk state so extended headers (mode,
      # rename lines, ...) before the first hunk are never read as content.
      if ($row | startswith("diff --git ")) then .in_hunk = false
      elif ($row | startswith("+++ b/")) then
        ($row[6:]) as $new_path
        | (if .path != null and .path != $new_path then .files += [{path, lines}] | .lines = {} else . end)
        | .path = $new_path
        | .in_hunk = false
      elif ($row | startswith("rename to ")) then
        (if .path != null then .files += [{path, lines}] | .lines = {} else . end)
        | .path = $row[10:]
        | .in_hunk = false
      elif ($row | startswith("--- ")) or ($row | startswith("index ")) then .
      elif ($row | startswith("@@ ")) then
        ($row | hunk_new_start) as $start
        | (if $start != null then .line_number = $start else . end)
        | .in_hunk = true
      elif ($row | startswith("\\ ")) then .
      # Gating on in_hunk keeps a deleted file "+++ /dev/null" line (or any
      # pre-hunk header) out of the previous file line map.
      elif .path != null and .in_hunk then
        if ($row | startswith("+")) then
          .lines[.line_number | tostring] = $row[1:] | .line_number += 1
        elif ($row | startswith("-")) then .
        else
          .lines[.line_number | tostring] = (if ($row | startswith(" ")) then $row[1:] else $row end)
          | .line_number += 1
        end
      else .
      end
    )
  | if .path != null then .files + [{path, lines}] else .files end;

def code_at($files; $path; $line):
  first($files[] | select(.path | path_matches($path)) | .lines[$line | tostring]) // null;

# Lines in *other* changed files with the same extension whose trimmed content
# is identical to the commented line.
def related_lines($pattern_index; $source_path; $code_context):
  (($code_context // "") | strip) as $pattern
  | if $source_path == null or $pattern == "" then []
    else ($source_path | extension) as $source_ext
      | [ $pattern_index[]
          | select(.pattern == $pattern and .ext == $source_ext and (.file | path_matches($source_path) | not))
          | {file, line, code, same_pattern: true} ]
      | sort_by(.file, .line)
    end;

# A null endpoint falls back to the other endpoint of the same range.
def ranges_overlap($start_a; $end_a; $start_b; $end_b):
  ($start_a // $end_a) as $a_start
  | ($end_a // $start_a) as $a_end
  | ($start_b // $end_b) as $b_start
  | ($end_b // $start_b) as $b_end
  | $a_start != null and $a_end != null and $b_start != null and $b_end != null
    and $a_start <= $b_end
    and $b_start <= $a_end;

def comments_overlap($a; $b):
  $a.file != null and $a.file == $b.file
  and ranges_overlap($a.start_line // $a.line; $a.line; $b.start_line // $b.line; $b.line);

def append_unique($items): reduce $items[] as $item (.; if any(.[]; . == $item) then . else . + [$item] end);

def min_optional($a; $b): if $a != null and $b != null then [$a, $b] | min else $a // $b end;
def max_optional($a; $b): if $a != null and $b != null then [$a, $b] | max else $a // $b end;

def merge_into($other):
  .authors |= append_unique($other.authors)
  | .body = .body + "\n---\n" + $other.body
  | .urls += $other.urls
  | .comments += $other.comments
  | .thread_ids |= append_unique($other.thread_ids)
  | .collapsed_count += $other.collapsed_count
  | .thread_comments += $other.thread_comments
  # One fresh thread makes the whole merged entry actionable.
  | .is_outdated = (.is_outdated and $other.is_outdated)
  | .code_context = (.code_context // $other.code_context)
  | .related_lines_in_pr = reduce $other.related_lines_in_pr[] as $related (.related_lines_in_pr;
      if any(.[]; .file == $related.file and .line == $related.line) then . else . + [$related] end)
  | .start_line = min_optional(.start_line; $other.start_line)
  | .line = max_optional(.line; $other.line);

# A merge widens the target range, which can bridge entries that did not
# overlap before; keep absorbing until nothing overlaps .[$target], so the
# result does not depend on the order threads arrive in.
def absorb_overlaps($target):
  . as $entries
  | ([range(0; length) | select(. != $target and comments_overlap($entries[.]; $entries[$target]))][0]) as $other
  | if $other == null then .
    else (.[$target] |= merge_into($entries[$other]))
      | del(.[$other])
      | absorb_overlaps(if $other < $target then $target - 1 else $target end)
    end;

def collapse_same_location:
  reduce .[] as $comment ([];
    . as $collapsed
    | ([range(0; length) | select(comments_overlap($collapsed[.]; $comment))][0]) as $target
    | if $target == null then . + [$comment]
      else (.[$target] |= merge_into($comment)) | absorb_overlaps($target)
      end);

def comment_output:
  {file, line, start_line, authors, body, urls, comments, thread_ids, is_outdated, collapsed_count}
  + (if .code_context != null then {code_context} else {} end)
  + {related_lines_in_pr, thread_comments};

def check_output:
  . as $check
  | {name: (.name // ""), state: (.state // ""), bucket: (.bucket // "")}
  + reduce ("description", "link", "workflow") as $key ({};
      if $check[$key] != null then .[$key] = $check[$key] else . end);

def bucket_count($bucket): map(select(.bucket == $bucket)) | length;

def checks_summary:
  {
    total: length,
    passed: bucket_count("pass"),
    failed: bucket_count("fail"),
    pending: bucket_count("pending"),
    skipped: (bucket_count("skipping") + bucket_count("cancel"))
  };

# PR-level comments not anchored to a diff line: review summaries and
# conversation-tab comments, minus empty bodies, pending reviews, and deploy bots.
def top_level_comments($reviews; $issue_comments):
  ["vercel[bot]", "netlify[bot]", "changeset-bot[bot]"] as $noise_bots
  | [
      ( $reviews[]
        | ((.body // "") | strip) as $body
        | select($body != "" and .state != "PENDING")
        | {source: "review", author: (.user.login // "ghost"), body: $body}
          + (if .html_url != null then {url: .html_url} else {} end)
          + (if .submitted_at != null then {created_at: .submitted_at} else {} end) ),
      ( $issue_comments[]
        | ((.body // "") | strip) as $body
        | (.user.login // "ghost") as $author
        | select($body != "" and (any($noise_bots[]; . == $author) | not))
        | {source: "conversation", author: $author, body: $body}
          + (if .html_url != null then {url: .html_url} else {} end)
          + (if .created_at != null then {created_at: .created_at} else {} end) )
    ];

($diff | parse_diff) as $files
| [ $files[]
    | .path as $file
    | (.path | extension) as $ext
    | .lines | to_entries[]
    | {file: $file, ext: $ext, line: (.key | tonumber), code: .value, pattern: (.value | strip)} ] as $pattern_index
| $thread_data[0] as $pull
| [ $pull.threads[] | select(.is_resolved | not) ] as $unresolved
| ( [ $unresolved[]
      | (if .path != null and .line != null then code_at($files; .path; .line) else null end) as $code_context
      | .comments as $thread_comments
      | {
          file: .path,
          line,
          start_line,
          authors: (reduce $thread_comments[] as $comment ([]; append_unique([$comment.author // "ghost"]))),
          body: ($thread_comments | map(.body // "") | join("\n---\n")),
          urls: [$thread_comments[].url],
          comments: $thread_comments,
          thread_ids: (if .id == "" then [] else [.id] end),
          is_outdated,
          collapsed_count: 1,
          code_context: $code_context,
          related_lines_in_pr: related_lines($pattern_index; .path; $code_context),
          thread_comments: ($thread_comments | length)
        } ]
    | collapse_same_location ) as $collapsed
| ($collapsed | length) as $after_collapsing
| (if $max_comments == 0 then $after_collapsing else $max_comments end) as $cap
| ([0, $after_collapsing - $cap] | max) as $truncated_count
| top_level_comments($reviews[0]; $issue_comments[0]) as $top_level
| {
    success: true,
    pr: $pr,
    owner: $owner,
    repo: $repo,
    head_branch: $pull.head_branch,
    head_sha: $pull.head_sha,
    is_checked_out: ($current_sha != "" and $current_sha == $pull.head_sha),
    changed_files: $changed_files[0],
    unresolved_comments: [$collapsed[:$cap][] | comment_output]
  }
  + (if $top_level == [] then {} else {top_level_comments: $top_level} end)
  + {
      pattern_summary: (
        {
          total_unresolved: ($unresolved | length),
          after_collapsing: $after_collapsing,
          with_cross_file_patterns: ($collapsed | map(select(.related_lines_in_pr != [])) | length)
        }
        + (if $truncated_count > 0 then {truncated_count: $truncated_count} else {} end)
      ),
      checks: ($checks[0] | map(check_output)),
      checks_summary: ($checks[0] | checks_summary)
    }
  + (if $checks_fetch_failed then {checks_fetch_failed: true} else {} end)
  + (if $reviews_fetch_failed then {reviews_fetch_failed: true} else {} end)
  + (if $conversation_comments_fetch_failed then {conversation_comments_fetch_failed: true} else {} end)
'

# shellcheck disable=SC2016
readonly RENDER_HUMAN='
def bar: "════════════════════════════════════════════════════════════";

def body_lines:
  (if length > 500 then .[:500] + "... (truncated)" else . end)
  | split("\n") | (if .[-1] == "" then .[:-1] else . end)
  | .[] | "     " + rtrimstr("\r");

def location:
  if .file != null and .line != null then "\(.file):\(.line)"
  elif .file != null then .file
  else "(no file)"
  end;

def checks_lines:
  if length == 0 then "    No checks found."
  else
    (map(select(.bucket == "pass")) | length) as $passed
    | map(select(.bucket == "fail")) as $failed
    | map(select(.bucket == "pending")) as $pending
    | (map(select(.bucket == "skipping" or .bucket == "cancel")) | length) as $skipped
    | (if ($failed | length) == 0 and ($pending | length) == 0 then "    \($passed)/\(length) passing"
       elif ($failed | length) > 0 then "    \($failed | length) failed, \($passed) passed, \($pending | length) pending"
       else "    \($passed) passed, \($pending | length) pending"
       end),
      (if $skipped > 0 then "    \($skipped) skipped/cancelled" else empty end),
      ($failed[] | "    ✗ \(.name)\(if (.link // "") != "" then " → \(.link)" else "" end)"),
      ($pending[] | "    ● \(.name)")
  end;

(.top_level_comments // []) as $top_level
| "",
  bar,
  "  \(.owner)/\(.repo)#\(.pr) Fix Plan",
  bar,
  "  Branch: \(.head_branch) (\(if .is_checked_out then "checked out"
      else "NOT checked out — current: \(if $current_branch == "" then "unknown" else $current_branch end)" end))",
  (if (.reviews_fetch_failed // false) or (.conversation_comments_fetch_failed // false) then "  WARNING: PR feedback is incomplete; do not treat this plan as complete." else empty end),
  "",
  "  Changed Files (\(.changed_files | length)):",
  (.changed_files[] | "    " + .),
  "",
  "  Unresolved Comments (\(.unresolved_comments | length)):",
  (if .unresolved_comments == [] then "    No unresolved comments."
   else
     .unresolved_comments | to_entries[] | .key as $index | .value
     | "",
       "  \($index + 1). \(location)\(if .collapsed_count > 1 then " [collapsed: \(.collapsed_count) comments]" else "" end)",
       "     \(.authors | map("@" + .) | join(", ")) (\(.thread_comments) comments in thread):",
       (.body | body_lines),
       (if .code_context != null then "     Code: \(.code_context)" else empty end),
       (if .related_lines_in_pr != [] then
          "     Related patterns:", (.related_lines_in_pr[] | "       → \(.file):\(.line)")
        else empty end)
   end),
  (if $top_level != [] then
     "",
     "  Top-Level Comments (Outside Diff) (\($top_level | length)):",
     ($top_level | to_entries[] | .key as $index | .value
      | "",
        "  \($index + 1). @\(.author) [\(.source)] (\((.created_at // "")[:10]))",
        (.body | body_lines))
   else empty end),
  "",
  "  Pattern Summary:",
  "    Total unresolved: \(.pattern_summary.total_unresolved)",
  "    After collapsing: \(.pattern_summary.after_collapsing)",
  (if $top_level != [] then "    Top-level comments: \($top_level | length)" else empty end),
  "    With cross-file patterns: \(.pattern_summary.with_cross_file_patterns)",
  "",
  "  CI CHECKS",
  (.checks | checks_lines),
  "",
  bar,
  (if (.pattern_summary.truncated_count // 0) > 0 then
     "",
     "  Note: \(.pattern_summary.truncated_count) additional comment(s) dropped by --max-comments \($max_comments)."
   else empty end),
  ""
'

# ============================================================================
# Main
# ============================================================================

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/pr-fix-plan.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT

fetch_review_threads "$WORK_DIR/threads.json" "$WORK_DIR/threads.err" &
threads_pid=$!
gh_or_fail "$WORK_DIR/diff.err" pr diff "$PR_NUMBER" --repo "$OWNER/$REPO" >"$WORK_DIR/diff.txt" &
diff_pid=$!
fetch_changed_files "$WORK_DIR/changed-files.json" "$WORK_DIR/changed-files.err" &
files_pid=$!
fetch_checks "$WORK_DIR/checks.json" "$WORK_DIR/checks.err" &
checks_pid=$!
fetch_reviews "$WORK_DIR/reviews.json" "$WORK_DIR/reviews.err" &
reviews_pid=$!
fetch_conversation_comments "$WORK_DIR/issue-comments.json" "$WORK_DIR/issue-comments.err" &
comments_pid=$!

current_branch=$(git branch --show-current 2>/dev/null || true)
current_sha=$(git rev-parse HEAD 2>/dev/null || true)

# Threads, diff, and changed-file metadata are required to build a complete plan.
fatal_error=""
if ! wait "$threads_pid"; then
  fatal_error=$(failure_reason "$WORK_DIR/threads.err")
fi
if ! wait "$diff_pid" && [[ -z $fatal_error ]]; then
  fatal_error=$(failure_reason "$WORK_DIR/diff.err")
fi
if ! wait "$files_pid" && [[ -z $fatal_error ]]; then
  fatal_error=$(failure_reason "$WORK_DIR/changed-files.err")
fi

checks_fetch_failed=false
if ! wait "$checks_pid"; then
  warn "could not fetch checks: $(failure_reason "$WORK_DIR/checks.err")"
  echo '[]' >"$WORK_DIR/checks.json"
  checks_fetch_failed=true
fi
reviews_fetch_failed=false
if ! wait "$reviews_pid"; then
  warn "could not fetch reviews: $(failure_reason "$WORK_DIR/reviews.err")"
  echo '[]' >"$WORK_DIR/reviews.json"
  reviews_fetch_failed=true
fi
conversation_comments_fetch_failed=false
if ! wait "$comments_pid"; then
  warn "could not fetch comments: $(failure_reason "$WORK_DIR/issue-comments.err")"
  echo '[]' >"$WORK_DIR/issue-comments.json"
  conversation_comments_fetch_failed=true
fi

[[ -z $fatal_error ]] || die "$fatal_error"

jq -n \
  --argjson pr "$PR_NUMBER" \
  --arg owner "$OWNER" \
  --arg repo "$REPO" \
  --arg current_branch "$current_branch" \
  --arg current_sha "$current_sha" \
  --argjson max_comments "$MAX_COMMENTS" \
  --argjson checks_fetch_failed "$checks_fetch_failed" \
  --argjson reviews_fetch_failed "$reviews_fetch_failed" \
  --argjson conversation_comments_fetch_failed "$conversation_comments_fetch_failed" \
  --slurpfile thread_data "$WORK_DIR/threads.json" \
  --slurpfile changed_files "$WORK_DIR/changed-files.json" \
  --rawfile diff "$WORK_DIR/diff.txt" \
  --slurpfile checks "$WORK_DIR/checks.json" \
  --slurpfile reviews "$WORK_DIR/reviews.json" \
  --slurpfile issue_comments "$WORK_DIR/issue-comments.json" \
  "$BUILD_FIX_PLAN" >"$WORK_DIR/plan.json"

if [[ $AS_JSON == true ]]; then
  cat "$WORK_DIR/plan.json"
else
  jq -r \
    --arg current_branch "$current_branch" \
    --argjson max_comments "$MAX_COMMENTS" \
    "$RENDER_HUMAN" "$WORK_DIR/plan.json"
fi
