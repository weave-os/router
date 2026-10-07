const assert = require("node:assert/strict");
const { execFile } = require("node:child_process");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
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
  const response = await runFile(process.execPath, [path.join(packageDirectory, "windows.js"), ...args], {
    env: {
      ...process.env,
      USERPROFILE: temporaryHome,
      WEAVE_ROUTER_KEY: "rk_test_native_windows",
    },
  });
  return response.stdout;
}

test("the native installer writes each client's config and keeps existing settings", async () => {
  const claudeSettings = path.join(temporaryHome, ".claude", "settings.json");
  fs.mkdirSync(path.dirname(claudeSettings), { recursive: true });
  fs.writeFileSync(claudeSettings, JSON.stringify({ env: { KEEP_ME: "yes" }, custom: true }));

  for (const target of ["claude", "codex", "opencode", "pi"]) {
    await runInstaller(`--${target}`, "--non-interactive", "--base-url", baseUrl);
  }

  const claude = JSON.parse(fs.readFileSync(claudeSettings, "utf8"));
  assert.equal(claude.env.KEEP_ME, "yes");
  assert.equal(claude.env.ANTHROPIC_BASE_URL, baseUrl);
  assert.match(claude.env.ANTHROPIC_CUSTOM_HEADERS, /rk_test_native_windows/);
  const codexConfig = fs.readFileSync(path.join(temporaryHome, ".codex", "config.toml"), "utf8");
  assert.match(codexConfig, /model = "weave-auto"/);
  assert.match(codexConfig, /model_provider = "weave"/);
  assert.equal(JSON.parse(fs.readFileSync(path.join(temporaryHome, ".config", "opencode", "opencode.json"), "utf8")).model, "weave/auto");
  assert.equal(JSON.parse(fs.readFileSync(path.join(temporaryHome, ".pi", "agent", "models.json"), "utf8")).providers.weave.baseUrl, baseUrl);
});

test("native uninstall removes the managed Claude routing variables", async () => {
  await runInstaller("--uninstall", "--claude");
  const claude = JSON.parse(fs.readFileSync(path.join(temporaryHome, ".claude", "settings.json"), "utf8"));
  assert.equal(claude.env.KEEP_ME, "yes");
  assert.equal(claude.env.ANTHROPIC_BASE_URL, undefined);
  assert.equal(claude.custom, true);
});

test("native uninstall removes Codex, OpenCode, and pi router settings", async () => {
  await runInstaller("--uninstall", "--codex");
  await runInstaller("--uninstall", "--opencode");
  await runInstaller("--uninstall", "--pi");

  const codexConfig = fs.readFileSync(path.join(temporaryHome, ".codex", "config.toml"), "utf8");
  assert.doesNotMatch(codexConfig, /model_providers\.weave|model_provider = "weave"/);
  const openCodeConfig = JSON.parse(fs.readFileSync(path.join(temporaryHome, ".config", "opencode", "opencode.json"), "utf8"));
  assert.equal(openCodeConfig.provider.weave, undefined);
  const piModels = JSON.parse(fs.readFileSync(path.join(temporaryHome, ".pi", "agent", "models.json"), "utf8"));
  assert.equal(piModels.providers.weave, undefined);
});
