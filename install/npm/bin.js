#!/usr/bin/env node
// Thin wrapper that runs install.sh with the user's arguments. The canonical
// no-argument command opens the hosted setup page instead.

const { spawnSync } = require("node:child_process");
const { existsSync, readFileSync } = require("node:fs");
const path = require("node:path");

const args = process.argv.slice(2);
const packageName = JSON.parse(
  readFileSync(path.join(__dirname, "package.json"), "utf8"),
).name;

const defaultEntrypointURL = "https://router.workweave.ai/start";

if (packageName === "@weave-os/router" && args.length === 0) {
  openDefaultEntrypoint(defaultEntrypointURL);
  process.exit(0);
}

if (packageName === "@workweave/router") {
  console.error(
    "warning: @workweave/router is deprecated; use @weave-os/router instead (npx @weave-os/router).",
  );
}

const uninstallIdx = args.indexOf("--uninstall");
const isUninstall = uninstallIdx !== -1;
if (isUninstall) args.splice(uninstallIdx, 1);

const scriptName = isUninstall ? "uninstall.sh" : "install.sh";
const script = path.join(__dirname, scriptName);

if (!existsSync(script)) {
  console.error(
    `Weave Router: ${scriptName} missing from package — report it at https://github.com/weave-os/router/issues`,
  );
  process.exit(1);
}

const bash = pickBash();
if (!bash) {
  console.error(
    "Weave Router: bash is required. On Windows install Git Bash or run inside WSL.",
  );
  process.exit(1);
}

const result = spawnSync(bash, [script, ...args], {
  stdio: "inherit",
  env: {
    ...process.env,
    WEAVE_ROUTER_NPM_PACKAGE: packageName,
  },
});

if (result.error) {
  console.error("Weave Router:", result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);

function pickBash() {
  if (process.platform !== "win32") return "bash";
  const candidates = [
    process.env.SHELL,
    "C:\\Program Files\\Git\\bin\\bash.exe",
    "C:\\Program Files (x86)\\Git\\bin\\bash.exe",
  ].filter(Boolean);
  for (const c of candidates) {
    if (existsSync(c)) return c;
  }
  return null;
}

function openDefaultEntrypoint(url) {
  const opener = process.platform === "darwin"
    ? ["open", [url]]
    : process.platform === "win32"
      ? ["cmd.exe", ["/c", "start", "", url]]
      : ["xdg-open", [url]];
  const result = spawnSync(opener[0], opener[1], { stdio: "ignore" });
  if (result.error || result.status !== 0) {
    console.log(`Open ${url} in your browser to get started.`);
  }
}
