#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/go_ci_relevance.sh"
repo_root=$(cd "$script_dir/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
mkdir -p "$repo"
git -C "$repo" init --quiet
git -C "$repo" config user.name "Go Relevance Test"
git -C "$repo" config user.email "go-relevance@example.test"

mkdir -p \
	"$repo/cmd/router" \
	"$repo/internal/policyregistry" \
	"$repo/internal/router/planner" \
	"$repo/internal/router/llmescalation" \
	"$repo/internal/sqlc" \
	"$repo/db/migrations" \
	"$repo/docs" \
	"$repo/scripts" \
	"$repo/sidecars/hmm" \
	"$repo/install/pi-router/src" \
	"$repo/bench/weave_bench"
printf 'module example.test\n\ngo 1.25.0\n' >"$repo/go.mod"
printf 'package main\n' >"$repo/cmd/router/main.go"
printf 'package policyregistry\n' >"$repo/internal/policyregistry/registry.go"
printf 'package router\n' >"$repo/internal/router/router.go"
printf 'package planner\n' >"$repo/internal/router/planner/planner.go"
mkdir -p "$repo/internal/router/planner/artifacts"
printf 'model\n' >"$repo/internal/router/planner/artifacts/model.txt"
printf 'package llmescalation\n' >"$repo/internal/router/llmescalation/contracts.go"
printf 'escalate\n' >"$repo/internal/router/llmescalation/prompt.md"
printf 'package sqlc\n' >"$repo/internal/sqlc/models.go"
printf 'BEGIN; COMMIT;\n' >"$repo/db/migrations/0001_init.up.sql"
printf '# Docs\n' >"$repo/docs/SERVING_CONTROL.md"
printf '# Router\n' >"$repo/README.md"
printf 'test:\n\ttrue\n' >"$repo/Makefile"
printf 'FROM scratch\n' >"$repo/Dockerfile"
printf 'print("hmm")\n' >"$repo/sidecars/hmm/policy.py"
printf '#!/usr/bin/env bash\nexit 0\n' >"$repo/install/install.sh"
printf '# Installer\n' >"$repo/install/README.md"
printf 'export const PRICING = {};\n' >"$repo/install/pi-router/src/pricing.generated.ts"
printf '{}\n' >"$repo/bench/weave_bench/prices.generated.json"
git -C "$repo" add .
git -C "$repo" commit --quiet -m base
base=$(git -C "$repo" rev-parse HEAD)

# Each case commits its edits on a branch off the same base, so the fixtures
# stay independent.
run_case() {
	local name=$1 expected_go=$2
	shift 2

	git -C "$repo" checkout --quiet -B "case" "$base"
	"$@"
	git -C "$repo" add --all
	git -C "$repo" commit --quiet -m "$name"
	local head
	head=$(git -C "$repo" rev-parse HEAD)

	local got_go
	got_go=$(cd "$repo" && "$script" changed "$base" "$head")

	if [[ "$got_go" != "go_changed=$expected_go" ]]; then
		echo "case $name: expected go_changed=$expected_go, got $got_go" >&2
		exit 1
	fi
}

edit() {
	local path=$1
	mkdir -p "$(dirname "$repo/$path")"
	printf 'changed %s\n' "$(date +%s%N)" >>"$repo/$path"
}

run_case readme-only false edit README.md
run_case docs-only false bash -c "
	printf 'more\n' >>'$repo/docs/SERVING_CONTROL.md'
	printf 'guide\n' >'$repo/docs/CONFIGURATION.md'
"
run_case registry-go true edit internal/policyregistry/registry.go
run_case worker-only-go true edit internal/router/planner/planner.go
run_case worker-only-embed true edit internal/router/planner/artifacts/model.txt
run_case closure-package-go true edit internal/router/router.go
run_case embedded-markdown true edit internal/router/llmescalation/prompt.md
run_case go-mod true edit go.mod
run_case generated-sqlc true edit internal/sqlc/models.go
run_case hmm-sidecar-only false edit sidecars/hmm/policy.py
run_case installer-only false edit install/README.md
# Artifacts cmd/genprices regenerates from the Go catalog and its tests assert.
run_case installer-price-block true edit install/install.sh
run_case pi-pricing-artifact true edit install/pi-router/src/pricing.generated.ts
run_case bench-pricing-artifact true edit bench/weave_bench/prices.generated.json
# A move reports both paths, so leaving a Go package still gates ON.
run_case embed-moved-out-of-package true bash -c "
	git -C '$repo' mv internal/router/llmescalation/prompt.md docs/prompt.md
"
run_case migration true edit db/migrations/0001_init.up.sql
run_case makefile true edit Makefile
run_case dockerfile true edit Dockerfile
run_case workflow-self true edit .github/workflows/test.yml
run_case other-workflow false edit .github/workflows/smoke.yml
run_case classifier-script true edit scripts/go_ci_relevance.sh

# No base (push / workflow_dispatch) fails closed in both modes.
head=$(git -C "$repo" rev-parse HEAD)
[[ "$(cd "$repo" && "$script" changed "" "$head")" == "go_changed=true" ]]
# Git failures are unclassifiable, not a clean "nothing to do".
mkdir -p "$work/bin"
real_git=$(command -v git)
{
	echo '#!/usr/bin/env bash'
	# shellcheck disable=SC2016 # the stub, not this script, expands these.
	echo 'if [[ "$1" == "${FAIL_GIT_SUBCOMMAND:-}" ]]; then'
	echo '	echo "simulated git failure" >&2'
	echo '	exit 1'
	echo 'fi'
	echo "exec $real_git \"\$@\""
} >"$work/bin/git"
chmod +x "$work/bin/git"
for subcommand in diff ls-tree; do
	got=$(cd "$repo" && PATH="$work/bin:$PATH" FAIL_GIT_SUBCOMMAND="$subcommand" \
		"$script" changed "$base" "$head" 2>/dev/null)
	if [[ "$got" != "go_changed=true" ]]; then
		echo "failing git $subcommand: expected go_changed=true, got $got" >&2
		exit 1
	fi
done

# The generated-artifact list must track cmd/genprices, which writes these
# files and whose tests assert they match the Go price catalog.
while IFS= read -r artifact; do
	if ! grep -qE "^[[:space:]]*${artifact//\//\\/}\$" "$script"; then
		echo "cmd/genprices writes $artifact but the classifier does not gate on it" >&2
		exit 1
	fi
done < <(grep -oE '"(install|bench)/[^"]+"' "$repo_root/cmd/genprices/main.go" |
	tr -d '"' | sort -u)

echo "Go CI relevance classifier tests passed"
