#!/usr/bin/env node
// Native Windows entrypoint. Uses only Node built-ins so PowerShell users do
// not need Git Bash, jq, or a package manager beyond the npx they already ran.

const fs = require("node:fs");
const path = require("node:path");
const readline = require("node:readline/promises");
const { stdin, stdout } = require("node:process");

const defaultBaseUrl = process.env.WEAVE_ROUTER_URL || "https://router.workweave.ai";
const packageName = process.env.WEAVE_ROUTER_NPM_PACKAGE || "@weave-os/router";
const beginMarker = "# >>> weave-router managed (do not edit between markers) >>>";
const endMarker = "# <<< weave-router managed <<<";

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    console.log("Usage: npx @weave-os/router [--claude|--codex|--opencode|--pi] [--scope user|project] [--dir PATH] [--base-url URL] [--uninstall]");
    console.log("On Windows, install and uninstall run natively in Node.js; no Git Bash or jq is needed.");
    return;
  }
  if (options.mode !== "install" && options.mode !== "uninstall") {
    throw new Error("This Windows release supports install and --uninstall. Other router commands are not yet available natively.");
  }
  if (options.mode === "install" && (options.returnUrl || options.contextWindow || options.lsp || options.rotateKey)) {
    throw new Error("--return-url, --context-window, --lsp, and --rotate-key are not yet available in the native Windows installer.");
  }
  if (options.scope === "project" && !options.directory) {
    options.directory = findGitRoot(process.cwd());
    if (!options.directory) throw new Error("--scope project must run inside a git repo, or pass --dir <path>.");
  }

  const root = path.resolve(options.directory || osHome());
  const target = options.target || (options.nonInteractive ? "claude" : await selectTarget());
  const paths = configPaths(root, target, options.scope, options.directory);
  for (const filePath of Object.values(paths)) refuseSymlink(filePath);

  if (options.mode === "uninstall") {
    uninstall(paths, target);
    console.log(`Weave Router removed from ${target} settings.`);
    return;
  }

  const baseUrl = (options.baseUrl || defaultBaseUrl).replace(/\/+$/, "");
  let key = process.env.WEAVE_ROUTER_KEY || readInstalledKey(paths, target);
  if (!key && options.nonInteractive) throw new Error("--non-interactive requires WEAVE_ROUTER_KEY.");
  if (!key) {
    console.log(`Get your Weave Router API key at ${baseUrl}`);
    key = await promptForKey();
  }
  if (!key) throw new Error("No key provided.");

  const valid = await fetch(`${baseUrl}/validate`, {
    headers: { "X-Weave-Router-Key": key },
    signal: AbortSignal.timeout(5000),
  }).then((response) => response.ok).catch(() => false);
  if (!valid) console.warn("Warning: could not validate the key. Check the router URL and key if requests fail.");

  const identity = options.email || process.env.WEAVE_USER_EMAIL || "";
  if (target === "claude") installClaude(paths.config, baseUrl, key, identity);
  else if (target === "codex") installCodex(paths.config, baseUrl, key, identity);
  else if (target === "opencode") installOpenCode(paths.config, baseUrl, key, identity);
  else installPi(paths.config, paths.settings, baseUrl, key, identity);
  console.log(`Weave Router configured for ${target} at ${paths.config}.`);
}

function parseArgs(args) {
  const options = { mode: "install", scope: "user", target: null, nonInteractive: false, help: false };
  for (let index = 0; index < args.length; index++) {
    const arg = args[index];
    if (["--claude", "--codex", "--opencode", "--pi"].includes(arg)) options.target = arg.slice(2);
    else if (arg === "--uninstall") options.mode = "uninstall";
    else if (arg === "--non-interactive") options.nonInteractive = true;
    else if (arg === "--local") options.baseUrl = "http://localhost:8080";
    else if (arg === "-h" || arg === "--help") options.help = true;
    else if (["--scope", "--dir", "--base-url", "--email"].includes(arg)) {
      const value = args[++index];
      if (!value) throw new Error(`${arg} requires a value.`);
      if (arg === "--scope") {
        if (!["user", "project"].includes(value)) throw new Error("--scope must be user or project.");
        options.scope = value;
      } else if (arg === "--dir") options.directory = value;
      else if (arg === "--base-url") options.baseUrl = value;
      else options.email = value;
    } else if (arg === "install" || arg === "setup") {
      if (arg === "setup") throw new Error("'setup' is not yet available in the native Windows installer; choose one client with --claude or --codex.");
    } else if (["off", "--off", "on", "--on", "status", "--status", "update", "--update", "models", "accounts", "login", "disable-routing"].includes(arg)) {
      options.mode = "unsupported";
    } else throw new Error(`Unsupported Windows installer option: ${arg}`);
  }
  return options;
}

function configPaths(root, target, scope, directory) {
  if (target === "claude") return { config: path.join(root, ".claude", "settings.json") };
  if (target === "codex") return { config: path.join(root, ".codex", "config.toml") };
  if (target === "opencode") {
    const configDirectory = scope === "project" || directory
      ? root
      : path.join(process.env.XDG_CONFIG_HOME || process.env.APPDATA || path.join(root, ".config"), "opencode");
    return { config: path.join(configDirectory, "opencode.json") };
  }
  const piRoot = path.join(root, ".pi", scope === "project" || directory ? "" : "agent");
  return { config: path.join(piRoot, "models.json"), settings: path.join(piRoot, "settings.json") };
}

function installClaude(filePath, baseUrl, key, email) {
  const settings = readJson(filePath);
  settings.env ||= {};
  settings.env.ANTHROPIC_BASE_URL = baseUrl;
  settings.env.ANTHROPIC_CUSTOM_HEADERS = [
    `X-Weave-Router-Key: ${key}`,
    ...(email ? [`X-Weave-User-Email: ${email}`] : []),
    "X-App: claude-code",
  ].join("\n");
  settings.env.ENABLE_TOOL_SEARCH = "true";
  settings.attribution = {
    ...(settings.attribution || {}),
    commit: "Co-Authored-By: Weave Router <router@weaveos.com>",
    pr: "🤖 Generated with [Weave Router](https://router.workweave.ai)",
  };
  writeJson(filePath, settings);
}

function installCodex(filePath, baseUrl, key, email) {
  const headers = { "X-Weave-Router-Key": key, ...(email ? { "X-Weave-User-Email": email } : {}), "X-App": "codex", "X-Weave-Codex-Native-Model-Pin": "1" };
  const block = [
    beginMarker,
    'model = "weave-auto"',
    'model_provider = "weave"',
    "",
    "[model_providers.weave]",
    'name = "Weave Router"',
    `base_url = ${toml(baseUrl + "/v1")}`,
    'wire_api = "responses"',
    "requires_openai_auth = true",
    `http_headers = { ${Object.entries(headers).map(([name, value]) => `${toml(name)} = ${toml(value)}`).join(", ")} }`,
    endMarker,
  ].join("\n");
  let original = readText(filePath);
  original = original.replace(new RegExp(`\\n?${escapeRegExp(beginMarker)}[\\s\\S]*?${escapeRegExp(endMarker)}\\n?`, "g"), "\n");
  original = original.replace(/^model_provider\s*=.*(?:\r?\n|$)/m, "");
  original = original.replace(/^\[model_providers\.weave\][\s\S]*?(?=^\[|\s*$)/m, "");
  const savedModel = original.match(/^model\s*=\s*("[^"]*"|'[^']*')\s*$/m)?.[1];
  if (savedModel && savedModel !== '"weave-auto"') {
    block.splice(1, 1);
  } else {
    original = original.replace(/^model\s*=.*(?:\r?\n|$)/m, "");
  }
  const firstSection = original.search(/^\s*\[/m);
  const updated = firstSection < 0 ? `${original.trim()}\n\n${block}\n` : `${original.slice(0, firstSection).trimEnd()}\n\n${block}\n\n${original.slice(firstSection)}`;
  writeText(filePath, updated);
}

function installOpenCode(filePath, baseUrl, key, email) {
  const settings = readJson(filePath);
  settings["$schema"] ||= "https://opencode.ai/config.json";
  settings.provider ||= {};
  settings.provider.weave = {
    npm: "@ai-sdk/openai",
    name: "Weave Router",
    options: {
      apiKey: key,
      baseURL: `${baseUrl}/v1`,
      headers: { "X-Weave-Router-Key": key, "X-App": "opencode", ...(email ? { "X-Weave-User-Email": email } : {}) },
    },
    models: { auto: { name: "Auto", reasoning: true, attachment: true, modalities: { input: ["text", "image", "pdf"], output: ["text"] }, limit: { context: 500000, output: 32000 } } },
  };
  settings.model = "weave/auto";
  writeJson(filePath, settings);
}

function installPi(modelsPath, settingsPath, baseUrl, key, email) {
  const models = readJson(modelsPath);
  models.providers ||= {};
  models.providers.weave = {
    baseUrl,
    api: "anthropic-messages",
    apiKey: key,
    authHeader: false,
    headers: { "X-Weave-Router-Key": key, "X-App": "pi", "x-weave-routing-marker": "off", "x-weave-routing-alpha": "0.8", "x-weave-routing-speed-weight": "0.05", "x-weave-routing-output-cost-ratio": "0.5", "x-weave-routing-expected-output-tokens": "3000", ...(email ? { "X-Weave-User-Email": email } : {}) },
    models: [{ id: "claude-sonnet-4-6", name: "Claude Sonnet 4.6 (via Weave Router)", reasoning: true, input: ["text", "image"], contextWindow: 1000000, maxTokens: 64000 }],
  };
  const settings = readJson(settingsPath);
  settings.packages = [...new Set([...(settings.packages || []).filter((item) => !/^npm:@(?:workweave\/(?:pi-)?router|weave-os\/router)$/.test(item)), `npm:${packageName}`])];
  settings.defaultProvider = "weave";
  settings.defaultModel ||= "claude-sonnet-4-6";
  writeJson(modelsPath, models);
  writeJson(settingsPath, settings);
}

function uninstall(paths, target) {
  if (target === "claude") {
    const settings = readJson(paths.config);
    if (settings.env) {
      delete settings.env.ANTHROPIC_BASE_URL;
      delete settings.env.ANTHROPIC_CUSTOM_HEADERS;
      delete settings.env.ENABLE_TOOL_SEARCH;
      if (!Object.keys(settings.env).length) delete settings.env;
    }
    delete settings.attribution;
    writeJson(paths.config, settings);
  } else if (target === "codex") {
    let original = readText(paths.config);
    original = original.replace(new RegExp(`\\n?${escapeRegExp(beginMarker)}[\\s\\S]*?${escapeRegExp(endMarker)}\\n?`, "g"), "\n");
    original = original.replace(/^model_provider\s*=\s*"weave"\s*(?:\r?\n|$)/m, "");
    original = original.replace(/^model\s*=\s*"weave-auto"\s*(?:\r?\n|$)/m, "");
    original = original.replace(/^\[model_providers\.weave\][\s\S]*?(?=^\[|\s*$)/m, "");
    writeText(paths.config, original);
  } else if (target === "opencode") {
    const settings = readJson(paths.config);
    delete settings.provider?.weave;
    if (settings.model === "weave/auto") delete settings.model;
    writeJson(paths.config, settings);
  } else {
    const models = readJson(paths.config);
    delete models.providers?.weave;
    writeJson(paths.config, models);
    const settings = readJson(paths.settings);
    settings.packages = (settings.packages || []).filter((item) => item !== `npm:${packageName}`);
    if (settings.defaultProvider === "weave") delete settings.defaultProvider;
    writeJson(paths.settings, settings);
  }
}

function readInstalledKey(paths, target) {
  try {
    if (target === "claude") {
      const header = readJson(paths.config).env?.ANTHROPIC_CUSTOM_HEADERS || "";
      return header.match(/^X-Weave-Router-Key:\s*(\S+)/m)?.[1] || "";
    }
    if (target === "codex") return readText(paths.config).match(/"X-Weave-Router-Key"\s*=\s*"([^"]+)"/)?.[1] || "";
    if (target === "opencode") return readJson(paths.config).provider?.weave?.options?.headers?.["X-Weave-Router-Key"] || "";
    return readJson(paths.config).providers?.weave?.headers?.["X-Weave-Router-Key"] || "";
  } catch { return ""; }
}

async function selectTarget() {
  const terminal = readline.createInterface({ input: stdin, output: stdout });
  try {
    const answer = (await terminal.question("Install target: [1] Claude Code (default), [2] Codex, [3] OpenCode, [4] pi: ")).trim();
    return ({ "": "claude", "1": "claude", "2": "codex", "3": "opencode", "4": "pi" })[answer] || (() => { throw new Error("Choose 1, 2, 3, or 4."); })();
  } finally { terminal.close(); }
}

async function promptForKey() {
  stdout.write("Paste your key (rk_…): ");
  if (!stdin.isTTY || typeof stdin.setRawMode !== "function") {
    const terminal = readline.createInterface({ input: stdin, output: stdout });
    try { return (await terminal.question("")).trim(); }
    finally { terminal.close(); }
  }
  readline.emitKeypressEvents(stdin);
  stdin.setRawMode(true);
  stdin.resume();
  return new Promise((resolve, reject) => {
    let key = "";
    const cleanup = () => {
      stdin.removeListener("keypress", onKeypress);
      stdin.setRawMode(false);
      stdout.write("\n");
    };
    const onKeypress = (character, event) => {
      if (event.ctrl && event.name === "c") {
        cleanup();
        reject(new Error("Cancelled."));
      } else if (event.name === "return" || event.name === "enter") {
        cleanup();
        resolve(key.trim());
      } else if (event.name === "backspace") {
        key = key.slice(0, -1);
      } else if (!event.ctrl && !event.meta) {
        key += character;
      }
    };
    stdin.on("keypress", onKeypress);
  });
}

function readJson(filePath) {
  try {
    const content = fs.readFileSync(filePath, "utf8");
    const value = JSON.parse(content);
    if (!value || Array.isArray(value) || typeof value !== "object") throw new Error("expected a JSON object");
    return value;
  } catch (error) {
    if (error.code === "ENOENT") return {};
    throw new Error(`Cannot safely read ${filePath}: ${error.message}`);
  }
}

function writeJson(filePath, value) { writeText(filePath, `${JSON.stringify(value, null, 2)}\n`); }
function readText(filePath) { try { return fs.readFileSync(filePath, "utf8"); } catch (error) { if (error.code === "ENOENT") return ""; throw error; } }
function writeText(filePath, value) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  const temporaryPath = `${filePath}.weave-${process.pid}.tmp`;
  fs.writeFileSync(temporaryPath, value, { mode: 0o600 });
  fs.renameSync(temporaryPath, filePath);
}
function refuseSymlink(filePath) {
  try { if (fs.lstatSync(filePath).isSymbolicLink()) throw new Error(`${filePath} is a symlink; refusing to write through it.`); }
  catch (error) { if (error.code !== "ENOENT") throw error; }
}
function findGitRoot(directory) {
  let current = path.resolve(directory);
  while (true) {
    if (fs.existsSync(path.join(current, ".git"))) return current;
    const parent = path.dirname(current);
    if (parent === current) return null;
    current = parent;
  }
}
function osHome() { return process.env.USERPROFILE || process.env.HOME; }
function toml(value) { return `"${String(value).replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`; }
function escapeRegExp(value) { return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }

main().catch((error) => {
  console.error(`Weave Router: ${error.message}`);
  process.exitCode = 1;
});
