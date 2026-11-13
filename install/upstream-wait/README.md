# Upstream provider waiting indicator

After configuring the Router with the normal installer, start an interactive session with:

```sh
npx @weave-os/router codex
npx @weave-os/router claude
```

Pass client arguments after the client name, for example `npx @weave-os/router codex resume --last`.
These launch the installed clients with a process-local bridge. They do not change saved
credentials, provider selection, or config files. Codex profiles and config overrides also
apply to its local app-server.

The native animated status displays only `Waiting on upstream provider..` while an upstream
HTTP request is waiting for output. It clears on first useful output, attempt failure,
cancellation, or completion. No status text is added to model input, replies, transcript,
or saved conversation history. Retries can show another wait.

The Router emits dispatch and first-output signals over an opt-in framed HTTP transport
(`X-Weave-Provider-Wait: 1`). Its frames carry wait state, the original HTTP status and
headers, base64-encoded original body bytes, and completion. Keeping the original response
inside the frames lets the Router announce a dispatch without committing the provider's
response or changing its retry/error semantics. The bridge restores that response before
it reaches the client. Ordinary requests use their existing wire protocol. With an older
Router, the launcher forwards responses normally and shows no provider-specific indicator.

Codex uses its stock terminal UI over a loopback app-server connection. The bridge sends
`hook/started` and `hook/completed` notifications only to that UI, with empty entries. This
reuses Codex's experimental hook status protocol, verified with Codex CLI 0.163.0; it is not
a stable upstream provider-status extension. See the [app-server documentation](https://developers.openai.com/codex/app-server/).

Claude Code uses a mod that changes the native `Spinner` message and retains its animation.
The mod redirects that process's configured `ANTHROPIC_BASE_URL` through the bridge, polls
its actual provider state during main-loop model steps, and resets at turn/session end.
It adds no tools or messages. It uses Claude Code's mods API, with mods enabled. The
native-client check was run on Claude Code 2.1.292. See
[the Spinner mod API](https://code.claude.com/docs/en/plugins/mods/interface#change-what-claude-code-already-draws).

Each launcher owns its bridge and stops it when the client exits. The listener binds only
to loopback, uses a random per-launch URL capability, rejects browser origins, and stores
no request bodies, credentials, or conversations.

Offline validation:

```sh
npm install --prefix install/npm --ignore-scripts --package-lock=false
node install/npm/scripts/copy-installer.js
NODE_PATH="$PWD/install/npm/node_modules" node --test install/upstream-wait/test/*.test.cjs
node --test install/upstream-wait/test/*.test.mjs
uv run install/upstream-wait/test/verify_tui.py --client codex
uv run install/upstream-wait/test/verify_tui.py --client claude
```

Add `--interrupt` to either UI check to verify cancellation. These checks use disposable
client homes and authored localhost responses; they make no inference-provider calls.
