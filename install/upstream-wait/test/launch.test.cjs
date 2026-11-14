const assert = require("node:assert/strict");
const { mkdtempSync, writeFileSync, readFileSync, rmSync } = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { test } = require("node:test");
const TOML = require("smol-toml");
const { appServerOptions } = require("../launch.cjs");

test("profiles, overrides and cwd reach app-server without rewriting saved files", (t) => {
  const root = mkdtempSync(path.join(os.tmpdir(), "weave-wait-profile-"));
  t.after(() => rmSync(root, { recursive: true }));
  // Include an array of tables: each CLI override must remain valid TOML.
  const profile = { model: "fixture-model", model_provider: "weave", model_providers: { weave: { base_url: "https://router.example.invalid/v1", http_headers: { "X-Fixture": 'quote " and newline\n' } } }, hooks: [{ name: "fixture", args: ["a", "b"] }] };
  const profilePath = path.join(root, "fixture.config.toml");
  writeFileSync(profilePath, TOML.stringify(profile));
  const before = readFileSync(profilePath, "utf8");
  const options = appServerOptions(["--profile", "fixture", "-C", "subdir", "--config", 'model="cli-model"', "--disable", "memories"], { CODEX_HOME: root }, root);
  assert.equal(options.cwd, path.join(root, "subdir"));
  const overrides = options.profileConfig;
  assert.equal(overrides.model, "cli-model");
  assert.deepEqual(overrides.hooks, TOML.parse(before).hooks);
  assert.equal(overrides.model_providers.weave.http_headers['X-Fixture'], profile.model_providers.weave.http_headers['X-Fixture']);
  assert.deepEqual(options.serverArgs, ['--config', 'model="cli-model"', '--disable', 'memories']);
  assert.ok(!JSON.stringify(options.serverArgs).includes('quote'), 'profile credentials stay out of process arguments');
  assert.equal(readFileSync(profilePath, "utf8"), before);
  assert.deepEqual(options.serverArgs.slice(-2), ["--disable", "memories"]);
});
