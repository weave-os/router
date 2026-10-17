#!/usr/bin/env bash

set -euo pipefail

if ! command -v node >/dev/null 2>&1 || ! node -e 'if (Number(process.versions.node.split(".")[0]) < 18 || typeof fetch !== "function" || typeof AbortSignal.timeout !== "function") process.exit(1)' >/dev/null 2>&1; then
  echo "organization policy hook tests require Node.js 18 or newer" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
installer="${INSTALLER:-$script_dir/../install.sh}"
uninstaller="${UNINSTALLER:-$script_dir/../uninstall.sh}"
work="$(mktemp -d)"
server_pid=""
cleanup() {
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

fake_bin="$work/bin"
home="$work/home with spaces"
real_mv="$(command -v mv)"
export TEST_REAL_MV="$real_mv"
mkdir -p "$fake_bin" "$home/.claude"
printf '%s\n' '#!/usr/bin/env bash' 'exit 22' >"$fake_bin/curl"
cat >"$fake_bin/mv" <<'MV'
#!/usr/bin/env bash
destination="${!#}"
if [ "$destination" = "${TEST_POLICY_HOOK_PATH:-}" ]; then
  printf '%s\n' hook >>"$TEST_POLICY_MOVE_LOG"
elif [ "$destination" = "${TEST_POLICY_SETTINGS_FILE:-}" ]; then
  if [ ! -f "$TEST_POLICY_HOOK_PATH" ] || ! grep -Fq 'weave-router managed organization policy hook' "$TEST_POLICY_HOOK_PATH"; then
    echo "settings registered the policy hook before its helper was installed" >&2
    exit 1
  fi
  printf '%s\n' settings >>"$TEST_POLICY_MOVE_LOG"
fi
exec "$TEST_REAL_MV" "$@"
MV
chmod +x "$fake_bin/curl" "$fake_bin/mv"
node_bin="$(command -v node)"
printf -v legacy_hook_command '%q %q' "$node_bin" "$home/.claude/weave-router-policy.js"
printf -v old_node_hook_command '%q %q' /old/node "$home/.claude/weave-router-policy.cjs"
jq -n --arg legacy_hook_command "$legacy_hook_command" --arg old_node_hook_command "$old_node_hook_command" '{
  env: {WEAVE_POLICY_API_URL: "https://attacker.invalid/collect"},
  hooks: {
    SessionStart: [{matcher: "startup", hooks: [
      {type: "command", command: "user-hook"},
      {type: "command", command: $legacy_hook_command}
    ]}],
    SubagentStart: [{hooks: [{type: "command", command: $old_node_hook_command}]}]
  }
}' >"$home/.claude/settings.json"

HOME="$home" XDG_CACHE_HOME="$work/home/.cache" PATH="$fake_bin:$PATH" \
  TEST_REAL_MV="$real_mv" TEST_POLICY_HOOK_PATH="$home/.claude/weave-router-policy.cjs" \
  TEST_POLICY_SETTINGS_FILE="$home/.claude/settings.json" TEST_POLICY_MOVE_LOG="$work/move-order" \
  NO_COLOR=1 WEAVE_ROUTER_KEY="rk_policy_test" \
  bash "$installer" --claude --quiet --non-interactive --scope user \
    --base-url https://router.workweave.ai </dev/null >/dev/null 2>&1

settings="$home/.claude/settings.json"
hook="$home/.claude/weave-router-policy.cjs"
printf -v hook_command '%q %q' "$node_bin" "$hook"
test -f "$hook"
test "$(sed -n '1p' "$work/move-order")" = hook
test "$(sed -n '2p' "$work/move-order")" = settings
jq -e --arg hook_command "$hook_command" '
  .env.WEAVE_POLICY_API_URL == "https://attacker.invalid/collect" and
  any(.hooks.SessionStart[]?.hooks[]?; .command == $hook_command) and
  any(.hooks.SubagentStart[]?.hooks[]?; .command == $hook_command) and
  any(.hooks.SessionStart[]?.hooks[]?; .command == "user-hook") and
  ([.hooks.SessionStart[]?.hooks[]? | select(.command | contains("weave-router-policy"))] | length == 1) and
  ([.hooks.SubagentStart[]?.hooks[]? | select(.command | contains("weave-router-policy"))] | length == 1) and
  ([.hooks.UserPromptSubmit[]?.hooks[]?] | length == 0)
' "$settings" >/dev/null

cat >"$work/server.js" <<'NODE'
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const content = "Use the organization's documented workflow.";
const validKeys = new Set(["rk_policy_test", "rk_project_test"]);
const audienceFile = process.argv[3];
const requestFile = process.argv[4];
const modeFile = process.argv[5];
const leakFile = process.argv[6];
const server = http.createServer((request, response) => {
  if (request.url === "/leak") {
    fs.appendFileSync(leakFile, "leak\n");
    response.writeHead(200).end();
    return;
  }
  fs.appendFileSync(requestFile, "request\n");
  if (!validKeys.has(request.headers["x-weave-router-key"])) {
    response.writeHead(401).end();
    return;
  }
  const mode = fs.readFileSync(modeFile, "utf8");
  if (mode === "no-policy") {
    response.writeHead(204).end();
    return;
  }
  if (mode === "redirect") {
    response.writeHead(302, {location: "/leak"}).end();
    return;
  }
  const hash = crypto.createHash("sha256").update(content).digest("hex");
  const policy = {
    hash: mode === "invalid-hash" ? "0".repeat(64) : hash,
    revision: 7,
    audience: fs.readFileSync(audienceFile, "utf8"),
    content,
  };
  response.writeHead(200, {"content-type": "application/json"}).end(JSON.stringify(policy));
});
server.listen(0, "127.0.0.1", () => fs.writeFileSync(process.argv[2], String(server.address().port)));
NODE
cat >"$work/fetch-stub.cjs" <<'NODE'
const realFetch = globalThis.fetch.bind(globalThis);
const expected = "https://app.workweave.ai/api/weave_router/organization-policy";
globalThis.fetch = (url, options) => {
  if (url !== expected || options.redirect !== "error") {
    throw new Error("policy hook did not use the trusted no-redirect request");
  }
  return realFetch(process.env.WEAVE_POLICY_TEST_ENDPOINT, options);
};
NODE
port_file="$work/port"
printf '%s' main_and_subagents >"$work/audience"
printf '%s' valid >"$work/mode"
: >"$work/requests"
: >"$work/leaks"
node "$work/server.js" "$port_file" "$work/audience" "$work/requests" "$work/mode" "$work/leaks" &
server_pid=$!
for _ in $(seq 1 100); do
  [ -s "$port_file" ] && break
  sleep 0.02
done
test -s "$port_file"

policy_endpoint="http://127.0.0.1:$(cat "$port_file")"
headers="$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$settings")"
request_count() { wc -l <"$work/requests" | tr -d ' '; }
run_hook() {
  local event="$1" source="${2:-}" custom_headers="${3:-$headers}"
  printf '{"hook_event_name":"%s","source":"%s"}\n' "$event" "$source" |
    NODE_OPTIONS="--require=$work/fetch-stub.cjs" \
      ANTHROPIC_CUSTOM_HEADERS="$custom_headers" WEAVE_POLICY_TEST_ENDPOINT="$policy_endpoint" \
      bash -c "$hook_command"
}

startup="$(run_hook SessionStart startup)"
[[ "$startup" == *'"hookEventName":"SessionStart"'* && "$startup" == *"documented workflow."* ]]
test "$(request_count)" = 1
resume="$(run_hook SessionStart resume)"
test -z "$resume"
test "$(request_count)" = 1
clear="$(run_hook SessionStart clear)"
[[ "$clear" == *"documented workflow."* ]]
compact="$(run_hook SessionStart compact)"
[[ "$compact" == *"documented workflow."* ]]
child="$(run_hook SubagentStart)"
[[ "$child" == *'"hookEventName":"SubagentStart"'* && "$child" == *"documented workflow."* ]]
printf '%s' main_thread >"$work/audience"
test -z "$(run_hook SubagentStart)"
printf '%s' main_and_subagents >"$work/audience"
printf '%s' no-policy >"$work/mode"
test -z "$(run_hook SessionStart startup)"
printf '%s' invalid-hash >"$work/mode"
test -z "$(run_hook SessionStart startup)"
printf '%s' redirect >"$work/mode"
test -z "$(run_hook SessionStart startup)"
test ! -s "$work/leaks"
test -z "$(run_hook SessionStart startup "X-Weave-Router-Key: invalid")"

printf '%s' valid >"$work/mode"
project="$work/project with spaces"
mkdir -p "$project"
printf '%s\n' '{"type":"module"}' >"$project/package.json"
git -C "$project" init -q
(
  cd "$project"
  HOME="$work/project-home" XDG_CACHE_HOME="$work/project-home/.cache" \
    PATH="$fake_bin:$PATH" NO_COLOR=1 WEAVE_ROUTER_KEY="rk_project_test" \
    bash "$installer" --claude --quiet --non-interactive --scope project \
      --base-url https://router.workweave.ai </dev/null >/dev/null 2>&1
)
project_settings="$project/.claude/settings.json"
project_hook="$project/.claude/weave-router-policy.cjs"
test -f "$project_hook"
jq -e '.hooks.SessionStart[0].hooks[0].command == "node \"${CLAUDE_PROJECT_DIR}/.claude/weave-router-policy.cjs\""' "$project_settings" >/dev/null
project_local_settings="$project/.claude/settings.local.json"
project_headers="$(jq -r '.env.ANTHROPIC_CUSTOM_HEADERS' "$project_local_settings")"
project_command="$(jq -r '.hooks.SessionStart[0].hooks[0].command' "$project_settings")"
project_output="$(printf '{"hook_event_name":"SessionStart","source":"startup"}\n' |
  NODE_OPTIONS="--require=$work/fetch-stub.cjs" CLAUDE_PROJECT_DIR="$project" \
    ANTHROPIC_CUSTOM_HEADERS="$project_headers" WEAVE_POLICY_TEST_ENDPOINT="$policy_endpoint" \
    bash -c "$project_command")"
[[ "$project_output" == *"documented workflow."* ]]

no_node_path="$work/no-node-home"
mkdir -p "$no_node_path"
unavailable_node_bin="$work/unavailable-node-bin"
mkdir -p "$unavailable_node_bin"
printf '%s\n' '#!/usr/bin/env bash' 'exit 127' >"$unavailable_node_bin/node"
chmod +x "$unavailable_node_bin/node"
HOME="$no_node_path" XDG_CACHE_HOME="$no_node_path/.cache" \
  PATH="$unavailable_node_bin:$fake_bin:$PATH" NO_COLOR=1 WEAVE_ROUTER_KEY="rk_no_node" \
  bash "$installer" --claude --quiet --non-interactive --scope user \
    --base-url https://router.workweave.ai </dev/null >/dev/null 2>&1
test ! -e "$no_node_path/.claude/weave-router-policy.cjs"
jq -e '(.hooks == null) or (.hooks | length == 0)' "$no_node_path/.claude/settings.json" >/dev/null

custom_home="$work/custom-home"
mkdir -p "$custom_home/.claude"
custom_hook="$custom_home/.claude/weave-router-policy.cjs"
printf -v custom_hook_command '%q %q' "$node_bin" "$custom_hook"
printf '%s\n' '/* weave-router managed organization policy hook */' >"$custom_hook"
jq -n --arg hook "$custom_hook_command" '{hooks:{SessionStart:[{hooks:[{type:"command",command:$hook}]}],SubagentStart:[{hooks:[{type:"command",command:$hook}]}]}}' >"$custom_home/.claude/settings.json"
HOME="$custom_home" XDG_CACHE_HOME="$custom_home/.cache" PATH="$fake_bin:$PATH" \
  NO_COLOR=1 WEAVE_ROUTER_KEY="rk_custom_test" \
  bash "$installer" --claude --quiet --non-interactive --scope user \
    --base-url http://127.0.0.1:9 </dev/null >/dev/null 2>&1
test ! -e "$custom_hook"
jq -e '(.hooks == null) or (.hooks | length == 0)' "$custom_home/.claude/settings.json" >/dev/null

python3 -c 'import os,sys; os.unlink(sys.argv[1])' "$hook"
HOME="$home" XDG_CACHE_HOME="$work/home/.cache" \
  bash "$uninstaller" --claude --scope user </dev/null >/dev/null 2>&1
jq -e '
  (.env.WEAVE_POLICY_API_URL == "https://attacker.invalid/collect") and
  ([.hooks.SessionStart[]?.hooks[]? | select(.command == "user-hook")] | length) == 1 and
  (.hooks.SubagentStart == null)
' "$settings" >/dev/null

HOME="$work/project-home" XDG_CACHE_HOME="$work/project-home/.cache" \
  bash "$uninstaller" --claude --scope project --dir "$project" </dev/null >/dev/null 2>&1
jq -e '(.hooks == null) or (.hooks | length == 0)' "$project_settings" >/dev/null

echo "Organization policy hook lifecycle and security tests passed"
