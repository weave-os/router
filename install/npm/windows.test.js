const assert = require("node:assert/strict");
const { execFile, execFileSync } = require("node:child_process");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const vm = require("node:vm");
const TOML = require("smol-toml");
const { after, before, test } = require("node:test");
const { promisify } = require("node:util");
const runFile = promisify(execFile);

const temporaryHome = fs.mkdtempSync(path.join(os.tmpdir(), "weave-router-windows-"));
const packageDirectory = __dirname;
let baseUrl;
const server = http.createServer((request, response) => {
  response.writeHead(request.url === "/validate" ? 200 : 404);
  response.end("{}");
});

server.listen(0, "127.0.0.1");
before(async () => {
  await new Promise((resolve) => server.once("listening", resolve));
  baseUrl = `http://127.0.0.1:${server.address().port}`;
});
after(() => {
  server.closeAllConnections();
  server.close();
  fs.rmSync(temporaryHome, { recursive: true, force: true });
});

async function runInstaller(...args) {
  return runInstallerAt(temporaryHome, args);
}

async function runInstallerAt(home, args, { cwd, envOverrides = {} } = {}) {
  const processResult = await runFile(process.execPath, [path.join(packageDirectory, "windows.js"), ...args], {
    cwd,
    env: {
      ...process.env,
      USERPROFILE: home,
      XDG_CONFIG_HOME: path.join(home, ".config"),
      WEAVE_ROUTER_KEY: "rk_test_native_windows",
      ...envOverrides,
    },
  });
  return processResult.stdout;
}

test("the native installer writes each client's config and keeps existing settings", async () => {
  const claudeSettings = path.join(temporaryHome, ".claude", "settings.json");
  fs.mkdirSync(path.dirname(claudeSettings), { recursive: true });
  fs.writeFileSync(claudeSettings, JSON.stringify({ env: { KEEP_ME: "yes", ANTHROPIC_BASE_URL: "https://api.anthropic.com", ANTHROPIC_CUSTOM_HEADERS: "X-Custom: keep\nX-App: custom-client", ENABLE_TOOL_SEARCH: "false" }, attribution: { commit: "original commit", custom: "keep" }, custom: true }));
  const codexConfigPath = path.join(temporaryHome, ".codex", "config.toml");
  fs.mkdirSync(path.dirname(codexConfigPath), { recursive: true });
  fs.writeFileSync(codexConfigPath, '# Keep my preferences\nmodel = "gpt-4.1"\nmodel_provider = "openai" # Original provider\n[profiles.work]\nmodel_provider = "openai"\ninstructions = """\n[not_a_table]\nkeep this text\n"""\n[model_providers.openai]\nname = "OpenAI"\n');
  const openCodeConfigPath = path.join(temporaryHome, ".config", "opencode", "opencode.json");
  fs.mkdirSync(path.dirname(openCodeConfigPath), { recursive: true });
  fs.writeFileSync(openCodeConfigPath, JSON.stringify({ model: "openai/gpt-4.1", custom: "keep" }));
  const piSettingsPath = path.join(temporaryHome, ".pi", "agent", "settings.json");
  fs.mkdirSync(path.dirname(piSettingsPath), { recursive: true });
  fs.writeFileSync(piSettingsPath, JSON.stringify({ defaultProvider: "anthropic", defaultModel: "user-model", packages: ["npm:user-package"] }));

  for (const target of ["claude", "codex", "opencode", "pi"]) {
    await runInstaller(`--${target}`, "--non-interactive", "--base-url", baseUrl);
  }

  const claude = JSON.parse(fs.readFileSync(claudeSettings, "utf8"));
  assert.equal(claude.env.KEEP_ME, "yes");
  assert.equal(claude.env.ANTHROPIC_BASE_URL, baseUrl);
  assert.match(claude.env.ANTHROPIC_CUSTOM_HEADERS, /rk_test_native_windows/);
  assert.match(claude.env.ANTHROPIC_CUSTOM_HEADERS, /X-Custom: keep/);
  const codexConfig = fs.readFileSync(path.join(temporaryHome, ".codex", "config.toml"), "utf8");
  assert.match(codexConfig, /model = "gpt-4.1"/);
  assert.match(codexConfig, /model_provider = "weave"/);
  assert.equal(TOML.parse(codexConfig).profiles.work.model_provider, "openai");
  await runInstaller("--codex", "--non-interactive", "--base-url", baseUrl, "--email", "user@example.com");
  const reinstalledCodex = TOML.parse(fs.readFileSync(codexConfigPath, "utf8"));
  assert.equal(reinstalledCodex.model, "gpt-4.1");
  assert.equal(reinstalledCodex.profiles.work.instructions, "[not_a_table]\nkeep this text\n");
  assert.equal(reinstalledCodex.model_providers.weave.http_headers["X-Weave-User-Email"], "user@example.com");
  assert.equal(JSON.parse(fs.readFileSync(path.join(temporaryHome, ".config", "opencode", "opencode.json"), "utf8")).model, "weave/auto");
  assert.equal(JSON.parse(fs.readFileSync(path.join(temporaryHome, ".pi", "agent", "models.json"), "utf8")).providers.weave.baseUrl, baseUrl);
});

test("native uninstall removes the managed Claude routing variables", async () => {
  await runInstaller("--uninstall", "--claude");
  const claude = JSON.parse(fs.readFileSync(path.join(temporaryHome, ".claude", "settings.json"), "utf8"));
  assert.equal(claude.env.KEEP_ME, "yes");
  assert.equal(claude.env.ANTHROPIC_BASE_URL, "https://api.anthropic.com");
  assert.equal(claude.env.ANTHROPIC_CUSTOM_HEADERS, "X-Custom: keep\nX-App: custom-client");
  assert.equal(claude.env.ENABLE_TOOL_SEARCH, "false");
  assert.equal(claude.attribution.commit, "original commit");
  assert.equal(claude.attribution.custom, "keep");
  assert.equal(claude.custom, true);
});

test("native uninstall removes Codex, OpenCode, and pi router settings", async () => {
  await runInstaller("--uninstall", "--codex");
  await runInstaller("--uninstall", "--opencode");
  await runInstaller("--uninstall", "--pi");

  const codexConfig = fs.readFileSync(path.join(temporaryHome, ".codex", "config.toml"), "utf8");
  assert.doesNotMatch(codexConfig, /model_providers\.weave|model_provider = "weave"/);
  assert.equal(TOML.parse(codexConfig).profiles.work.model_provider, "openai");
  assert.equal(TOML.parse(codexConfig).model, "gpt-4.1");
  assert.equal(TOML.parse(codexConfig).model_provider, "openai");
  assert.match(codexConfig, /# Keep my preferences/);
  assert.match(codexConfig, /# Original provider/);
  const openCodeConfig = JSON.parse(fs.readFileSync(path.join(temporaryHome, ".config", "opencode", "opencode.json"), "utf8"));
  assert.equal(openCodeConfig.provider.weave, undefined);
  assert.equal(openCodeConfig.model, "openai/gpt-4.1");
  assert.equal(openCodeConfig.custom, "keep");
  const piModels = JSON.parse(fs.readFileSync(path.join(temporaryHome, ".pi", "agent", "models.json"), "utf8"));
  assert.equal(piModels.providers.weave, undefined);
  const piSettings = JSON.parse(fs.readFileSync(path.join(temporaryHome, ".pi", "agent", "settings.json"), "utf8"));
  assert.equal(piSettings.defaultProvider, "anthropic");
  assert.equal(piSettings.defaultModel, "user-model");
  assert.deepEqual(piSettings.packages, ["npm:user-package"]);
});

test("uninstalling an absent target does not create configuration files", async () => {
  const emptyHome = path.join(temporaryHome, "empty");
  for (const target of ["claude", "codex", "opencode", "pi"]) await runInstallerAt(emptyHome, ["--uninstall", `--${target}`]);
  assert.equal(fs.existsSync(emptyHome), false);
});

test("OpenCode uses its XDG home fallback even when APPDATA is set", async () => {
  const home = path.join(temporaryHome, "opencode-home");
  const appData = path.join(home, "appdata");
  await runInstallerAt(home, ["--opencode", "--non-interactive", "--base-url", baseUrl], { envOverrides: { XDG_CONFIG_HOME: "", APPDATA: appData } });
  assert.equal(fs.existsSync(path.join(home, ".config", "opencode", "opencode.json")), true);
  assert.equal(fs.existsSync(path.join(appData, "opencode", "opencode.json")), false);
});

test("project setup protects credentials and refuses already tracked credential files", async () => {
  const project = path.join(temporaryHome, "project");
  fs.mkdirSync(project);
  await runFile("git", ["init", "-q", project]);
  for (const target of ["claude", "codex", "opencode", "pi"]) await runInstallerAt(project, [`--${target}`, "--scope", "project", "--non-interactive", "--base-url", baseUrl], { cwd: project, envOverrides: { PATH: "" } });
  assert.doesNotMatch(fs.readFileSync(path.join(project, ".claude", "settings.json"), "utf8"), /rk_test_native_windows/);
  assert.match(fs.readFileSync(path.join(project, ".claude", "settings.local.json"), "utf8"), /rk_test_native_windows/);
  const untrackedFiles = (await runFile("git", ["-C", project, "ls-files", "--others", "--exclude-standard"])).stdout;
  for (const config of [".claude/settings.local.json", ".codex/config.toml", "opencode.json", ".pi/models.json"]) assert.equal(untrackedFiles.includes(config), false);
  const trackedProject = path.join(temporaryHome, "tracked-project");
  const trackedConfig = path.join(trackedProject, ".codex", "config.toml");
  fs.mkdirSync(path.dirname(trackedConfig), { recursive: true });
  fs.writeFileSync(trackedConfig, 'model = "user-model"\n');
  await runFile("git", ["init", "-q", "--separate-git-dir", path.join(temporaryHome, "tracked-metadata"), trackedProject]);
  await runFile("git", ["-C", trackedProject, "add", ".codex/config.toml"]);
  await assert.rejects(runInstallerAt(trackedProject, ["--codex", "--scope", "project", "--dir", trackedProject, "--non-interactive", "--base-url", baseUrl], { cwd: trackedProject, envOverrides: { PATH: "" } }), (error) => error.stderr.includes("already tracked"));
  assert.equal(fs.readFileSync(trackedConfig, "utf8"), 'model = "user-model"\n');
});

test("Claude migrates an untracked legacy project key but never adopts a tracked key", async () => {
  for (const tracked of [false, true]) {
    const project = path.join(temporaryHome, tracked ? "shared-key-project" : "legacy-key-project");
    const config = path.join(project, ".claude", "settings.json");
    fs.mkdirSync(path.dirname(config), { recursive: true });
    fs.writeFileSync(config, JSON.stringify({ env: { ANTHROPIC_CUSTOM_HEADERS: "X-Weave-Router-Key: rk_legacy_native" } }));
    await runFile("git", ["init", "-q", project]);
    if (tracked) await runFile("git", ["-C", project, "add", ".claude/settings.json"]);
    const installation = runInstallerAt(project, ["--claude", "--scope", "project", "--non-interactive", "--base-url", baseUrl], { cwd: project, envOverrides: { PATH: "", WEAVE_ROUTER_KEY: "" } });
    const localConfig = path.join(project, ".claude", "settings.local.json");
    if (tracked) {
      await assert.rejects(installation, (error) => error.stderr.includes("requires WEAVE_ROUTER_KEY"));
      assert.equal(fs.existsSync(localConfig), false);
    } else {
      await installation;
      assert.match(fs.readFileSync(localConfig, "utf8"), /rk_legacy_native/);
      assert.doesNotMatch(fs.readFileSync(config, "utf8"), /rk_legacy_native/);
    }
  }
});

test("the Windows wrapper bypasses WSL and forwards uninstall to the native installer", () => {
  const spawnCalls = [];
  const exitSignal = {};
  const processStub = { platform: "win32", argv: ["node", "bin.js", "--uninstall", "--claude"], execPath: "C:\\node\\node.exe", env: { PATH: "C:\\Windows\\System32" }, exit: () => { throw exitSignal; } };
  const modules = {
    "node:child_process": { spawnSync: (executable, args) => { spawnCalls.push({ executable, args }); return { status: 0, stdout: executable.endsWith("bash.exe") ? "Linux\n" : "" }; } },
    "node:fs": { readFileSync: () => '{"name":"@weave-os/router"}', existsSync: (filePath) => filePath.startsWith("C:\\package") || filePath === "C:\\Windows\\System32\\bash.exe" },
    "node:path": path.win32,
  };
  assert.throws(() => vm.runInNewContext(fs.readFileSync(path.join(packageDirectory, "bin.js"), "utf8"), { require: (module) => modules[module], __dirname: "C:\\package", process: processStub, console }), (error) => error === exitSignal);
  const nativeCall = spawnCalls.find((call) => call.executable === processStub.execPath);
  assert.deepEqual(Array.from(nativeCall.args), ["C:\\package\\windows.js", "--uninstall", "--claude"]);
  assert.equal(spawnCalls.some((call) => call.args.includes("C:\\package\\uninstall.sh")), false);
});

test("the Windows wrapper probes Git Bash without profile or BASH_ENV banners", () => {
  const spawnCalls = [];
  const exitSignal = {};
  const gitBash = "C:\\Program Files\\Git\\bin\\bash.exe";
  const processStub = { platform: "win32", argv: ["node", "bin.js", "--uninstall", "--claude"], execPath: "C:\\node\\node.exe", env: { PATH: "", BASH_ENV: "C:\\banner.bash" }, exit: () => { throw exitSignal; } };
  const modules = {
    "node:child_process": { spawnSync: (executable, args, options) => {
      spawnCalls.push({ executable, args, options });
      const probeIgnoresStartupFiles = args.includes("--noprofile") && args.includes("--norc") && options.env?.BASH_ENV === "";
      return { status: 0, stdout: probeIgnoresStartupFiles ? "MINGW64_NT\n" : "Welcome to my shell\nMINGW64_NT\n" };
    } },
    "node:fs": { readFileSync: () => '{"name":"@weave-os/router"}', existsSync: (filePath) => filePath.startsWith("C:\\package") || filePath === gitBash },
    "node:path": path.win32,
  };
  assert.throws(() => vm.runInNewContext(fs.readFileSync(path.join(packageDirectory, "bin.js"), "utf8"), { require: (module) => modules[module], __dirname: "C:\\package", process: processStub, console }), (error) => error === exitSignal);
  const installerCall = spawnCalls.find((call) => call.args.includes("C:\\package\\uninstall.sh"));
  assert.equal(installerCall.executable, gitBash);
  assert.deepEqual(Array.from(installerCall.args), ["C:\\package\\uninstall.sh", "--claude"]);
});

test("native installer rejects control characters in email headers", async () => {
  await assert.rejects(runInstaller("--codex", "--non-interactive", "--base-url", baseUrl, "--email", "user\n@example.com"), (error) => error.stderr.includes("control characters"));
});

test("project setup handles large Git indexes without listing unrelated paths", async () => {
  const project = path.join(temporaryHome, "large-project");
  fs.mkdirSync(project);
  await runFile("git", ["init", "-q", project]);
  const fixture = path.join(project, "fixture");
  fs.writeFileSync(fixture, "synthetic fixture");
  const objectId = (await runFile("git", ["-C", project, "hash-object", "-w", fixture])).stdout.trim();
  const indexEntries = Array.from({ length: 12000 }, (_, index) => `100644 ${objectId}\tfiles/${"x".repeat(100)}-${index}\n`).join("");
  execFileSync("git", ["-C", project, "update-index", "--index-info"], { input: indexEntries });
  await runInstallerAt(project, ["--codex", "--scope", "project", "--non-interactive", "--base-url", baseUrl], { cwd: project });
  assert.equal(TOML.parse(fs.readFileSync(path.join(project, ".codex", "config.toml"), "utf8")).model_provider, "weave");
});

test("Codex uninstall after upgrading a stateless install removes the managed provider", async () => {
  const home = path.join(temporaryHome, "codex-upgrade");
  const config = path.join(home, ".codex", "config.toml");
  fs.mkdirSync(path.dirname(config), { recursive: true });
  fs.writeFileSync(config, 'model_provider = "weave"\nmodel = "weave-auto"\n[model_providers.weave]\nname = "Weave Router"\n');
  await runInstallerAt(home, ["--codex", "--non-interactive", "--base-url", baseUrl]);
  await runInstallerAt(home, ["--codex", "--uninstall"]);
  const settings = TOML.parse(fs.readFileSync(config, "utf8"));
  assert.equal(settings.model_provider, undefined);
  assert.equal(settings.model_providers?.weave, undefined);
});
