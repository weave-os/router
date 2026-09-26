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
	"$repo/cmd/router-gateway" \
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
printf 'package main\n' >"$repo/cmd/router-gateway/main.go"
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

closure_file="$work/gateway-closure-dirs"
# internal/router is in the gateway closure while the nested
# internal/router/planner package is not, mirroring the real closure.
printf '%s\n' cmd/router-gateway internal/policyregistry internal/router >"$closure_file"

# Each case commits its edits on a branch off the same base, so the fixtures
# stay independent.
run_case() {
	local name=$1 expected_go=$2 expected_gateway=$3
	shift 3

	git -C "$repo" checkout --quiet -B "case" "$base"
	"$@"
	git -C "$repo" add --all
	git -C "$repo" commit --quiet -m "$name"
	local head
	head=$(git -C "$repo" rev-parse HEAD)

	local got_go got_gateway
	got_go=$(cd "$repo" && "$script" changed "$base" "$head")
	got_gateway=$(cd "$repo" && GO_CI_GATEWAY_CLOSURE_DIRS_FILE="$closure_file" \
		"$script" gateway-closure "$base" "$head")

	if [[ "$got_go" != "go_changed=$expected_go" ]]; then
		echo "case $name: expected go_changed=$expected_go, got $got_go" >&2
		exit 1
	fi
	if [[ "$got_gateway" != "gateway_closure_changed=$expected_gateway" ]]; then
		echo "case $name: expected gateway_closure_changed=$expected_gateway, got $got_gateway" >&2
		exit 1
	fi
}

edit() {
	local path=$1
	mkdir -p "$(dirname "$repo/$path")"
	printf 'changed %s\n' "$(date +%s%N)" >>"$repo/$path"
}

run_case readme-only false false edit README.md
run_case docs-only false false bash -c "
	printf 'more\n' >>'$repo/docs/SERVING_CONTROL.md'
	printf 'guide\n' >'$repo/docs/CONFIGURATION.md'
"
run_case gateway-closure-go true true edit internal/policyregistry/registry.go
run_case worker-only-go true false edit internal/router/planner/planner.go
run_case worker-only-embed true false edit internal/router/planner/artifacts/model.txt
run_case closure-package-go true true edit internal/router/router.go
run_case embedded-markdown true false edit internal/router/llmescalation/prompt.md
run_case go-mod true true edit go.mod
run_case generated-sqlc true false edit internal/sqlc/models.go
run_case hmm-sidecar-only false false edit sidecars/hmm/policy.py
run_case installer-only false false edit install/README.md
# Artifacts cmd/genprices regenerates from the Go catalog and its tests assert.
run_case installer-price-block true false edit install/install.sh
run_case pi-pricing-artifact true false edit install/pi-router/src/pricing.generated.ts
run_case bench-pricing-artifact true false edit bench/weave_bench/prices.generated.json
# A move reports both paths, so leaving a Go package still gates ON.
run_case embed-moved-out-of-package true false bash -c "
	git -C '$repo' mv internal/router/llmescalation/prompt.md docs/prompt.md
"
run_case migration true false edit db/migrations/0001_init.up.sql
run_case makefile true false edit Makefile
run_case dockerfile true false edit Dockerfile
run_case workflow-self true false edit .github/workflows/test.yml
run_case other-workflow false false edit .github/workflows/smoke.yml
run_case classifier-script true false edit scripts/go_ci_relevance.sh

# No base (push / workflow_dispatch) fails closed in both modes.
head=$(git -C "$repo" rev-parse HEAD)
[[ "$(cd "$repo" && "$script" changed "" "$head")" == "go_changed=true" ]]
[[ "$(cd "$repo" && "$script" gateway-closure "" "$head")" == "gateway_closure_changed=true" ]]
# An unreachable closure listing fails closed rather than skipping the build.
[[ "$(cd "$repo" && GO_CI_GATEWAY_CLOSURE_DIRS_FILE="$work/missing" \
	"$script" gateway-closure "$base" "$head" 2>/dev/null)" == "gateway_closure_changed=true" ]]

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

# The fixture closure above mirrors the real one: assert the two packages the
# cases rely on are classified the same way by `go list` in this repository.
if command -v go >/dev/null; then
	closure=$(cd "$repo_root" && CGO_ENABLED=0 go list -deps ./cmd/router-gateway)
	grep -qx 'weave-os/router/internal/policyregistry' <<<"$closure"
	grep -qx 'weave-os/router/internal/router' <<<"$closure"
	if grep -qx 'weave-os/router/internal/router/planner' <<<"$closure"; then
		echo "internal/router/planner entered the gateway closure; pick another worker-only package" >&2
		exit 1
	fi
else
	echo "go toolchain unavailable: skipped the real gateway closure assertions" >&2
fi

echo "Go CI relevance classifier tests passed"
