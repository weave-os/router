const { spawn } = require("node:child_process");
const { once } = require("node:events");
const { readFileSync } = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const TOML = require("smol-toml");
const { UpstreamWaitBridge, mergeConfig } = require("./bridge.cjs");

function appServerOptions(args, env = process.env, cwd = process.cwd()) {
  const configArgs = [];
  let explicitConfig = {};
  const recordOverride = (assignment) => {
    let parsed;
    try { parsed = TOML.parse(assignment); }
    catch {
      const equals = assignment.indexOf("=");
      if (equals < 0) throw new Error("Codex config overrides require key=value");
      parsed = TOML.parse(assignment.slice(0, equals + 1) + JSON.stringify(assignment.slice(equals + 1)));
    }
    explicitConfig = mergeConfig(explicitConfig, parsed);
  };
  let profile;
  for (let i = 0; i < args.length; i++) {
    const argument = args[i];
    if (argument === "--") break;
    if (["-c", "--config", "--enable", "--disable"].includes(argument)) {
      if (args[i + 1] === undefined) throw new Error(`${argument} requires a value`);
      configArgs.push(argument, args[++i]);
      if (["-c", "--config"].includes(argument)) recordOverride(args[i]);
      else explicitConfig = mergeConfig(explicitConfig, { features: { [args[i]]: argument === "--enable" } });
    } else if (/^--(config|enable|disable)=/.test(argument) || argument === "--strict-config") {
      configArgs.push(argument);
      if (argument.startsWith("--config=")) recordOverride(argument.slice("--config=".length));
      else if (argument.startsWith("--enable=") || argument.startsWith("--disable=")) {
        explicitConfig = mergeConfig(explicitConfig, { features: { [argument.slice(argument.indexOf("=") + 1)]: argument.startsWith("--enable=") } });
      }
    } else if (["-p", "--profile"].includes(argument)) profile = args[++i];
    else if (argument.startsWith("--profile=")) profile = argument.slice("--profile=".length);
    else if (["-C", "--cd"].includes(argument)) cwd = path.resolve(cwd, args[++i]);
    else if (argument.startsWith("--cd=")) cwd = path.resolve(cwd, argument.slice("--cd=".length));
    else if (argument === "--remote" || argument.startsWith("--remote=")) throw new Error("The Router launcher owns the local app-server connection");
  }
  let profileConfig = {};
  if (profile) {
    const profilePath = path.join(env.CODEX_HOME || path.join(os.homedir(), ".codex"), `${profile}.config.toml`);
    profileConfig = mergeConfig(TOML.parse(readFileSync(profilePath, "utf8")), explicitConfig);
  }
  return { serverArgs: configArgs, profileConfig, cwd };
}

async function main(client, args) {
  const options = client === "codex" ? appServerOptions(args) : {};
  const bridge = new UpstreamWaitBridge(options);
  const endpoint = await bridge.listen();
  const environment = { ...process.env };
  let command;
  if (client === "codex") {
    environment.WEAVE_PROVIDER_WAIT_TOKEN = bridge.secret;
    command = ["--remote", new URL(endpoint).origin.replace(/^http:/, "ws:"), "--remote-auth-token-env", "WEAVE_PROVIDER_WAIT_TOKEN", ...args];
  } else {
    environment.WEAVE_PROVIDER_WAIT_ENDPOINT = endpoint;
    const plugin = ["--plugin-dir", path.join(__dirname, "claude-plugin")];
    const terminator = args.indexOf("--");
    command = terminator < 0 ? [...args, ...plugin] : [...args.slice(0, terminator), ...plugin, ...args.slice(terminator)];
  }
  let child;
  const forward = (signal) => child?.kill(signal);
  const interrupt = () => forward("SIGINT");
  const terminate = () => forward("SIGTERM");
  process.on("SIGINT", interrupt);
  process.on("SIGTERM", terminate);
  try {
    child = spawn(client, command, { env: environment, stdio: "inherit" });
    const [code] = await once(child, "exit");
    return code ?? 1;
  } finally {
    process.off("SIGINT", interrupt);
    process.off("SIGTERM", terminate);
    await bridge.close();
  }
}

module.exports = { main, appServerOptions };
