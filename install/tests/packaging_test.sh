#!/usr/bin/env bash
#
# Packaging coverage: the published tarball must be self-contained.
#
# `npm pack` runs the prepack copy step, so these tests assert against the real
# tarball and drive the actual npx entrypoint rather than grepping the scripts.
# A registry entry whose adapter never gets packaged would install fine from a
# git checkout and fail only for users installing from npm.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install_dir="$script_dir/.."
npm_dir="$install_dir/npm"
# shellcheck disable=SC1091
. "$install_dir/registry.sh"

command -v npm >/dev/null 2>&1 || { echo "npm is required for the packaging tests" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok() { printf '  ok   %s\n' "$1"; pass=$((pass + 1)); }
no() { printf '  FAIL %s\n         expected: %s\n         actual:   %s\n' "$1" "$2" "$3"; fail=$((fail + 1)); }
check() { if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$2" "$3"; fi; }

printf 'packaging\n'

tarball="$(cd "$npm_dir" && npm pack --pack-destination "$work" 2>/dev/null | tail -1)"
[ -f "$work/$tarball" ] || { echo "npm pack produced no tarball" >&2; exit 1; }
pkg="$work/extracted"
mkdir -p "$pkg"
tar -xzf "$work/$tarball" -C "$pkg"
root="$pkg/package"

# The registry itself must ship: install.sh sources it at runtime, so a tarball
# without it is an installer that cannot resolve a single directive.
for asset in registry.sh directives.tsv install.sh uninstall.sh cc-statusline.sh codex-status.sh bin.js; do
  if [ -f "$root/$asset" ]; then ok "the tarball ships $asset"; else no "the tarball ships $asset" "present" "missing"; fi
done

# Every registry-declared adapter is packaged, and nothing is packaged that the
# registry does not declare. This is the check that keeps the two in lockstep.
check "the canonical package name is published" "@weave-os/router" \
  "$(node -p 'require(process.argv[1]).name' "$root/package.json")"

missing=""
while IFS= read -r name; do
  [ -f "$root/commands/$name.md" ] || missing="$missing $name"
done < <(weave_registry_names claude)
check "every Claude directive's command file is packaged" "" "$missing"

missing=""
while IFS= read -r name; do
  [ -f "$root/commands/$name.md" ] || missing="$missing $name"
done < <(weave_registry_names opencode)
check "every opencode directive's command file is packaged" "" "$missing"

missing=""
while IFS= read -r name; do
  [ -f "$root/codex-skills/$name/SKILL.md" ] || missing="$missing $name"
done < <(weave_registry_skill_names codex)
check "every Codex prompt directive's skill is packaged" "" "$missing"

orphans=""
for f in "$root"/commands/*.md; do
  name="$(basename "$f" .md)"
  weave_registry_canonical_for "$name" >/dev/null || orphans="$orphans $name"
done
check "no packaged command lacks a registry entry" "" "$orphans"

orphans=""
for d in "$root"/codex-skills/*/; do
  name="$(basename "$d")"
  weave_registry_canonical_for "$name" >/dev/null || orphans="$orphans $name"
done
check "no packaged Codex skill lacks a registry entry" "" "$orphans"

# Pi adapters travel through the existing pi.skills bundle path, not the
# installer, so assert the manifest still points at them.
check "the package still declares its pi extension" "./pi-router/src/index.ts" \
  "$(node -e 'process.stdout.write(require(process.argv[1]).pi.extensions[0])' "$root/package.json")"
check "the package still declares its pi skills bundle" "./pi-router/skills" \
  "$(node -e 'process.stdout.write(require(process.argv[1]).pi.skills[0])' "$root/package.json")"

# ---------- the real entrypoint ----------

printf '\nentrypoint\n'

home="$work/home"; mkdir -p "$home/bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$home/bin/curl"
chmod +x "$home/bin/curl"

# Drive bin.js exactly as `npx @workweave/router` does. A tarball missing any
# runtime asset fails here even though every string assertion above passed.
HOME="$home" PATH="$home/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
  node "$root/bin.js" --codex --scope user --quiet --base-url http://127.0.0.1:9 >/dev/null 2>&1 || true

installed="$(cd "$home/.codex/skills" 2>/dev/null && ls -d */ 2>/dev/null | tr -d '/' | sort | tr '\n' ' ' | sed 's/ $//')"
check "the packed entrypoint installs every Codex skill" \
  "disable-routing fm force-model rf router-feedback router-models router-off router-on router-status ufm unforce-model" "$installed"

HOME="$home" PATH="$home/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
  node "$root/bin.js" --claude --scope user --quiet --base-url http://127.0.0.1:9 >/dev/null 2>&1 || true
installed="$(cd "$home/.claude/commands" 2>/dev/null && ls *.md 2>/dev/null | sed 's/\.md$//' | sort | tr '\n' ' ' | sed 's/ $//')"
check "the packed entrypoint installs every Claude command" \
  "$(weave_registry_names claude | sort | tr '\n' ' ' | sed 's/ $//')" "$installed"

HOME="$home" PATH="$home/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
  node "$root/bin.js" --pi --scope user --quiet --base-url http://127.0.0.1:9 >/dev/null 2>&1 || true
check "the packed entrypoint registers its own pi package" \
  "npm:@weave-os/router" \
  "$(jq -r '.packages[]? // empty' "$home/.pi/agent/settings.json" 2>/dev/null | grep -F 'npm:@weave-os/router' || true)"

# Re-install after a prior @workweave/router install must not leave both
# extensions in packages — pi would load them twice.
mkdir -p "$home/.pi/agent"
printf '%s\n' '{"defaultProvider":"weave","packages":["npm:@workweave/router","npm:@workweave/pi-router"]}' >"$home/.pi/agent/settings.json"
HOME="$home" PATH="$home/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
  node "$root/bin.js" --pi --scope user --quiet --base-url http://127.0.0.1:9 >/dev/null 2>&1 || true
check "a weave-os re-install drops leftover workweave pi packages" \
  "npm:@weave-os/router" \
  "$(jq -r '.packages[]? // empty' "$home/.pi/agent/settings.json" 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"

# The uninstall path is bundled too; a tarball that can install but not
# uninstall strands the user.
HOME="$home" PATH="$home/bin:$PATH" NO_COLOR=1 \
  node "$root/bin.js" --uninstall --claude --scope user >/dev/null 2>&1 || true
check "the packed entrypoint uninstalls what it installed" "" \
  "$(cd "$home/.claude/commands" 2>/dev/null && ls *.md 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"

HOME="$home" PATH="$home/bin:$PATH" NO_COLOR=1 \
  node "$root/bin.js" --uninstall --pi --scope user >/dev/null 2>&1 || true
check "the packed entrypoint removes its pi package" "" \
  "$(jq -r '.packages[]? // empty' "$home/.pi/agent/settings.json" 2>/dev/null | grep -E 'npm:(@weave-os/router|@workweave/router)' || true)"

# The legacy package is published from the same tarball with only its package
# name changed. Its entrypoint must identify that name and explain the
# migration while retaining the exact installer behavior.
legacy="$work/legacy"
cp -R "$root" "$legacy"
node -e '
  const fs = require("node:fs");
  const file = process.argv[1];
  const pkg = JSON.parse(fs.readFileSync(file, "utf8"));
  pkg.name = "@workweave/router";
  fs.writeFileSync(file, JSON.stringify(pkg, null, 2) + "\n");
' "$legacy/package.json"
legacy_err="$work/legacy.err"
HOME="$home" PATH="$home/bin:$PATH" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
  node "$legacy/bin.js" --codex --scope user --quiet --base-url http://127.0.0.1:9 \
  >/dev/null 2>"$legacy_err" || true
check "the legacy entrypoint points to the renamed package" \
  "yes" "$(grep -Fq 'use @weave-os/router' "$legacy_err" && echo yes || echo no)"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
