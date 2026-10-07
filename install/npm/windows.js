#!/usr/bin/env node
// Native Windows entrypoint. PowerShell users do not need Git Bash, jq, or
// another installation beyond the npx command they already ran.

const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { emitKeypressEvents } = require("node:readline");
const readline = require("node:readline/promises");
const { stdin, stdout } = require("node:process");
const TOML = require("smol-toml");

const defaultBaseUrl = process.env.WEAVE_ROUTER_URL || "https://router.workweave.ai";
const packageName = process.env.WEAVE_ROUTER_NPM_PACKAGE || "@weave-os/router";
const claudeAttribution = {
  commit: "Co-Authored-By: Weave Router <router@weaveos.com>",
  pr: "🤖 Generated with [Weave Router](https://router.workweave.ai)",
};

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
  if (options.scope === "project" && !options.directory) {
    options.directory = findGitRoot(process.cwd());
    if (!options.directory) throw new Error("--scope project must run inside a git repo, or pass --dir <path>.");
  }

  const root = path.resolve(options.directory || process.env.USERPROFILE || process.env.HOME);
  const target = options.target || (options.nonInteractive ? "claude" : await selectTarget());
  const paths = configPaths(root, target, options.scope, options.explicitDirectory);
  for (const filePath of Object.values(paths).filter(Boolean)) {
    if (options.scope === "project") refuseSymlink(path.dirname(filePath));
    refuseSymlink(filePath);
  }

  if (options.mode === "uninstall") {
    uninstall(paths, target);
    console.log(`Weave Router removed from ${target} settings.`);
    return;
  }
  if (options.scope === "project") ensureProjectFilesPrivate(root, target);

  const baseUrl = (options.baseUrl || defaultBaseUrl).replace(/\/+$/, "");
  let key = process.env.WEAVE_ROUTER_KEY || readInstalledKey(paths, target);
  if (!key && options.nonInteractive) throw new Error("--non-interactive requires WEAVE_ROUTER_KEY.");
  if (!key) {
    console.log(`Get your Weave Router API key at ${baseUrl}`);
    key = await promptForKey();
  }
  if (!key) throw new Error("No key provided.");

  const keyIsValid = await fetch(`${baseUrl}/validate`, {
    headers: { "X-Weave-Router-Key": key },
    signal: AbortSignal.timeout(5000),
  }).then((response) => response.ok).catch(() => false);
  if (!keyIsValid) console.warn("Warning: could not validate the key. Check the router URL and key if requests fail.");

  const email = options.email || process.env.WEAVE_USER_EMAIL || "";
  if (target === "claude") installClaude(paths, baseUrl, key, email);
  else if (target === "codex") installCodex(paths.config, baseUrl, key, email);
  else if (target === "opencode") installOpenCode(paths, baseUrl, key, email);
  else installPi(paths, baseUrl, key, email);
  console.log(`Weave Router configured for ${target} at ${paths.config}.`);
}

function parseArgs(args) {
  const options = { mode: "install", scope: "user", target: null, nonInteractive: false, explicitDirectory: false, help: false };
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
      } else if (arg === "--dir") { options.directory = value; options.explicitDirectory = true; }
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
  if (target === "claude") return {
    config: path.join(root, ".claude", "settings.json"),
    localConfig: scope === "project" ? path.join(root, ".claude", "settings.local.json") : null,
    state: path.join(root, ".claude", ".weave-router-state.json"),
  };
  if (target === "codex") return { config: path.join(root, ".codex", "config.toml") };
  if (target === "opencode") {
    const configDirectory = scope === "project" || directory
      ? root
      : path.join(process.env.XDG_CONFIG_HOME || path.join(root, ".config"), "opencode");
    return { config: path.join(configDirectory, "opencode.json"), parked: path.join(configDirectory, ".weave-parked.json") };
  }
  const piRoot = path.join(root, ".pi", scope === "project" || directory ? "" : "agent");
  return { config: path.join(piRoot, "models.json"), settings: path.join(piRoot, "settings.json"), state: path.join(piRoot, ".weave-router-state.json") };
}

function ensureProjectFilesPrivate(root, target) {
  const entries = {
    claude: [".claude/settings.local.json", ".claude/.weave-router-state.json"],
    codex: [".codex/config.toml"],
    opencode: ["opencode.json", ".weave-parked.json"],
    pi: [".pi/models.json", ".pi/settings.json", ".pi/.weave-router-state.json"],
  }[target];
  const gitignorePath = path.join(root, ".gitignore");
  refuseSymlink(gitignorePath);
  const trackedFiles = findGitRoot(root) ? execFileSync("git", ["-C", root, "ls-files", "--", ...entries], { encoding: "utf8" }).trim().split(/\r?\n/).filter(Boolean) : [];
  if (trackedFiles.length) throw new Error(`Project config is already tracked by git and may expose credentials: ${trackedFiles.join(", ")}. Untrack it before installing.`);
  const existingEntries = fs.existsSync(gitignorePath) ? fs.readFileSync(gitignorePath, "utf8").split(/\r?\n/) : [];
  const missingEntries = entries.filter((entry) => !existingEntries.includes(entry));
  if (missingEntries.length) {
    fs.mkdirSync(root, { recursive: true });
    fs.appendFileSync(gitignorePath, `${existingEntries.length && existingEntries.at(-1) ? "\n" : ""}${missingEntries.join("\n")}\n`);
  }
}

function installClaude(paths, baseUrl, key, email) {
  const settings = readJson(paths.config);
  const localSettings = paths.localConfig ? readJson(paths.localConfig) : null;
  const state = readJson(paths.state);
  state.env ||= Object.fromEntries(["ANTHROPIC_BASE_URL", "ENABLE_TOOL_SEARCH"].map((field) => [field, { exists: Object.hasOwn(settings.env || {}, field), value: settings.env?.[field] }]));
  state.attribution ||= Object.fromEntries(Object.keys(claudeAttribution).map((field) => [field, { exists: Object.hasOwn(settings.attribution || {}, field), value: settings.attribution?.[field] }]));
  state.originalAppHeaders ||= {
    config: (settings.env?.ANTHROPIC_CUSTOM_HEADERS || "").split("\n").filter((header) => /^X-App:/i.test(header)),
    localConfig: (localSettings?.env?.ANTHROPIC_CUSTOM_HEADERS ?? settings.env?.ANTHROPIC_CUSTOM_HEADERS ?? "").split("\n").filter((header) => /^X-App:/i.test(header)),
  };
  state.installedBaseUrl = baseUrl;
  const customHeaders = localSettings?.env?.ANTHROPIC_CUSTOM_HEADERS ?? settings.env?.ANTHROPIC_CUSTOM_HEADERS ?? "";
  const previousHeaders = customHeaders.split("\n").filter((header) => !/^X-(?:Weave-Router-Key|Weave-User-Email|Weave-User-Name|App):/i.test(header));
  const routerHeaders = [
    `X-Weave-Router-Key: ${key}`,
    ...(email ? [`X-Weave-User-Email: ${email}`] : []),
    "X-App: claude-code",
  ];
  settings.env ||= {};
  settings.env.ANTHROPIC_BASE_URL = baseUrl;
  if (paths.localConfig) {
    if (settings.env.ANTHROPIC_CUSTOM_HEADERS) settings.env.ANTHROPIC_CUSTOM_HEADERS = settings.env.ANTHROPIC_CUSTOM_HEADERS.split("\n").filter((header) => !/^X-(?:Weave-Router-Key|Weave-User-Email|Weave-User-Name):/i.test(header)).join("\n");
    localSettings.env ||= {};
    localSettings.env.ANTHROPIC_CUSTOM_HEADERS = [...previousHeaders, ...routerHeaders].filter(Boolean).join("\n");
  } else {
    settings.env.ANTHROPIC_CUSTOM_HEADERS = [...previousHeaders, ...routerHeaders].filter(Boolean).join("\n");
  }
  settings.env.ENABLE_TOOL_SEARCH = "true";
  settings.attribution = { ...(settings.attribution || {}), ...claudeAttribution };
  writeJson(paths.config, settings);
  if (paths.localConfig) writeJson(paths.localConfig, localSettings);
  writeJson(paths.state, state);
}

function installCodex(filePath, baseUrl, key, email) {
  const settings = readToml(filePath);
  const currentModel = settings.model;
  const hasUserModel = typeof currentModel === "string" && currentModel !== "weave-auto";
  settings.model_provider = "weave";
  if (!hasUserModel) settings.model = "weave-auto";
  settings.model_providers ||= {};
  settings.model_providers.weave = {
    name: "Weave Router",
    base_url: `${baseUrl}/v1`,
    wire_api: "responses",
    requires_openai_auth: true,
    http_headers: {
      "X-Weave-Router-Key": key,
      ...(email ? { "X-Weave-User-Email": email } : {}),
      "X-App": "codex",
      "X-Weave-Codex-Native-Model-Pin": "1",
    },
  };
  writeToml(filePath, settings);
}

function installOpenCode(paths, baseUrl, key, email) {
  const settings = readJson(paths.config);
  const parked = readJson(paths.parked);
  if (!Object.hasOwn(parked, "direct_model") && typeof settings.model === "string" && !settings.model.startsWith("weave/")) {
    writeJson(paths.parked, { direct_model: settings.model });
  }
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
  writeJson(paths.config, settings);
}

function installPi(paths, baseUrl, key, email) {
  const models = readJson(paths.config);
  models.providers ||= {};
  models.providers.weave = {
    baseUrl,
    api: "anthropic-messages",
    apiKey: key,
    authHeader: false,
    headers: { "X-Weave-Router-Key": key, "X-App": "pi", "x-weave-routing-marker": "off", "x-weave-routing-alpha": "0.8", "x-weave-routing-speed-weight": "0.05", "x-weave-routing-output-cost-ratio": "0.5", "x-weave-routing-expected-output-tokens": "3000", ...(email ? { "X-Weave-User-Email": email } : {}) },
    models: [{ id: "claude-sonnet-4-6", name: "Claude Sonnet 4.6 (via Weave Router)", reasoning: true, input: ["text", "image"], contextWindow: 1000000, maxTokens: 64000 }],
  };
  const settings = readJson(paths.settings);
  const state = readJson(paths.state);
  if (!Object.hasOwn(state, "defaultProvider")) state.defaultProvider = { exists: Object.hasOwn(settings, "defaultProvider"), value: settings.defaultProvider };
  if (!Object.hasOwn(state, "defaultModel")) state.defaultModel = { exists: Object.hasOwn(settings, "defaultModel"), value: settings.defaultModel };
  settings.packages = [...new Set([...(settings.packages || []).filter((item) => !/^npm:@(?:workweave\/(?:pi-)?router|weave-os\/router)$/.test(item)), `npm:${packageName}`])];
  settings.defaultProvider = "weave";
  settings.defaultModel ||= "claude-sonnet-4-6";
  writeJson(paths.config, models);
  writeJson(paths.settings, settings);
  writeJson(paths.state, state);
}

function uninstall(paths, target) {
  if (target === "claude") {
    if (!fs.existsSync(paths.config) && !fs.existsSync(paths.localConfig || "")) return;
    const settings = readJson(paths.config);
    const localSettings = paths.localConfig ? readJson(paths.localConfig) : null;
    const state = readJson(paths.state);
    const hadConfig = fs.existsSync(paths.config);
    const hadLocalConfig = paths.localConfig && fs.existsSync(paths.localConfig);
    if (!fs.existsSync(paths.state) && ![settings.env?.ANTHROPIC_CUSTOM_HEADERS, localSettings?.env?.ANTHROPIC_CUSTOM_HEADERS].some((headers) => /^X-Weave-Router-Key:/m.test(headers || ""))) return;
    restoreField(settings.env, "ANTHROPIC_BASE_URL", state.env?.ANTHROPIC_BASE_URL, state.installedBaseUrl || settings.env?.ANTHROPIC_BASE_URL);
    restoreField(settings.env, "ENABLE_TOOL_SEARCH", state.env?.ENABLE_TOOL_SEARCH, "true");
    const customHeaders = (value, originalAppHeaders) => {
      const preservedHeaders = (value || "").split("\n").filter((header) => !/^X-Weave-(?:Router-Key|User-Email|User-Name):/i.test(header) && !/^X-App:\s*claude-code\s*$/i.test(header));
      if (!preservedHeaders.some((header) => /^X-App:/i.test(header))) preservedHeaders.push(...(originalAppHeaders || []));
      return preservedHeaders.filter(Boolean).join("\n");
    };
    if (settings.env?.ANTHROPIC_CUSTOM_HEADERS) {
      settings.env.ANTHROPIC_CUSTOM_HEADERS = customHeaders(settings.env.ANTHROPIC_CUSTOM_HEADERS, state.originalAppHeaders?.config);
      if (!settings.env.ANTHROPIC_CUSTOM_HEADERS) delete settings.env.ANTHROPIC_CUSTOM_HEADERS;
    }
    if (localSettings?.env?.ANTHROPIC_CUSTOM_HEADERS) {
      localSettings.env.ANTHROPIC_CUSTOM_HEADERS = customHeaders(localSettings.env.ANTHROPIC_CUSTOM_HEADERS, state.originalAppHeaders?.localConfig);
      if (!localSettings.env.ANTHROPIC_CUSTOM_HEADERS) delete localSettings.env.ANTHROPIC_CUSTOM_HEADERS;
      if (!Object.keys(localSettings.env).length) delete localSettings.env;
    }
    for (const field of Object.keys(claudeAttribution)) restoreField(settings.attribution, field, state.attribution?.[field], claudeAttribution[field]);
    if (settings.env && !Object.keys(settings.env).length) delete settings.env;
    if (settings.attribution && !Object.keys(settings.attribution).length) delete settings.attribution;
    if (hadConfig) writeJson(paths.config, settings);
    if (hadLocalConfig) writeJson(paths.localConfig, localSettings);
    fs.rmSync(paths.state, { force: true });
  } else if (target === "codex") {
    if (!fs.existsSync(paths.config)) return;
    const settings = readToml(paths.config);
    if (!settings.model_providers?.weave?.http_headers?.["X-Weave-Router-Key"]) return;
    if (settings.model_provider === "weave") delete settings.model_provider;
    if (settings.model === "weave-auto") delete settings.model;
    delete settings.model_providers?.weave;
    if (settings.model_providers && !Object.keys(settings.model_providers).length) delete settings.model_providers;
    writeToml(paths.config, settings);
  } else if (target === "opencode") {
    if (!fs.existsSync(paths.config)) return;
    const settings = readJson(paths.config);
    if (!settings.provider?.weave) return;
    const parked = readJson(paths.parked);
    delete settings.provider?.weave;
    if (settings.model === "weave/auto") {
      if (typeof parked.direct_model === "string") settings.model = parked.direct_model;
      else delete settings.model;
    }
    writeJson(paths.config, settings);
    fs.rmSync(paths.parked, { force: true });
  } else {
    if (!fs.existsSync(paths.config) && !fs.existsSync(paths.settings)) return;
    const models = readJson(paths.config);
    delete models.providers?.weave;
    if (fs.existsSync(paths.config)) writeJson(paths.config, models);
    if (fs.existsSync(paths.settings)) {
      const settings = readJson(paths.settings);
      const state = readJson(paths.state);
      settings.packages = (settings.packages || []).filter((item) => item !== `npm:${packageName}`);
      restoreField(settings, "defaultProvider", state.defaultProvider, "weave");
      restoreField(settings, "defaultModel", state.defaultModel, "claude-sonnet-4-6");
      writeJson(paths.settings, settings);
    }
    fs.rmSync(paths.state, { force: true });
  }
}

function restoreField(record, field, savedValue, installedValue) {
  if (!record || record[field] !== installedValue) return;
  if (savedValue?.exists) record[field] = savedValue.value;
  else delete record[field];
}

function readInstalledKey(paths, target) {
  try {
    if (target === "claude") {
      return (readJson(paths.localConfig || paths.config).env?.ANTHROPIC_CUSTOM_HEADERS || "").match(/^X-Weave-Router-Key:\s*(\S+)/m)?.[1] || "";
    }
    if (target === "codex") return readToml(paths.config).model_providers?.weave?.http_headers?.["X-Weave-Router-Key"] || "";
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
  emitKeypressEvents(stdin);
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
    const value = JSON.parse(fs.readFileSync(filePath, "utf8"));
    if (!value || Array.isArray(value) || typeof value !== "object") throw new Error("expected a JSON object");
    return value;
  } catch (error) {
    if (error.code === "ENOENT") return {};
    throw new Error(`Cannot safely read ${filePath}: ${error.message}`);
  }
}

function writeJson(filePath, value) { writeText(filePath, `${JSON.stringify(value, null, 2)}\n`); }
function readToml(filePath) { return TOML.parse(readText(filePath), { integersAsBigInt: true }); }
function writeToml(filePath, settings) { writeText(filePath, TOML.stringify(settings, { numbersAsFloat: true })); }
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
main().catch((error) => {
  console.error(`Weave Router: ${error.message}`);
  process.exitCode = 1;
});
