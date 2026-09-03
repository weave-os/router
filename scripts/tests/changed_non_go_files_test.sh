#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
selector="$repo_root/scripts/changed_non_go_files.sh"
fixture_repo="$(mktemp -d)"
trap 'rm -rf "$fixture_repo"' EXIT

git -C "$fixture_repo" init --quiet
git -C "$fixture_repo" config user.email "checks@example.com"
git -C "$fixture_repo" config user.name "Changed-file checks"

mkdir -p "$fixture_repo/frontend/src" "$fixture_repo/install" "$fixture_repo/scripts"
printf '{"private":true}\n' > "$fixture_repo/frontend/package.json"
printf 'export const oldValue = 1;\n' > "$fixture_repo/frontend/src/old.ts"
printf '#!/usr/bin/env python3\nprint("deleted")\n' > "$fixture_repo/scripts/deleted file.py"
printf 'documentation\n' > "$fixture_repo/README.md"
git -C "$fixture_repo" add frontend install scripts README.md
git -C "$fixture_repo" commit --quiet -m baseline
base_ref="$(git -C "$fixture_repo" rev-parse HEAD)"

printf '{"private":true,"scripts":{"typecheck":"tsc --noEmit"}}\n' > "$fixture_repo/frontend/package.json"
printf 'export const currentValue = 2;\n' > "$fixture_repo/frontend/src/new component.tsx"
printf '#!/usr/bin/env bash\necho extensionless\n' > "$fixture_repo/install/spin"
printf '#!/usr/bin/env bash\necho spaced\n' > "$fixture_repo/install/space script.sh"
printf 'print("lint me")\n' > "$fixture_repo/scripts/check me.py"
rm "$fixture_repo/scripts/deleted file.py"
printf 'updated documentation\n' > "$fixture_repo/README.md"
git -C "$fixture_repo" add frontend install scripts README.md
git -C "$fixture_repo" commit --quiet -m head
head_ref="$(git -C "$fixture_repo" rev-parse HEAD)"

assert_files() {
  category="$1"
  shift
  expected=("$@")
  actual=()
  while IFS= read -r -d '' path; do
    actual+=("$path")
  done < <(cd "$fixture_repo" && "$selector" "$base_ref" "$head_ref" "$category")

  if [ "${#actual[@]}" -ne "${#expected[@]}" ]; then
    printf 'expected %s %s files, got %s: %s\n' "$category" "${#expected[@]}" "${#actual[@]}" "${actual[*]}" >&2
    exit 1
  fi

  for index in "${!expected[@]}"; do
    if [ "${actual[$index]}" != "${expected[$index]}" ]; then
      printf 'expected %s file %s to be %q, got %q\n' "$category" "$index" "${expected[$index]}" "${actual[$index]}" >&2
      exit 1
    fi
  done
}

assert_files typescript \
  "frontend/package.json" \
  "frontend/src/new component.tsx"
assert_files shell \
  "install/space script.sh" \
  "install/spin"
assert_files python \
  "scripts/check me.py"

echo "changed non-Go file selection passed"
