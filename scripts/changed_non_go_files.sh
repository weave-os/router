#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
	echo "usage: $0 <base-commit> <head-commit> <output-directory>" >&2
	exit 2
fi

base=$1
head=$2
output_dir=$3

git cat-file -e "${base}^{commit}"
git cat-file -e "${head}^{commit}"
mkdir -p "$output_dir"

shell_files="$output_dir/shell-files.nul"
python_files="$output_dir/python-files.nul"
: >"$shell_files"
: >"$python_files"
for marker in typescript-frontend typescript-opencode typescript-pi; do
	[[ ! -e "$output_dir/$marker" ]] || rm "$output_dir/$marker"
done
ruff_config_changed=false

while IFS= read -r -d '' file; do
	# Diff type changes can leave the path as a directory. Deleted paths are
	# excluded by the diff filter, and this guard keeps the consumer lists safe.
	[[ -f "$file" ]] || continue

	if [[ "$file" =~ ^(frontend|assets/ui/types)/.+\.tsx?$ ]] ||
		[[ "$file" == frontend/package.json || "$file" == frontend/package-lock.json || "$file" == frontend/tsconfig*.json ]]; then
		touch "$output_dir/typescript-frontend"
	elif [[ "$file" =~ ^install/opencode-weave/.+\.tsx?$ ]] ||
		[[ "$file" == install/opencode-weave/package.json || "$file" == install/opencode-weave/package-lock.json || "$file" == install/opencode-weave/tsconfig*.json ]]; then
		touch "$output_dir/typescript-opencode"
	elif [[ "$file" =~ ^install/pi-router/.+\.tsx?$ ]] ||
		[[ "$file" == install/pi-router/package.json || "$file" == install/pi-router/package-lock.json || "$file" == install/pi-router/tsconfig*.json ]]; then
		touch "$output_dir/typescript-pi"
	fi

	case "$file" in
		*.py | *.pyi)
			printf '%s\0' "$file" >>"$python_files"
			;;
		ruff.toml)
			ruff_config_changed=true
			;;
	esac

	case "$file" in
		*.sh | *.bash)
			printf '%s\0' "$file" >>"$shell_files"
			continue
			;;
	esac

	if IFS= read -r first_line <"$file" && [[ "$first_line" =~ ^\#\!.*(ba|z|da|k)?sh([[:space:]]|$) ]]; then
		printf '%s\0' "$file" >>"$shell_files"
	fi
done < <(git diff --name-only --diff-filter=ACMRT -z "${base}...${head}" --)

if [[ "$ruff_config_changed" == true ]]; then
	git ls-files -z -- '*.py' '*.pyi' >"$python_files"
fi
