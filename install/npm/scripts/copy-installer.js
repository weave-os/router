#!/usr/bin/env node
// Run by `npm pack` / `npm publish` (prepack hook). Copies the canonical
// install scripts from ../install/*.sh into the npm package root so the
// published tarball is self-contained. Keeps a single source of truth for
// the shell installer.

const { copyFileSync, cpSync, chmodSync, existsSync, mkdirSync, readdirSync, lstatSync, realpathSync } = require("node:fs");
const path = require("node:path");

const root = path.resolve(__dirname, "..");
const installDir = path.resolve(root, "..");
const repoRoot = path.resolve(installDir, "..");

const files = ["install.sh", "uninstall.sh", "cc-statusline.sh", "codex-status.sh", "registry.sh", "directives.tsv"];
for (const f of files) {
  const src = path.join(installDir, f);
  const dst = path.join(root, f);
  copyFileSync(src, dst);
  chmodSync(dst, 0o755);
  console.log(`Copied ${f}.`);
}

const registry = path.join(installDir, "directives.tsv");
const registryText = require("node:fs").readFileSync(registry, "utf8");

// commands dir relative to its own location, so colocating it alongside the
// script makes the bundle self-contained for `npx @weave-os/router`.
const commandsSrc = path.join(installDir, "commands");
const commandsDst = path.join(root, "commands");
const commandsSrcReal = realpathSync(commandsSrc);
mkdirSync(commandsDst, { recursive: true });
for (const f of readdirSync(commandsSrc)) {
  if (!f.endsWith(".md")) continue;
  const src = path.join(commandsSrc, f);
  const stat = lstatSync(src);
  if (stat.isSymbolicLink()) {
    throw new Error(`Refusing to package symlinked command file: ${src}`);
  }
  const srcReal = realpathSync(src);
  if (!srcReal.startsWith(commandsSrcReal + path.sep)) {
    throw new Error(`Refusing to package command outside commands dir: ${src}`);
  }
  copyFileSync(srcReal, path.join(commandsDst, f));
  console.log(`Copied commands/${f}.`);
}

// Codex discovers local skills under $CODEX_HOME/skills.
const registryNames = new Set();
for (const line of registryText.split(/\r?\n/)) {
  if (!line || line.startsWith("#")) continue;
  const fields = line.split("|");
  registryNames.add(fields[0]);
  for (const alias of (fields[1] || "").split(",")) if (alias) registryNames.add(alias);
}
for (const file of readdirSync(commandsSrc)) {
  if (file.endsWith(".md") && !registryNames.has(file.slice(0, -3))) {
    throw new Error(`Command ${file} is not declared in directives.tsv`);
  }
}

// registry prevents a newly supported directive from being omitted at publish time.
// Keyed on the skill adapter, not capability: Codex ships local-config toggles
// ($router-off/$router-on/$router-status/$disable-routing) as skills too.
for (const line of registryText.split(/\r?\n/)) {
  if (!line || line.startsWith("#")) continue;
  const fields = line.split("|");
  if (fields[4] !== "yes") continue;
  if (!(fields[8] || "").split(",").includes("skill")) continue;
  // Canonical name plus every alias: Codex discovers skills by directory name,
  // so an advertised $fm needs its own installed skill.
  const names = [fields[0], ...(fields[1] || "").split(",").filter(Boolean)];
  for (const name of names) {
    const src = path.join(installDir, "codex-skills", name, "SKILL.md");
    const dst = path.join(root, "codex-skills", name, "SKILL.md");
    // An alias may exist for the Claude command surface without a Codex skill
    // behind it (e.g. `models`), so a missing alias asset is not an error — but
    // a missing canonical one means the registry declared a skill we don't ship.
    if (!existsSync(src)) {
      if (name === fields[0]) throw new Error(`Codex skill ${name} is declared in directives.tsv but missing`);
      continue;
    }
    if (lstatSync(src).isSymbolicLink()) throw new Error(`Invalid Codex skill: ${src}`);
    mkdirSync(path.dirname(dst), { recursive: true });
    copyFileSync(src, dst);
    console.log(`Copied codex-skills/${name}/SKILL.md.`);
    // Prompt skills emit their directive through a script; toggles shell out to
    // the installer's own verbs and ship no script.
    const emitSrc = path.join(installDir, "codex-skills", name, "scripts", "emit.sh");
    if (!existsSync(emitSrc)) continue;
    const emitDst = path.join(root, "codex-skills", name, "scripts", "emit.sh");
    if (lstatSync(emitSrc).isSymbolicLink()) throw new Error(`Invalid Codex skill script: ${emitSrc}`);
    mkdirSync(path.dirname(emitDst), { recursive: true });
    copyFileSync(emitSrc, emitDst);
    chmodSync(emitDst, 0o755);
    console.log(`Copied codex-skills/${name}/scripts/emit.sh.`);
  }
}

// installer and the pi-router extension: pi loads it via the "pi.extensions"
// field in package.json, and install.sh adds `npm:@weave-os/router` to pi's
// settings. Source of truth lives at install/pi-router/src.
const piSrc = path.join(installDir, "pi-router", "src");
const piDst = path.join(root, "pi-router", "src");
mkdirSync(path.dirname(piDst), { recursive: true });
cpSync(piSrc, piDst, { recursive: true });
// pi discovers these through package.json's "pi.skills" once the package is
// installed — no installer involvement.
cpSync(path.join(installDir, "pi-router", "skills"), path.join(root, "pi-router", "skills"), { recursive: true });
// package.json marks the sources as ESM (type:module); README is docs.
for (const f of ["package.json", "README.md"]) {
  copyFileSync(path.join(installDir, "pi-router", f), path.join(root, "pi-router", f));
}
console.log("Copied pi-router/ (extension + skills).");

// Bundle the opencode Codex-subscription plugin the same way. install.sh
// (--codex/--opencode) drops opencode-weave/src/index.ts into the user's
// opencode plugins dir and registers it via opencode.json's "plugin" array.
// Source of truth lives at install/opencode-weave/src.
const ocSrc = path.join(installDir, "opencode-weave", "src");
const ocDst = path.join(root, "opencode-weave", "src");
mkdirSync(path.dirname(ocDst), { recursive: true });
cpSync(ocSrc, ocDst, { recursive: true });
for (const f of ["package.json", "README.md"]) {
  copyFileSync(path.join(installDir, "opencode-weave", f), path.join(root, "opencode-weave", f));
}
console.log("Copied opencode-weave/ (plugin).");

// LICENSE lives at the repo root and applies to the whole project. npm
// surfaces it on the package page when bundled alongside package.json.
copyFileSync(path.join(repoRoot, "LICENSE"), path.join(root, "LICENSE"));
console.log("Copied LICENSE.");
