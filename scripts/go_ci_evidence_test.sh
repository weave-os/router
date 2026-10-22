#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/go_ci_evidence.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

repository=example/router
repo="$work/repo"
git init --quiet "$repo"
git -C "$repo" config user.name "Go Evidence Test"
git -C "$repo" config user.email "go-evidence@example.test"
printf 'base\n' >"$repo/file.txt"
git -C "$repo" add file.txt
git -C "$repo" commit --quiet -m base
printf 'fix\n' >>"$repo/file.txt"
git -C "$repo" commit --quiet -am "squash merge"
squash=$(git -C "$repo" rev-parse HEAD)
tree=$(git -C "$repo" rev-parse "HEAD^{tree}")
parent_tree=$(git -C "$repo" rev-parse "HEAD^1^{tree}")
head_sha=1111111111111111111111111111111111111111

# A stand-in for gh: `api` answers from per-endpoint JSON fixtures through the
# real jq, and `run download` copies a per-run artifact directory.
fake_bin="$work/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
	api)
		[[ $3 == --jq ]] || exit 64
		fixture="$FAKE_GH/api/$(printf '%s' "$2" | tr -c 'A-Za-z0-9' '_').json"
		[[ -f $fixture ]] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
		jq -r "$4" "$fixture"
		;;
	run)
		[[ $2 == download && $4 == --repo && $6 == --name && $8 == --dir ]] || exit 64
		[[ $5 == "$FAKE_GH_REPOSITORY" ]] || exit 1
		source="$FAKE_GH/artifacts/$3/$7"
		[[ -d $source ]] || { echo "no artifact matches $7" >&2; exit 1; }
		cp -R "$source/." "$9"
		;;
	*) exit 64 ;;
esac
GH
chmod +x "$fake_bin/gh"

fixture_path() {
	printf '%s/api/%s.json' "$FAKE_GH" "$(printf '%s' "$1" | tr -c 'A-Za-z0-9' '_')"
}

pulls_endpoint="repos/$repository/commits/$squash/pulls"
runs_endpoint="repos/$repository/actions/workflows/test.yml/runs?event=pull_request&head_sha=$head_sha&per_page=100"
jobs_endpoint="repos/$repository/actions/runs/42/jobs?per_page=100"

pull_json() {
	local merge_sha=$1 head_repository=$2
	printf '[{"number":7,"merged_at":"2026-10-14T17:02:32Z","merge_commit_sha":"%s","head":{"sha":"%s","repo":{"full_name":"%s"}}}]' \
		"$merge_sha" "$head_sha" "$head_repository"
}

run_json() {
	local id=$1 number=$2 status=$3 conclusion=$4 head_repository=$5
	printf '{"id":%s,"run_number":%s,"status":"%s","conclusion":"%s","head_repository":{"full_name":"%s"}}' \
		"$id" "$number" "$status" "$conclusion" "$head_repository"
}

# A fixture where every reuse condition holds; each case breaks one.
reset_fixture() {
	export FAKE_GH="$work/fake-$RANDOM$RANDOM" FAKE_GH_REPOSITORY="$repository"
	mkdir -p "$FAKE_GH/api" "$FAKE_GH/artifacts/42/go-checks-evidence"
	pull_json "$squash" "$repository" >"$(fixture_path "$pulls_endpoint")"
	printf '{"workflow_runs":[%s,%s]}' \
		"$(run_json 41 10 completed failure "$repository")" \
		"$(run_json 42 11 completed success "$repository")" >"$(fixture_path "$runs_endpoint")"
	printf '{"jobs":[{"name":"Classify changed paths","conclusion":"success"},{"name":"Go checks","conclusion":"success"}]}' \
		>"$(fixture_path "$jobs_endpoint")"
	write_evidence "$tree" "$parent_tree"
}

write_evidence() {
	jq -n --arg tree "$1" --arg parent_tree "$2" '{tree: $tree, parent_tree: $parent_tree}' \
		>"$FAKE_GH/artifacts/42/go-checks-evidence/evidence"
}

failures=0
run_case() {
	local name=$1 expected=$2 expected_reason=$3
	shift 3
	reset_fixture
	"$@"
	local output stderr_file="$work/stderr"
	if ! output=$(cd "$repo" && PATH="$fake_bin:$PATH" bash "$script" reuse "$repository" "$squash" 2>"$stderr_file"); then
		echo "FAIL $name: script exited non-zero" >&2
		cat "$stderr_file" >&2
		failures=$((failures + 1))
		return
	fi
	if [[ $output != "go_evidence_reused=$expected" ]]; then
		echo "FAIL $name: got '$output', want go_evidence_reused=$expected" >&2
		cat "$stderr_file" >&2
		failures=$((failures + 1))
		return
	fi
	if ! grep -qF -- "$expected_reason" "$stderr_file"; then
		echo "FAIL $name: stderr lacks '$expected_reason'" >&2
		cat "$stderr_file" >&2
		failures=$((failures + 1))
		return
	fi
	echo "ok $name"
}

no_change() { :; }
remove_fixture() { rm -f "$(fixture_path "$1")"; }
set_pulls() { printf '%s' "$1" >"$(fixture_path "$pulls_endpoint")"; }
set_runs() { printf '{"workflow_runs":[%s]}' "$1" >"$(fixture_path "$runs_endpoint")"; }
set_go_checks() {
	printf '{"jobs":[{"name":"Go checks","conclusion":"%s"}]}' "$1" >"$(fixture_path "$jobs_endpoint")"
}
remove_go_checks() { printf '{"jobs":[]}' >"$(fixture_path "$jobs_endpoint")"; }
remove_artifact() { rm -rf "$FAKE_GH/artifacts/42"; }
remove_evidence_file() { rm -f "$FAKE_GH/artifacts/42/go-checks-evidence/evidence"; }
write_raw_evidence() { printf '%b' "$1" >"$FAKE_GH/artifacts/42/go-checks-evidence/evidence"; }

run_case "matching merged pull request evidence is reused" true "Reusing Go checks from Test run 42 of #7" no_change
run_case "pull request lookup failure keeps checks on" false "pull request lookup failed" remove_fixture "$pulls_endpoint"
run_case "direct push without a pull request keeps checks on" false "no single pull request merged" set_pulls '[]'
run_case "pull request merged as another commit keeps checks on" false "no single pull request merged" \
	set_pulls "$(pull_json 2222222222222222222222222222222222222222 "$repository")"
run_case "unmerged associated pull request keeps checks on" false "no single pull request merged" \
	set_pulls "[{\"number\":7,\"merged_at\":null,\"merge_commit_sha\":\"$squash\",\"head\":{\"sha\":\"$head_sha\",\"repo\":{\"full_name\":\"$repository\"}}}]"
run_case "fork pull request keeps checks on" false "comes from fork/router" \
	set_pulls "$(pull_json "$squash" fork/router)"
run_case "run lookup failure keeps checks on" false "Test run lookup failed" remove_fixture "$runs_endpoint"
run_case "pull request without a Test run keeps checks on" false "has no Test run" set_runs ''
run_case "older success does not mask a failed latest run" false "latest Test run 43 for #7 is completed/failure" \
	set_runs "$(run_json 42 11 completed success "$repository"),$(run_json 43 12 completed failure "$repository")"
run_case "in-progress latest run keeps checks on" false "latest Test run 43 for #7 is in_progress/" \
	set_runs "$(run_json 42 11 completed success "$repository"),$(run_json 43 12 in_progress "" "$repository")"
run_case "cancelled latest run keeps checks on" false "is completed/cancelled" \
	set_runs "$(run_json 42 11 completed cancelled "$repository")"
run_case "fork-owned run is never evidence" false "latest Test run 41 for #7 is completed/failure" \
	set_runs "$(run_json 41 10 completed failure "$repository"),$(run_json 99 50 completed success fork/router)"
run_case "job lookup failure keeps checks on" false "job lookup for Test run 42 failed" remove_fixture "$jobs_endpoint"
run_case "skipped Go checks are not evidence" false "Go checks in Test run 42 concluded 'skipped'" set_go_checks skipped
run_case "failed Go checks are not evidence" false "concluded 'failure'" set_go_checks failure
run_case "missing Go checks job keeps checks on" false "concluded 'missing'" \
	remove_go_checks
run_case "missing artifact keeps checks on" false "has no go-checks-evidence artifact" \
	remove_artifact
run_case "artifact without an evidence file keeps checks on" false "artifact has no evidence file" \
	remove_evidence_file
run_case "different tested tree keeps checks on" false "tested tree $parent_tree" write_evidence "$parent_tree" "$parent_tree"
run_case "same tree on a different parent keeps checks on" false "tested on parent tree $tree" write_evidence "$tree" "$tree"
run_case "malformed evidence keeps checks on" false "tested tree unknown" \
	write_raw_evidence "tree $tree\nparent_tree $parent_tree\n"
run_case "evidence without a parent tree keeps checks on" false "tested on parent tree unknown" \
	write_raw_evidence "{\"tree\": \"$tree\"}"
run_case "non-object evidence keeps checks on" false "tested tree unknown" \
	write_raw_evidence "[\"$tree\"]"

reset_fixture
if output=$(cd "$repo" && PATH="$fake_bin:$PATH" bash "$script" reuse "$repository" 3333333333333333333333333333333333333333 2>/dev/null) &&
	[[ $output == go_evidence_reused=false ]]; then
	echo "ok unreadable commit keeps checks on"
else
	echo "FAIL unreadable commit keeps checks on: got '$output'" >&2
	failures=$((failures + 1))
fi

if bash "$script" changed "$repository" "$squash" >/dev/null 2>&1; then
	echo "FAIL unknown mode is rejected" >&2
	failures=$((failures + 1))
else
	echo "ok unknown mode is rejected"
fi

if [[ $failures -ne 0 ]]; then
	echo "$failures case(s) failed" >&2
	exit 1
fi
