#!/usr/bin/env bash
#
# Registry consistency + cross-client installer coverage.
#
# The registry in install/directives.tsv is the single declaration of which
# router directive each client gets. These tests assert that every client's
# real install output matches it, so adding a directive to one client without
# the others fails here rather than silently shipping.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install_dir="$script_dir/.."
installer="${INSTALLER:-$install_dir/install.sh}"
uninstaller="${UNINSTALLER:-$install_dir/uninstall.sh}"
registry="$install_dir/directives.tsv"
# shellcheck disable=SC1091
. "$install_dir/registry.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fake_bin="$work/bin"
mkdir -p "$fake_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$fake_bin/curl"
chmod +x "$fake_bin/curl"
test_path="$fake_bin:$PATH"

pass=0
fail=0
ok()   { printf '  ok   %s\n' "$1"; pass=$((pass + 1)); }
no()   { printf '  FAIL %s\n         expected: %s\n         actual:   %s\n' "$1" "$2" "$3"; fail=$((fail + 1)); }
check() { # check <name> <expected> <actual>
  if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "$2" "$3"; fi
}

printf 'registry\n'

# ---------- registry shape ----------

bad_rows=""
while IFS='|' read -r canonical aliases capability claude codex opencode pi cursor adapter; do
  [ -n "$canonical" ] || continue
  case "$capability" in
    prompt|local-toggle) ;;
    *) bad_rows="$bad_rows $canonical:capability=$capability" ;;
  esac
  for value in "$claude" "$codex" "$opencode" "$pi"; do
    case "$value" in yes|no) ;; *) bad_rows="$bad_rows $canonical:client=$value" ;; esac
  done
  # Cursor has no installer-owned command surface; the registry must say so
  # rather than implying this installer can write Cursor's settings.
  [ "$cursor" = manual ] || bad_rows="$bad_rows $canonical:cursor=$cursor"
done < <(weave_registry_rows)
check "every row declares a known capability and client support" "" "$bad_rows"

# Names repeated across clients are the same directive installed for each of
# them (Codex ships most of Claude Code's set as skills). Pinning the exact
# overlap catches a genuine collision — two *different* directives claiming one
# filename — which would otherwise hide in this list.
dupes="$(weave_registry_names claude; weave_registry_names codex; weave_registry_names opencode)"
dupes="$(printf '%s\n' "$dupes" | sort | uniq -d | tr '\n' ' ' | sed 's/ $//')"
check "names shared across clients are the expected shared directives" \
  "fm force-model models rf router-feedback router-models router-off router-on router-status ufm unforce-model" "$dupes"

check "an alias resolves to its canonical directive" "force-model" "$(weave_registry_canonical_for fm)"
check "a canonical name resolves to itself" "router-feedback" "$(weave_registry_canonical_for router-feedback)"

# Pi implements /fm, /ufm and /beta in its extension rather than through
# installed files. Its registered names must still match the shared registry.
pi_registered="$(grep -hoE '"(fm|force-model|ufm|unforce-model|beta)"' \
  "$install_dir/pi-router/src/force-model.ts" "$install_dir/pi-router/src/beta.ts" \
  | tr -d '"' | sort -u | tr '\n' ' ' | sed 's/ $//')"
check "the pi extension registers exactly the registry's pi names" \
  "$(weave_registry_names pi | sort -u | tr '\n' ' ' | sed 's/ $//')" "$pi_registered"

# Every prompt directive Codex supports needs a native skill asset, and no
# skill may ship without a registry entry.
missing=""
while IFS= read -r name; do
  [ -f "$install_dir/codex-skills/$name/SKILL.md" ] || missing="$missing $name"
done < <(weave_registry_skill_names codex)
check "every Codex prompt directive has a skill asset" "" "$missing"

orphans=""
for dir in "$install_dir"/codex-skills/*/; do
  name="$(basename "$dir")"
  weave_registry_canonical_for "$name" >/dev/null || orphans="$orphans $name"
done
check "no Codex skill ships without a registry entry" "" "$orphans"

# Every command wrapper must parse as front matter + body, and every generated
# shell asset must be syntactically valid.
bad_md=""
for f in "$install_dir"/commands/*.md; do
  [ "$(head -n 1 "$f")" = "---" ] || bad_md="$bad_md $(basename "$f")"
done
check "every command wrapper opens with YAML front matter" "" "$bad_md"

bad_sh=""
for f in "$install_dir"/registry.sh "$install_dir"/install.sh "$install_dir"/uninstall.sh "$install_dir"/cc-statusline.sh; do
  bash -n "$f" 2>/dev/null || bad_sh="$bad_sh $(basename "$f")"
done
check "every shell asset parses" "" "$bad_sh"

# install.sh is served standalone (WorkWeave serves it for `curl | sh`), so it
# embeds the registry data and helpers rather than sourcing them. Embedded and
# canonical copies must stay byte-identical, or a directive added to the
# registry would never reach the standalone installer.
embedded_data="$(awk '/^WEAVE_REGISTRY_DATA=\$\(cat <<.WEAVE_REGISTRY_EOF.$/{f=1;next} /^WEAVE_REGISTRY_EOF$/{f=0} f' "$install_dir/install.sh")"
check "install.sh embeds the canonical registry data" "$(cat "$install_dir/directives.tsv")" "$embedded_data"

lib_region() {
  awk '/^# >>> weave-router registry lib >>>$/{f=1;next} /^# <<< weave-router registry lib <<<$/{f=0} f' "$1"
}
embedded_lib="$(lib_region "$install_dir/install.sh")"
check "install.sh embeds the canonical registry helpers" \
  "$(lib_region "$install_dir/registry.sh")" "$embedded_lib"

# The standalone installer must actually resolve directives with no sibling
# files present — the exact situation of a `curl | sh` install. Run the real
# script from an empty directory and assert it installs the registry's set.
standalone="$work/standalone"
mkdir -p "$standalone/bin" "$standalone/home"
cp "$install_dir/install.sh" "$standalone/install.sh"
cp "$fake_bin/curl" "$standalone/bin/curl"
HOME="$standalone/home" PATH="$standalone/bin:$PATH" WEAVE_ROUTER_KEY=rk_test_key NO_COLOR=1 \
  bash "$standalone/install.sh" --codex --scope user --quiet --base-url http://127.0.0.1:9 >"$work/standalone.log" 2>&1 || true
if grep -qi 'registry.sh: No such file\|unbound variable\|command not found' "$work/standalone.log"; then
  no "the standalone installer runs without its sibling files" "no resolution errors" \
    "$(grep -im1 'registry.sh: No such file\|unbound variable\|command not found' "$work/standalone.log")"
else
  ok "the standalone installer runs without its sibling files"
fi

# uninstall.sh is served standalone too (the npm bin runs it directly, and it
# can be piped). Sourcing a sibling registry.sh would abort it under `set -e`
# before it removed anything.
cp "$install_dir/uninstall.sh" "$standalone/uninstall.sh"
( cd "$standalone" && cat uninstall.sh | HOME="$standalone/home" bash -s -- --claude --scope user ) \
  >"$work/standalone-uninstall.log" 2>&1 || true
if grep -qi 'registry.sh: No such file\|unbound variable\|command not found' "$work/standalone-uninstall.log"; then
  no "the standalone uninstaller runs without its sibling files" "no resolution errors" \
    "$(grep -im1 'registry.sh: No such file\|unbound variable\|command not found' "$work/standalone-uninstall.log")"
else
  ok "the standalone uninstaller runs without its sibling files"
fi

check "uninstall.sh embeds the canonical registry data" "$(cat "$install_dir/directives.tsv")" \
  "$(awk '/^WEAVE_REGISTRY_DATA=\$\(cat <<.WEAVE_REGISTRY_EOF.$/{f=1;next} /^WEAVE_REGISTRY_EOF$/{f=0} f' "$install_dir/uninstall.sh")"
check "uninstall.sh embeds the canonical registry helpers" \
  "$(lib_region "$install_dir/registry.sh")" "$(lib_region "$install_dir/uninstall.sh")"

# ---------- installs ----------

printf '\ninstalls\n'

# XDG_CONFIG_HOME is forwarded when set so opencode's destination resolves the
# same way the installer resolves it; `env -u` keeps it genuinely unset
# otherwise, matching a plain developer machine.
run_install() { # run_install <home> <target-flag> [extra args...]
  local home="$1"; shift
  env ${XDG_CONFIG_HOME:+XDG_CONFIG_HOME="$XDG_CONFIG_HOME"} \
    HOME="$home" PATH="$test_path" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
    bash "$installer" "$@" --quiet --base-url http://127.0.0.1:9 >/dev/null 2>&1
}
run_uninstall() {
  local home="$1"; shift
  env ${XDG_CONFIG_HOME:+XDG_CONFIG_HOME="$XDG_CONFIG_HOME"} \
    HOME="$home" PATH="$test_path" NO_COLOR=1 \
    bash "$uninstaller" "$@" >/dev/null 2>&1
}

installed_names() { # installed_names <dir>
  local f
  for f in "$1"/*.md; do
    [ -f "$f" ] || continue
    printf '%s\n' "$(basename "$f" .md)"
  done | sort | tr '\n' ' ' | sed 's/ $//'
}

# Claude Code, user scope: the installed wrapper set is exactly the registry's.
cc_home="$work/claude-user"; mkdir -p "$cc_home"
run_install "$cc_home" --claude --scope user
check "claude user install writes exactly the registry's commands" \
  "$(weave_registry_names claude | sort | tr '\n' ' ' | sed 's/ $//')" \
  "$(installed_names "$cc_home/.claude/commands")"

# opencode, user scope: the smaller registry subset, in the XDG commands dir.
# The installer honours XDG_CONFIG_HOME, so resolve the destination the same
# way rather than assuming $HOME/.config — CI runners set it.
oc_home="$work/opencode-user"; mkdir -p "$oc_home"
oc_xdg="$work/opencode-xdg"; mkdir -p "$oc_xdg"
XDG_CONFIG_HOME="$oc_xdg" run_install "$oc_home" --opencode --scope user
oc_cmds="$oc_xdg/opencode/commands"
check "opencode user install writes exactly the registry's commands" \
  "$(weave_registry_names opencode | sort | tr '\n' ' ' | sed 's/ $//')" \
  "$(installed_names "$oc_cmds")"

# opencode must not receive the local-config toggles: it has no equivalent of
# the Claude settings.json the toggles flip.
absent=""
for name in router-off router-on router-status router-models models; do
  [ -e "$oc_cmds/$name.md" ] && absent="$absent $name"
done
check "opencode gets no Claude-only local toggle" "" "$absent"

# Codex, user scope: skills (not prompt wrappers) for every prompt directive.
cx_home="$work/codex-user"; mkdir -p "$cx_home"
run_install "$cx_home" --codex --scope user
codex_skills="$(cd "$cx_home/.codex/skills" && ls -d */ 2>/dev/null | tr -d '/' | sort | tr '\n' ' ' | sed 's/ $//')"
check "codex user install writes a skill per supported directive" \
  "disable-routing fm force-model rf router-feedback router-models router-off router-on router-status ufm unforce-model" "$codex_skills"
check "codex install writes no prompt wrappers" "" \
  "$(ls "$cx_home/.codex/prompts" 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"

# A Codex skill cannot author a user message, so each one execs emit.sh, whose
# output carries the leading-space directive the router intercepts. Codex
# reserves `/…` for its own built-ins, so a bare slash never reaches the router.
for name in force-model unforce-model router-feedback fm ufm rf; do
  emit="$cx_home/.codex/skills/$name/scripts/emit.sh"
  case "$name" in
    fm) command=force-model ;; ufm) command=unforce-model ;; rf) command=router-feedback ;; *) command="$name" ;;
  esac
  if [ -x "$emit" ] && [ "$(bash "$emit" probe-arg)" = " /$command probe-arg" ] 2>/dev/null; then
    ok "codex \$$name emits a leading-space directive"
  elif [ -x "$emit" ] && [ "$(bash "$emit")" = " /$command" ] 2>/dev/null; then
    ok "codex \$$name emits a leading-space directive"
  else
    no "codex \$$name emits a leading-space directive" " /$command" "$( [ -x "$emit" ] && bash "$emit" probe-arg 2>&1 || echo 'no executable emit.sh')"
  fi
done

# Local-config toggles reach the installer's own verbs. A skill that names the
# wrong verb (or the wrong client) silently edits the wrong install, so pin the
# command each one shells out to.
for name in router-off:off router-on:on router-status:status router-models:models; do
  skill_name="${name%%:*}"; verb="${name##*:}"
  skill="$cx_home/.codex/skills/$skill_name/SKILL.md"
  if grep -Fq "weave-router $verb --codex" "$skill"; then
    ok "codex \$$skill_name shells out to '$verb --codex'"
  else
    no "codex \$$skill_name shells out to '$verb --codex'" \
      "weave-router $verb --codex" "$(grep -m1 'weave-router' "$skill" || echo 'no weave-router call')"
  fi
done

# A skill may only advertise a $name the installer actually creates. Codex
# discovers skills by directory name, so telling the user to invoke an alias
# that was never installed advertises a command that cannot run.
bad_alias=""
for dir in "$cx_home"/.codex/skills/*/; do
  skill="$dir/SKILL.md"
  [ -f "$skill" ] || continue
  while IFS= read -r advertised; do
    [ -n "$advertised" ] || continue
    [ -d "$cx_home/.codex/skills/$advertised" ] \
      || bad_alias="$bad_alias $(basename "$dir"):\$$advertised"
  done < <(grep -oE '\$[a-z][a-z-]+' "$skill" | tr -d '$' | sort -u)
done
check "every \$name a Codex skill advertises is installed" "" "$bad_alias"

# jq is a hard requirement for the Claude/opencode/pi JSON merges, but Codex
# writes TOML and plain files — installing skills must not start needing it.
nojq_bin="$work/nojq"; mkdir -p "$nojq_bin"
cp "$fake_bin/curl" "$nojq_bin/curl"
for tool in bash cat sed awk grep mkdir rm cp mv chmod ls dirname basename date stat cmp printf tr head tail git curl mktemp rmdir wc sort uniq diff; do
  src="$(command -v "$tool" 2>/dev/null || true)"
  [ -n "$src" ] && ln -sf "$src" "$nojq_bin/$tool" 2>/dev/null || true
done
nojq_home="$work/codex-nojq"; mkdir -p "$nojq_home"
HOME="$nojq_home" PATH="$nojq_bin" WEAVE_ROUTER_KEY="rk_test_key" NO_COLOR=1 \
  bash "$installer" --codex --scope user --quiet --base-url http://127.0.0.1:9 >/dev/null 2>&1 || true
if [ -f "$nojq_home/.codex/skills/force-model/SKILL.md" ]; then
  ok "codex installs its skills without jq on PATH"
else
  no "codex installs its skills without jq on PATH" "skills installed" "missing"
fi

# Project scope renders the scope selector into the commands that shell out to
# this installer, so they flip this install rather than the user-scope one.
proj="$work/proj"; mkdir -p "$proj"; ( cd "$proj" && git init -q . )
proj_home="$work/claude-proj"; mkdir -p "$proj_home"
run_install "$proj_home" --claude --scope project --dir "$proj"
if grep -q -- "--dir $proj" "$proj/.claude/commands/router-off.md"; then
  ok "a --dir install bakes its own scope into the toggle commands"
else
  no "a --dir install bakes its own scope into the toggle commands" \
    "--dir $proj" "$(grep -m1 npx "$proj/.claude/commands/router-off.md")"
fi
check "no rendered command leaks the router key" "" \
  "$(grep -rl 'rk_test_key' "$proj/.claude/commands" 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"

# Re-installing refreshes in place rather than duplicating.
before="$(installed_names "$cc_home/.claude/commands")"
run_install "$cc_home" --claude --scope user
check "a re-install leaves the same command set" "$before" "$(installed_names "$cc_home/.claude/commands")"

# ---------- ownership and uninstall ----------

printf '\nownership\n'

# A same-named file the user owns is never overwritten, and never removed.
printf '%s\n' 'my own wrapper' >"$cc_home/.claude/commands/rf.md"
run_install "$cc_home" --claude --scope user
check "install preserves a user-owned Claude command" "my own wrapper" \
  "$(cat "$cc_home/.claude/commands/rf.md")"

# Wrappers written before ownership markers existed carry none. One whose body
# still matches what this installer writes is ours from an older version, so an
# upgrade must adopt it — otherwise it is never refreshed and never uninstalled.
legacy_home="$work/claude-legacy"; mkdir -p "$legacy_home/.claude/commands"
legacy_body="$(sed 's/{{SCOPE}}//g' "$install_dir/commands/fm.md")"
printf '%s\n' "$legacy_body" >"$legacy_home/.claude/commands/fm.md"
printf '%s\n' 'MY OWN CUSTOM WRAPPER' >"$legacy_home/.claude/commands/rf.md"
run_install "$legacy_home" --claude --scope user
if grep -Fq '<!-- weave-router managed command: fm -->' "$legacy_home/.claude/commands/fm.md"; then
  ok "an upgrade adopts an unmarked wrapper it previously wrote"
else
  no "an upgrade adopts an unmarked wrapper it previously wrote" "marker added" "still unmarked"
fi
check "an upgrade still leaves a genuinely user-authored command alone" "MY OWN CUSTOM WRAPPER" \
  "$(cat "$legacy_home/.claude/commands/rf.md")"

# Having adopted it, uninstall must now clean it up rather than strand it.
run_uninstall "$legacy_home" --claude --scope user
check "uninstall removes a wrapper adopted from a pre-marker install" "rf" \
  "$(installed_names "$legacy_home/.claude/commands")"

printf '%s\n' 'my own opencode wrapper' >"$oc_cmds/fm.md"
XDG_CONFIG_HOME="$oc_xdg" run_install "$oc_home" --opencode --scope user
check "install preserves a user-owned opencode command" "my own opencode wrapper" \
  "$(cat "$oc_cmds/fm.md")"

# A symlinked destination in a project-scope install is refused outright: the
# repo is attacker-controlled and the write would follow the link out of it.
sym="$work/sym"; mkdir -p "$sym/.claude/commands"; ( cd "$sym" && git init -q . )
ln -sf /etc/passwd "$sym/.claude/commands/fm.md"
sym_home="$work/claude-sym"; mkdir -p "$sym_home"
if run_install "$sym_home" --claude --scope project --dir "$sym"; then
  no "a symlinked command destination is refused" "install refuses" "install succeeded"
else
  ok "a symlinked command destination is refused"
fi
check "the symlink target is left untouched" "/etc/passwd" "$(readlink "$sym/.claude/commands/fm.md")"

printf '\nuninstall\n'

run_uninstall "$cc_home" --claude --scope user
check "uninstall removes every command it owns" "rf" "$(installed_names "$cc_home/.claude/commands")"
check "uninstall preserves the user-owned command's contents" "my own wrapper" \
  "$(cat "$cc_home/.claude/commands/rf.md")"

XDG_CONFIG_HOME="$oc_xdg" run_uninstall "$oc_home" --opencode --scope user
check "uninstall removes every opencode command it owns" "fm" \
  "$(installed_names "$oc_cmds")"

run_uninstall "$cx_home" --codex --scope user
check "uninstall removes every Codex skill it owns" "" \
  "$(cd "$cx_home/.codex" 2>/dev/null && ls -d skills/*/ 2>/dev/null | tr -d '/' | sed 's|skills||' | tr '\n' ' ' | sed 's/ $//')"

if [ -d "$cx_home/.codex/skills" ]; then
  no "uninstall drops the skills dir once it is empty" "removed" "left behind"
else
  ok "uninstall drops the skills dir once it is empty"
fi

# An unrelated skill must keep the directory alive.
keep_home="$work/codex-keep"; mkdir -p "$keep_home"
run_install "$keep_home" --codex --scope user
mkdir -p "$keep_home/.codex/skills/my-own-skill"
printf '%s\n' 'user skill' >"$keep_home/.codex/skills/my-own-skill/SKILL.md"
run_uninstall "$keep_home" --codex --scope user
check "uninstall keeps a skills dir that still holds a user skill" "user skill" \
  "$(cat "$keep_home/.codex/skills/my-own-skill/SKILL.md" 2>/dev/null)"

# A symlinked skills/ or per-skill dir must not be followed: the marker check
# and rm would otherwise reach a file outside the Codex tree entirely.
sym_skills="$work/sym-skills"
mkdir -p "$sym_skills/home/.codex" "$sym_skills/external/force-model"
printf '%s\n' '<!-- weave-router managed force-model skill -->' \
  >"$sym_skills/external/force-model/SKILL.md"
ln -s "$sym_skills/external" "$sym_skills/home/.codex/skills"
run_uninstall "$sym_skills/home" --codex --scope user
if [ -f "$sym_skills/external/force-model/SKILL.md" ]; then
  ok "uninstall does not delete through a symlinked Codex skills dir"
else
  no "uninstall does not delete through a symlinked Codex skills dir" \
    "external file preserved" "deleted through the symlink"
fi

sym_one="$work/sym-one"
mkdir -p "$sym_one/home/.codex/skills" "$sym_one/external/force-model"
printf '%s\n' '<!-- weave-router managed force-model skill -->' \
  >"$sym_one/external/force-model/SKILL.md"
ln -s "$sym_one/external/force-model" "$sym_one/home/.codex/skills/force-model"
run_uninstall "$sym_one/home" --codex --scope user
if [ -f "$sym_one/external/force-model/SKILL.md" ]; then
  ok "uninstall does not delete through a symlinked per-skill dir"
else
  no "uninstall does not delete through a symlinked per-skill dir" \
    "external file preserved" "deleted through the symlink"
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
