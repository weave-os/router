const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { applyEdits, modify, parse } = require("jsonc-parser");

const providerName = "Weave Router";
const vendor = "customendpoint";
const modelId = "auto";
const byokSetting = "chat.agentHost.byokModels.enabled";
const hostedBaseUrl = "https://router.weaveos.com";

function setupVSCode(args) {
  const usage = "Usage: npx @weave-os/router --copilot --vscode [--base-url URL | --local] [--profile-dir PATH]";
  if (!args.includes("--copilot")) throw new Error(usage);
  if (args.includes("--help") || args.includes("-h")) {
    console.log(usage);
    return;
  }

  let profileDir;
  let baseUrl;
  for (let index = 0; index < args.length; index++) {
    const option = args[index];
    switch (option) {
      case "--copilot":
      case "--vscode":
        break;
      case "--profile-dir":
      case "--base-url": {
        const value = args[++index];
        if (!value || value.startsWith("--")) throw new Error(`${option} requires a value.`);
        if (option === "--profile-dir") profileDir = path.resolve(value);
        else baseUrl = value;
        break;
      }
      case "--local":
        baseUrl = "http://localhost:8080";
        break;
      case "--uninstall":
        throw new Error("Remove Weave Router from Chat: Manage Language Models so VS Code can also remove the saved key.");
      default:
        throw new Error(`Unsupported VS Code setup argument. ${usage}`);
    }
  }

  const endpoint = messagesUrl(baseUrl ?? (process.env.WEAVE_ROUTER_URL || hostedBaseUrl));
  profileDir ??= defaultProfileDir();
  const modelsFile = path.join(profileDir, "chatLanguageModels.json");
  const settingsFile = path.join(profileDir, "settings.json");
  const modelGroups = readConfig(modelsFile, []);
  const settings = readConfig(settingsFile, {});
  const existing = modelGroups.value.filter((group) => group?.name === providerName && group.vendor === vendor);
  if (existing.length > 1) throw new Error("Multiple Weave Router entries. Keep one in Chat: Manage Language Models and retry.");

  const group = existing[0];
  if (group) {
    const model = Array.isArray(group.models) && group.models.find((entry) => entry?.id === modelId);
    if (typeof model?.url !== "string" || (baseUrl !== undefined && model.url !== endpoint)) {
      // Retargeting a configured group would reuse its stored credential at the new URL.
      throw new Error("A Weave Router group already exists with a different configuration. In Chat: Manage Language Models, remove that group and retry to configure a new endpoint.");
    }
  }

  const modelsEdit = group ? modelGroups.text : editConfig(modelGroups, [0], {
    name: providerName,
    vendor,
    apiType: "messages",
    models: [{
      id: modelId,
      name: providerName,
      url: endpoint,
      toolCalling: true,
      maxInputTokens: 468000,
      maxOutputTokens: 32000,
      requestHeaders: { "X-App": "copilot" },
    }],
  }, { isArrayInsertion: true });
  const settingsEdit = settings.value[byokSetting] === true
    ? settings.text
    : editConfig(settings, [byokSetting], true, { getInsertionIndex: () => 0 });
  if (parse(settingsEdit.replace(/^\uFEFF/, ""))?.[byokSetting] !== true) {
    throw new Error(`Check for duplicate ${byokSetting} entries in ${settingsFile} and retry.`);
  }

  writeConfig(modelGroups, modelsEdit);
  writeConfig(settings, settingsEdit);
  console.log(`Weave Router is configured for the VS Code profile at ${profileDir}.`);
  if (typeof group?.apiKey !== "string" || !group.apiKey.startsWith("${input:")) {
    console.log("In VS Code, open Chat: Manage Language Models, then choose Weave Router → Update API Key.");
    console.log("Enter your Router key there. Get a hosted key at https://app.weaveos.com/start or ask your Router admin.");
  }
  console.log("Select Weave Router in a new Copilot chat. Reload the VS Code window if the model does not appear.");
}

function defaultProfileDir() {
  if (process.env.VSCODE_PORTABLE) return path.join(process.env.VSCODE_PORTABLE, "user-data", "User");
  if (process.platform === "win32") {
    if (!process.env.APPDATA) throw new Error("APPDATA is unavailable. Pass --profile-dir with your VS Code profile directory.");
    return path.join(process.env.APPDATA, "Code", "User");
  }
  const configDir = process.platform === "darwin"
    ? path.join(os.homedir(), "Library", "Application Support")
    : process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(configDir, "Code", "User");
}

function messagesUrl(baseUrl) {
  let url;
  try {
    url = new URL(baseUrl);
  } catch {
    throw new Error("Pass a valid Router URL with --base-url.");
  }
  const loopback = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
  if ((url.protocol !== "https:" && !(url.protocol === "http:" && loopback))
      || url.username || url.password || url.search || url.hash) {
    throw new Error("Use an HTTPS Router URL or a loopback HTTP URL, without credentials, query parameters, or a fragment.");
  }
  const endpointPath = url.pathname.replace(/\/+$/, "");
  url.pathname = endpointPath.endsWith("/v1/messages") ? endpointPath
    : endpointPath.endsWith("/v1") ? `${endpointPath}/messages`
    : `${endpointPath}/v1/messages`;
  return url.href;
}

function readConfig(file, emptyValue) {
  let mode;
  let text;
  try {
    const stat = fs.lstatSync(file);
    if (!stat.isFile()) throw new Error(`${file} must be a regular file.`);
    mode = stat.mode & 0o777;
    text = fs.readFileSync(file, "utf8");
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }

  const bom = text?.startsWith("\uFEFF") ? "\uFEFF" : "";
  const document = text?.slice(bom.length) ?? "";
  const errors = [];
  const parsed = parse(document, errors, { allowTrailingComma: true, allowEmptyContent: true });
  const value = parsed === undefined ? emptyValue : parsed;
  if (errors.length || !value || typeof value !== "object" || Array.isArray(value) !== Array.isArray(emptyValue)) {
    throw new Error(`Fix the JSON in ${file} and retry.`);
  }
  return { file, mode, text, bom, document, value, isEmpty: parsed === undefined, emptyValue };
}

function editConfig(config, jsonPath, value, options) {
  const eol = config.document.includes("\r\n") ? "\r\n" : "\n";
  const document = config.isEmpty ? `${config.document}${config.document ? eol : ""}${JSON.stringify(config.emptyValue)}${eol}` : config.document;
  const edits = modify(document, jsonPath, value, {
    ...options,
    formattingOptions: { insertSpaces: true, tabSize: 2, eol },
  });
  return config.bom + applyEdits(document, edits);
}

function writeConfig(config, text) {
  if (text === config.text) return;
  const directory = path.dirname(config.file);
  fs.mkdirSync(directory, { recursive: true });
  const tempDirectory = fs.mkdtempSync(path.join(directory, ".weave-vscode-"));
  try {
    const temporaryFile = path.join(tempDirectory, "config.json");
    fs.writeFileSync(temporaryFile, text, { mode: config.mode ?? 0o600 });
    if (readConfig(config.file, config.emptyValue).text !== config.text) {
      throw new Error(`${config.file} changed during setup. Run the command again.`);
    }
    fs.renameSync(temporaryFile, config.file);
  } finally {
    fs.rmSync(tempDirectory, { recursive: true, force: true });
  }
}

module.exports = { setupVSCode };
