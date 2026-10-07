#!/usr/bin/env bash
# Classifies a base..head diff for the Test workflow's Go gates.
#
#   go_ci_relevance.sh changed <base> <head>
#       go_changed=true|false — whether the Go checks must run.
# Classification fails closed: an empty base, an unreadable commit, or an
# uncomputable package closure gates the checks ON.
set -euo pipefail

if [[ $# -ne 3 ]]; then
	echo "usage: $0 <changed> <base-commit> <head-commit>" >&2
	exit 2
fi

mode=$1
base=$2
head=$3

case "$mode" in
	changed) output_name=go_changed ;;
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

# NUL-delimited git output cannot survive command substitution, so it goes
# through a scratch file whose exit status is checked: a git failure is
# unclassifiable, not a clean "nothing to do".
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

# --no-renames keeps both sides of a move: a file leaving a Go package still
# has to gate the checks ON.
git diff --name-only -z --no-renames "${base}...${head}" -- >"$scratch/diff" || gate_on

changed_files=()
while IFS= read -r -d '' file; do
	changed_files+=("$file")
done <"$scratch/diff"

if [[ ${#changed_files[@]} -eq 0 ]]; then
	echo "$output_name=false"
	exit 0
fi

# go.mod / go.sum / go.work* change the build for every package.
is_module_file() {
	case "$1" in
		go.mod | go.sum | go.work*) return 0 ;;
	esac
	return 1
}

# Checked-in artifacts generated from Go sources and asserted by Go tests
# (cmd/genprices), yet living outside every Go package directory.
go_generated_artifacts=(
	install/cc-statusline.sh
	install/install.sh
	install/pi-router/src/pricing.generated.ts
	bench/weave_bench/prices.generated.json
)

# Directories holding at least one Go source file in either tree. A file's
# owning package is its nearest such ancestor: embedded assets live next to or
# below the package that embeds them, so this resolves //go:embed inputs
# without a toolchain. Both trees are read so that deleting the last Go source
# of a package still classifies the rest of that directory.
declare -A go_package_dirs=()
load_go_package_dirs() {
	local tree file
	for tree in "$base" "$head"; do
		git ls-tree -r -z --name-only "$tree" >"$scratch/tree" || gate_on
		while IFS= read -r -d '' file; do
			[[ "$file" == *.go && "$file" == */* ]] || continue
			go_package_dirs["${file%/*}"]=1
		done <"$scratch/tree"
	done
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
		for artifact in "${go_generated_artifacts[@]}"; do
			[[ "$file" != "$artifact" ]] || gate_on
		done

		# Anything inside a Go package directory (or below it) can be compiled
		# in: source, embedded prompts, embedded model artifacts, testdata.
		if owning_package_dir "$file" >/dev/null; then
			gate_on
		fi
	done

	echo "go_changed=false"
	exit 0
fi
