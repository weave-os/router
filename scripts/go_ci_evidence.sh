#!/usr/bin/env bash
# Decides whether a main push can reuse the Go checks of the pull request it
# squash-merged instead of rerunning them.
#
#   go_ci_evidence.sh reuse <repository> <commit>
#       go_evidence_reused=true|false
#
# Reuse requires the merged pull request's latest same-repository Test run to
# have succeeded with Go checks run, on a merge commit whose tree and first
# parent tree both equal the pushed commit's. Every other outcome, including
# any API or git failure, keeps the checks ON.
set -euo pipefail

if [[ $# -ne 3 || $1 != reuse ]]; then
	echo "usage: $0 reuse <repository> <commit>" >&2
	exit 2
fi

repository=$2
commit=$3
artifact_name=go-checks-evidence

reject() {
	echo "Go checks evidence not reused: $1" >&2
	echo "go_evidence_reused=false"
	exit 0
}

tree=$(git rev-parse --verify --quiet "${commit}^{tree}") || reject "commit $commit is unreadable"
parent_tree=$(git rev-parse --verify --quiet "${commit}^1^{tree}") || reject "commit $commit has no readable first parent"

pull_request=$(gh api "repos/$repository/commits/$commit/pulls" \
	--jq "[.[] | select(.merged_at != null and .merge_commit_sha == \"$commit\")] | if length == 1 then .[0] else empty end | [.number, .head.sha, .head.repo.full_name] | @tsv") ||
	reject "pull request lookup failed"
[[ -n $pull_request ]] || reject "no single pull request merged as $commit"
IFS=$'\t' read -r pull_number head_sha head_repository <<<"$pull_request"
[[ $head_repository == "$repository" ]] || reject "pull request #$pull_number comes from $head_repository"

test_run=$(gh api "repos/$repository/actions/workflows/test.yml/runs?event=pull_request&head_sha=$head_sha&per_page=100" \
	--jq "[.workflow_runs[] | select(.head_repository.full_name == \"$repository\")] | max_by(.run_number) // empty | [.id, .status, .conclusion] | @tsv") ||
	reject "Test run lookup failed"
[[ -n $test_run ]] || reject "pull request #$pull_number has no Test run for $head_sha"
IFS=$'\t' read -r run_id run_status run_conclusion <<<"$test_run"
[[ $run_status == completed && $run_conclusion == success ]] ||
	reject "latest Test run $run_id for #$pull_number is $run_status/$run_conclusion"

go_checks=$(gh api "repos/$repository/actions/runs/$run_id/jobs?per_page=100" \
	--jq '[.jobs[] | select(.name == "Go checks")] | if length == 1 then .[0].conclusion else empty end') ||
	reject "job lookup for Test run $run_id failed"
[[ $go_checks == success ]] || reject "Go checks in Test run $run_id concluded '${go_checks:-missing}'"

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
gh run download "$run_id" --repo "$repository" --name "$artifact_name" --dir "$scratch" >/dev/null 2>&1 ||
	reject "Test run $run_id has no $artifact_name artifact"
[[ -f $scratch/evidence ]] || reject "Test run $run_id artifact has no evidence file"

tested_tree=$(jq -r '.tree // empty' "$scratch/evidence" 2>/dev/null || true)
tested_parent_tree=$(jq -r '.parent_tree // empty' "$scratch/evidence" 2>/dev/null || true)
[[ $tested_tree == "$tree" ]] || reject "Test run $run_id tested tree ${tested_tree:-unknown}, $commit has $tree"
[[ $tested_parent_tree == "$parent_tree" ]] ||
	reject "Test run $run_id tested on parent tree ${tested_parent_tree:-unknown}, $commit has $parent_tree"

echo "Reusing Go checks from Test run $run_id of #$pull_number for $commit" >&2
echo "go_evidence_reused=true"
