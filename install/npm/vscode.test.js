const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { test } = require("node:test");
const { parse } = require("jsonc-parser");

const entrypoint = path.join(__dirname, "bin.js");
const byokSetting = "chat.agentHost.byokModels.enabled";

function fixture(t, routerUrl = "") {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "weave-vscode-"));
  t.after(() => fs.rmSync(home, { recursive: true, force: true }));
  const profile = path.join(home, "Profile With Spaces");
  const models = path.join(profile, "chatLanguageModels.json");
  const settings = path.join(profile, "settings.json");
  const run = (...args) => spawnSync(process.execPath, [entrypoint, "--copilot", "--vscode", ...args], {
    encoding: "utf8",
    env: {
      ...process.env,
      HOME: home,
      USERPROFILE: home,
      APPDATA: path.join(home, "AppData"),
      XDG_CONFIG_HOME: path.join(home, ".config"),
      COPILOT_HOME: path.join(home, ".copilot"),
      VSCODE_PORTABLE: "",
      WEAVE_ROUTER_KEY: "fixture-key-never-written",
      WEAVE_ROUTER_URL: routerUrl,
    },
  });
  return { home, profile, models, settings, run };
}

test("installs the hosted model in the default desktop profile without reading a Router key", (t) => {
  const { home, run } = fixture(t);
  const command = run();
  assert.equal(command.status, 0, command.stderr);
  assert.match(command.stdout, /Update API Key/);
  const configHome = {
    darwin: path.join(home, "Library", "Application Support"),
    win32: path.join(home, "AppData"),
  }[process.platform] ?? path.join(home, ".config");
  const profile = path.join(configHome, "Code", "User");
  const models = fs.readFileSync(path.join(profile, "chatLanguageModels.json"), "utf8");
  assert.deepEqual(parse(models), [{
    name: "Weave Router",
    vendor: "customendpoint",
    apiType: "messages",
    models: [{
      id: "auto",
      name: "Weave Router",
      url: "https://router.weaveos.com/v1/messages",
      toolCalling: true,
      maxInputTokens: 468000,
      maxOutputTokens: 32000,
      requestHeaders: { "X-App": "copilot" },
    }],
  }]);
  assert.doesNotMatch(models, /fixture-key-never-written|apiKey/);
  assert.equal(parse(fs.readFileSync(path.join(profile, "settings.json"), "utf8"))[byokSetting], true);
  assert.equal(fs.existsSync(path.join(home, ".copilot")), false);
});

test("merges JSONC and preserves an existing editor-managed secret and custom URL on rerun", (t) => {
  const { profile, models, settings, run } = fixture(t);
  fs.mkdirSync(profile);
  fs.writeFileSync(models, '[\n  // Existing provider\n  {"vendor":"ollama","name":"Local"},\n]\n');
  fs.writeFileSync(settings, '\uFEFF{\r\n  // Keep the user setting\r\n  "editor.tabSize": 4,\r\n}\r\n');
  const first = run("--profile-dir", profile, "--base-url", "https://example.test/router/v1/");
  assert.equal(first.status, 0, first.stderr);
  const modelText = fs.readFileSync(models, "utf8");
  const groups = parse(modelText);
  assert.deepEqual(groups[1], { vendor: "ollama", name: "Local" });
  assert.match(modelText, /\/\/ Existing provider/);
  assert.equal(groups[0].models[0].url, "https://example.test/router/v1/messages");
  const settingsText = fs.readFileSync(settings, "utf8");
  assert.match(settingsText, /^\uFEFF/);
  assert.match(settingsText, /\/\/ Keep the user setting\r\n/);
  assert.deepEqual(parse(settingsText.slice(1)), { "editor.tabSize": 4, [byokSetting]: true });

  groups[0].apiKey = "${input:chat.lm.secret.fixture}";
  const keyedModelText = JSON.stringify(groups);
  fs.writeFileSync(models, keyedModelText);
  const rerun = run("--profile-dir", profile);
  assert.equal(rerun.status, 0, rerun.stderr);
  assert.doesNotMatch(rerun.stdout, /Update API Key/);
  assert.equal(fs.readFileSync(models, "utf8"), keyedModelText);
  assert.equal(fs.readFileSync(settings, "utf8"), settingsText);

  const retarget = run("--profile-dir", profile, "--base-url", "https://other.example.test");
  assert.notEqual(retarget.status, 0);
  assert.match(retarget.stderr, /already exists/);
  assert.equal(fs.readFileSync(models, "utf8"), keyedModelText);
});

test("validates both files before editing either of them", (t) => {
  const { profile, models, settings, run } = fixture(t);
  fs.mkdirSync(profile);
  fs.writeFileSync(settings, '{ "editor.tabSize": }');
  const command = run("--profile-dir", profile);
  assert.notEqual(command.status, 0);
  assert.match(command.stderr, /Fix the JSON in .*settings\.json/);
  assert.equal(fs.existsSync(models), false);
  assert.equal(fs.readFileSync(settings, "utf8"), '{ "editor.tabSize": }');

  fs.writeFileSync(settings, "// Existing comments only");
  fs.writeFileSync(models, '{ "name": "invalid root shape" }');
  const badModels = run("--profile-dir", profile);
  assert.notEqual(badModels.status, 0);
  assert.match(badModels.stderr, /Fix the JSON in .*chatLanguageModels\.json/);
  assert.equal(fs.readFileSync(settings, "utf8"), "// Existing comments only");
});

test("uses the Router URL from the environment unless an explicit flag overrides it", (t) => {
  const { home, profile, models, run } = fixture(t, "https://env-router.example.test");
  const command = run("--profile-dir", profile);
  assert.equal(command.status, 0, command.stderr);
  assert.equal(parse(fs.readFileSync(models, "utf8"))[0].models[0].url, "https://env-router.example.test/v1/messages");

  const explicitProfile = path.join(home, "Explicit Profile");
  const explicit = run("--profile-dir", explicitProfile, "--base-url", "https://flag-router.example.test");
  assert.equal(explicit.status, 0, explicit.stderr);
  const explicitModels = path.join(explicitProfile, "chatLanguageModels.json");
  assert.equal(parse(fs.readFileSync(explicitModels, "utf8"))[0].models[0].url, "https://flag-router.example.test/v1/messages");
});

test("rejects insecure endpoints and accepts local Router setup", (t) => {
  const { profile, models, run } = fixture(t, "https://env-router.example.test");
  const rejected = run("--profile-dir", profile, "--base-url", "http://example.test");
  assert.notEqual(rejected.status, 0);
  assert.match(rejected.stderr, /HTTPS/);
  assert.equal(fs.existsSync(profile), false);

  const local = run("--profile-dir", profile, "--local");
  assert.equal(local.status, 0, local.stderr);
  assert.equal(parse(fs.readFileSync(models, "utf8"))[0].models[0].url, "http://localhost:8080/v1/messages");
});
