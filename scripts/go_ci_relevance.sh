#!/usr/bin/env bash
# Classifies a base..head diff for the Test workflow's Go gates.
#
#   go_ci_relevance.sh changed <base> <head>
#       go_changed=true|false — whether the Go checks must run.
#   go_ci_relevance.sh gateway-closure <base> <head>
#       gateway_closure_changed=true|false — whether the standalone gateway
#       build must run.
#
# Both modes fail closed: an empty base, an unreadable commit, or an
# uncomputable package closure gates the checks ON.
set -euo pipefail

if [[ $# -ne 3 ]]; then
	echo "usage: $0 <changed|gateway-closure> <base-commit> <head-commit>" >&2
	exit 2
fi

mode=$1
base=$2
head=$3

case "$mode" in
	changed) output_name=go_changed ;;
	gateway-closure) output_name=gateway_closure_changed ;;
	*)
		echo "unknown mode: $mode" >&2
		exit 2
		;;
esac

gate_on() {
	echo "$output_name=true"
	exit 0
}

# Push, merge-queue and workflow_dispatch runs have no base to diff against.
if [[ -z "$base" || -z "$head" ]]; then
	gate_on
fi
git cat-file -e "${base}^{commit}" 2>/dev/null || gate_on
git cat-file -e "${head}^{commit}" 2>/dev/null || gate_on

changed_files=()
while IFS= read -r -d '' file; do
	changed_files+=("$file")
done < <(git diff --name-only -z "${base}...${head}" --)

if [[ ${#changed_files[@]} -eq 0 ]]; then
	echo "$output_name=false"
	exit 0
fi

# go.mod / go.sum / go.work* change the build for every package, including the
# gateway's.
is_module_file() {
	case "$1" in
		go.mod | go.sum | go.work*) return 0 ;;
	esac
	return 1
}

# Directories holding at least one Go source file at head. A file's owning
# package is its nearest such ancestor: embedded assets live next to or below
# the package that embeds them, so this resolves //go:embed inputs without a
# toolchain.
declare -A go_package_dirs=()
load_go_package_dirs() {
	while IFS= read -r -d '' file; do
		[[ "$file" == *.go && "$file" == */* ]] || continue
		go_package_dirs["${file%/*}"]=1
	done < <(git ls-tree -r -z --name-only "$head")
}

# Prints the owning package directory of "$1", or nothing when the file sits
# outside every Go package.
owning_package_dir() {
	local dir=$1
	while [[ "$dir" == */* ]]; do
		dir=${dir%/*}
		if [[ -n "${go_package_dirs["$dir"]:-}" ]]; then
			echo "$dir"
			return 0
		fi
	done
	return 1
}

if [[ "$mode" == changed ]]; then
	load_go_package_dirs

	for file in "${changed_files[@]}"; do
		if is_module_file "$file"; then
			gate_on
		fi

		case "$file" in
			*.go | Makefile | .github/workflows/test.yml | db/* | scripts/* | internal/sqlc/*)
				gate_on
				;;
			*/sqlc.yml | */sqlc.yaml | sqlc.yml | sqlc.yaml | .golangci.yml | .golangci.yaml)
				gate_on
				;;
		esac
		case "${file##*/}" in
			Dockerfile*) gate_on ;;
		esac

		# Anything inside a Go package directory (or below it) can be compiled
		# in: source, embedded prompts, embedded model artifacts, testdata.
		if owning_package_dir "$file" >/dev/null; then
			gate_on
		fi
	done

	echo "go_changed=false"
	exit 0
fi

declare -A closure_dirs=()
closure_listing=""
if [[ -n "${GO_CI_GATEWAY_CLOSURE_DIRS_FILE:-}" ]]; then
	closure_listing=$(cat "$GO_CI_GATEWAY_CLOSURE_DIRS_FILE") || gate_on
else
	repo_root=$(git rev-parse --show-toplevel) || gate_on
	closure_listing=$(go list -deps -f '{{.Dir}}' ./cmd/router-gateway 2>/dev/null) || gate_on
	# Keep module-local packages only; stdlib and module-cache dependencies
	# cannot be touched by a diff in this repository.
	closure_listing=$(printf '%s\n' "$closure_listing" |
		sed -n "s|^${repo_root}/||p")
fi

while IFS= read -r dir; do
	[[ -n "$dir" ]] || continue
	closure_dirs["$dir"]=1
done <<<"$closure_listing"

[[ ${#closure_dirs[@]} -gt 0 ]] || gate_on

load_go_package_dirs

for file in "${changed_files[@]}"; do
	if is_module_file "$file"; then
		gate_on
	fi
	# Match the owning package exactly: a nested package such as
	# internal/router/planner is not part of internal/router's compilation
	# unit, so ancestor containment would over-gate every worker-only change.
	owner=$(owning_package_dir "$file") || continue
	if [[ -n "${closure_dirs["$owner"]:-}" ]]; then
		gate_on
	fi
done

echo "gateway_closure_changed=false"
