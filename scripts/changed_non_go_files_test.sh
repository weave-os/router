#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

repo="$work/repo"
output="$work/output"
mkdir -p "$repo"
git -C "$repo" init --quiet
git -C "$repo" config user.name "Changed File Test"
git -C "$repo" config user.email "changed-files@example.test"

mkdir -p "$repo/frontend/src/app" "$repo/install/opencode-weave/src" "$repo/install/pi-router/src" "$repo/sidecars/hmm"
printf '#!/usr/bin/env bash\nexit 0\n' >"$repo/deleted.sh"
printf 'print("old")\n' >"$repo/old.py"
printf 'export const old = true\n' >"$repo/frontend/old.ts"
git -C "$repo" add .
git -C "$repo" commit --quiet -m base
base=$(git -C "$repo" rev-parse HEAD)

printf '#!/usr/bin/env bash\nexit 0\n' >"$repo/odd name.sh"
printf '#!/usr/bin/env bash\nexit 0\n' >"$repo/run-check"
printf 'print("new")\n' >"$repo/sidecars/hmm/new check.py"
printf 'export const next = true\n' >"$repo/frontend/src/app/next.tsx"
printf 'export const plugin = true\n' >"$repo/install/opencode-weave/src/index.ts"
printf 'export const extension = true\n' >"$repo/install/pi-router/src/index.ts"
git -C "$repo" mv old.py "renamed file.py"
git -C "$repo" rm --quiet deleted.sh
git -C "$repo" add .
git -C "$repo" commit --quiet -m head
head=$(git -C "$repo" rev-parse HEAD)

(
	cd "$repo"
	"$script_dir/changed_non_go_files.sh" "$base" "$head" "$output"
)

shell_files=()
while IFS= read -r -d '' file; do
	shell_files+=("$file")
done <"$output/shell-files.nul"
python_files=()
while IFS= read -r -d '' file; do
	python_files+=("$file")
done <"$output/python-files.nul"

[[ "${shell_files[*]}" == "odd name.sh run-check" ]]
[[ "${python_files[*]}" == "renamed file.py sidecars/hmm/new check.py" ]]
for file in "${shell_files[@]}"; do
	[[ -n "$file" ]]
done
for file in "${python_files[@]}"; do
	[[ -n "$file" ]]
done
[[ ! " ${shell_files[*]} " =~ deleted\.sh ]]
[[ -f "$output/typescript-frontend" ]]
[[ -f "$output/typescript-opencode" ]]
[[ -f "$output/typescript-pi" ]]

echo "changed non-Go file discovery tests passed"
