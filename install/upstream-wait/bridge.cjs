const { spawn } = require("node:child_process");
const { randomUUID } = require("node:crypto");
const { once } = require("node:events");
const http = require("node:http");
const https = require("node:https");
const { createInterface } = require("node:readline");
const { WebSocketServer, WebSocket } = require("ws");

const WAITING_MESSAGE = "Waiting on upstream provider..";
const WAIT_HEADER = "x-weave-provider-wait";
const WAIT_CONTENT_TYPE = "application/vnd.weave.provider-wait+ndjson";
const Frame = Object.freeze({ WAITING: "waiting", RESPONSE: "response", BODY: "body", DONE: "done" });
const Method = Object.freeze({
  TURN_STARTED: "turn/started", TURN_COMPLETED: "turn/completed",
  HOOK_STARTED: "hook/started", HOOK_COMPLETED: "hook/completed",
});
const threadMethods = new Set(["thread/start", "thread/resume", "thread/fork"]);
const hopHeaders = new Set(["connection", "transfer-encoding", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "upgrade"]);

function mergeConfig(base, overlay) {
  const merged = Object.assign(Object.create(null), base);
  for (const [key, value] of Object.entries(overlay)) {
    merged[key] = value && typeof value === "object" && !Array.isArray(value) && !(value instanceof Date)
      ? mergeConfig(merged[key], value) : value;
  }
  return merged;
}

function endToEndHeaders(headers) {
  const excluded = new Set([...hopHeaders, ...String(headers.connection || "").toLowerCase().split(",").map((name) => name.trim())]);
  return Object.fromEntries(Object.entries(headers).filter(([name]) => !excluded.has(name.toLowerCase())));
}

class UpstreamWaitBridge {
  constructor({ codex = "codex", serverArgs = [], profileConfig = {}, cwd = process.cwd(), env = process.env } = {}) {
    this.codex = codex;
    this.serverArgs = serverArgs;
    this.profileConfig = profileConfig;
    this.configReads = new Set();
    this.cwd = cwd;
    this.env = env;
    this.secret = randomUUID();
    this.targets = new Map();
    this.turns = new Map();
    this.requests = new Map();
    this.pending = new Map();
    this.socket = null;
    this.child = null;
    this.server = http.createServer((request, response) => {
      this.handleHTTP(request, response).catch((error) => {
        if (response.headersSent) response.destroy(error);
        else { response.writeHead(502); response.end("Provider bridge request failed"); }
      });
    });
    this.websockets = new WebSocketServer({ noServer: true, maxPayload: 0 });
    this.server.on("upgrade", (request, socket, head) => {
      if (request.headers.origin || request.url !== "/" || request.headers.authorization !== `Bearer ${this.secret}` || this.socket) {
        socket.end("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n");
        return;
      }
      this.websockets.handleUpgrade(request, socket, head, (websocket) => this.connect(websocket));
    });
  }

  async listen() {
    this.server.listen(0, "127.0.0.1");
    await once(this.server, "listening");
    this.endpoint = `http://127.0.0.1:${this.server.address().port}/${this.secret}`;
    return this.endpoint;
  }

  providerURL(baseURL) {
    const target = new URL(baseURL);
    if (!["http:", "https:"].includes(target.protocol) || target.username || target.password || target.search || target.hash) {
      throw new Error("Provider base URL must be an HTTP(S) URL without credentials, query, or fragment");
    }
    const normalized = target.href.replace(/\/$/, "");
    for (const [id, original] of this.targets) {
      const local = `${this.endpoint}/provider/${id}`;
      if (original === normalized || local === normalized) return local;
    }
    if (target.origin === new URL(this.endpoint).origin) throw new Error("Provider bridge cannot proxy to itself");
    const id = randomUUID();
    this.targets.set(id, normalized);
    return `${this.endpoint}/provider/${id}`;
  }

  notify(method, params) {
    if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify({ method, params }));
  }

  startWait(turn) {
    if (turn.run) return;
    turn.run = {
      id: `weave-upstream-${randomUUID()}`, eventName: "userPromptSubmit", handlerType: "command",
      executionMode: "sync", scope: "turn", source: "plugin", sourcePath: __filename,
      displayOrder: 0, startedAt: Date.now(), status: "running", statusMessage: WAITING_MESSAGE, entries: [],
    };
    this.notify(Method.HOOK_STARTED, { threadId: turn.threadId, turnId: turn.turnId, run: turn.run });
  }

  endWait(turn) {
    if (!turn.run) return;
    const completedAt = Date.now();
    this.notify(Method.HOOK_COMPLETED, {
      threadId: turn.threadId, turnId: turn.turnId,
      run: { ...turn.run, status: "completed", statusMessage: null, completedAt, durationMs: completedAt - turn.run.startedAt },
    });
    turn.run = null;
  }

  setWaiting(id, request, waiting) {
    request.waiting = waiting;
    const turn = request.turn;
    if (!turn || this.turns.get(turn.threadId) !== turn) return;
    if (waiting) turn.waits.add(id);
    else turn.waits.delete(id);
    if (turn.waits.size) this.startWait(turn);
    else this.endWait(turn);
  }

  observeNotification(message) {
    const { method, params } = message;
    if (method === Method.TURN_STARTED) {
      const previous = this.turns.get(params.threadId);
      if (previous) this.endWait(previous);
      this.turns.set(params.threadId, { threadId: params.threadId, turnId: params.turn.id, waits: new Set(), run: null });
    } else if (method === Method.TURN_COMPLETED) {
      const turn = this.turns.get(params.threadId);
      if (turn?.turnId === params.turn.id) {
        this.endWait(turn);
        this.turns.delete(params.threadId);
      }
    }
  }

  async handleHTTP(request, response) {
    if (request.headers.origin || !request.url.startsWith(`/${this.secret}/`)) {
      response.writeHead(403); response.end(); return;
    }
    const path = request.url.slice(this.secret.length + 2);
    if (request.method === "GET" && path === "status") {
      response.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" });
      response.end(JSON.stringify({ waiting: [...this.requests.values()].some((entry) => entry.waiting) }));
      return;
    }
    if (request.method === "POST" && path === "configure") {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const { baseURL } = JSON.parse(Buffer.concat(chunks));
      const local = this.providerURL(baseURL);
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ baseURL: local }));
      return;
    }
    const match = /^provider\/([^/]+)(\/.*)?$/.exec(path);
    const baseURL = match && this.targets.get(match[1]);
    if (!baseURL) { response.writeHead(404); response.end(); return; }
    await this.proxy(request, response, `${baseURL}${match[2] || ""}`);
  }

  async proxy(request, response, target) {
    const id = randomUUID();
    const abort = new AbortController();
    const entry = { waiting: false, turn: this.turns.get(request.headers["thread-id"]), abort };
    this.requests.set(id, entry);
    const headers = endToEndHeaders(request.headers);
    delete headers.host;
    delete headers[WAIT_HEADER];
    const inference = request.method === "POST" && /\/(messages|responses|chat\/completions)$/.test(new URL(target).pathname);
    if (inference) headers[WAIT_HEADER] = "1";
    const upstream = (target.startsWith("https:") ? https : http).request(target, {
      method: request.method, headers, signal: abort.signal,
    });
    const disconnected = () => abort.abort();
    response.once("close", disconnected);
    request.once("aborted", disconnected);
    const received = once(upstream, "response");
    request.pipe(upstream);
    try {
      const [incoming] = await received;
      if (incoming.headers["content-type"]?.split(";")[0] === WAIT_CONTENT_TYPE) {
        let ended = false;
        for await (const line of createInterface({ input: incoming, crlfDelay: Infinity })) {
          const frame = JSON.parse(line);
          if (ended) throw new Error("Provider wait frame after completion");
          switch (frame.type) {
            case Frame.WAITING:
              this.setWaiting(id, entry, frame.waiting === true);
              break;
            case Frame.RESPONSE:
              if (response.headersSent) throw new Error("Duplicate provider response headers");
              response.writeHead(frame.status, endToEndHeaders(frame.headers || {}));
              break;
            case Frame.BODY:
              if (!response.headersSent) throw new Error("Provider response body before headers");
              if (!response.write(Buffer.from(frame.body || "", "base64"))) await once(response, "drain", { signal: abort.signal });
              break;
            case Frame.DONE:
              ended = true;
              break;
            default: throw new Error("Unknown provider wait frame");
          }
        }
        if (!ended || !response.headersSent) throw new Error("Incomplete provider wait transport");
      } else {
        // Older routers return their original protocol and produce no wait status.
        response.writeHead(incoming.statusCode, endToEndHeaders(incoming.headers));
        for await (const chunk of incoming) {
          if (!response.write(chunk)) await once(response, "drain", { signal: abort.signal });
        }
      }
      response.end();
    } finally {
      this.setWaiting(id, entry, false);
      this.requests.delete(id);
      response.off("close", disconnected);
      request.off("aborted", disconnected);
      upstream.destroy();
    }
  }

  rpc(method, params) {
    const id = `weave-bridge-${randomUUID()}`;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.child.stdin.write(JSON.stringify({ id, method, params }) + "\n");
    });
  }

  async routeThread(message) {
    if (!threadMethods.has(message.method)) return message;
    const params = message.params || {};
    const loaded = await this.rpc("config/read", { cwd: params.cwd || this.cwd, includeLayers: false });
    const requestConfig = params.config || {};
    const config = mergeConfig(loaded.config, this.profileConfig);
    const overrides = mergeConfig(this.profileConfig, requestConfig);
    const providerOverrides = mergeConfig(this.profileConfig.model_providers || {}, requestConfig.model_providers || {});
    if (Object.keys(providerOverrides).length) overrides.model_providers = providerOverrides;
    const definition = requestConfig.model_providers?.weave || this.profileConfig.model_providers?.weave || config.model_providers?.weave;
    const baseURL = requestConfig["model_providers.weave.base_url"] || definition?.base_url;
    if (!baseURL) return message;
    const local = this.providerURL(baseURL);
    overrides.model_providers = {
      ...overrides.model_providers,
      weave: { ...overrides.model_providers?.weave, base_url: local },
    };
    delete overrides["model_providers.weave.base_url"];
    return { ...message, params: { ...params, config: overrides } };
  }

  connect(socket) {
    this.socket = socket;
    const child = this.child = spawn(this.codex, ["app-server", "--listen", "stdio://", ...this.serverArgs], {
      cwd: this.cwd, env: this.env, stdio: ["pipe", "pipe", "inherit"],
    });
    const lines = createInterface({ input: child.stdout });
    child.stdin.on("error", () => socket.close(1011, "Codex app-server input closed"));
    lines.on("line", (line) => {
      try {
        const message = JSON.parse(line);
        const pending = this.pending.get(message.id);
        if (pending) {
          this.pending.delete(message.id);
          if (message.error) pending.reject(new Error(message.error.message));
          else pending.resolve(message.result);
          return;
        }
        if (this.configReads.delete(message.id) && message.result?.config) {
          message.result.config = mergeConfig(message.result.config, this.profileConfig);
        }
        this.observeNotification(message);
        if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
      } catch (error) { socket.close(1011, "App-server protocol failure"); }
    });
    let queue = Promise.resolve();
    socket.on("message", (bytes) => {
      queue = queue.then(async () => {
        const message = await this.routeThread(JSON.parse(bytes));
        if (message.method === "config/read") this.configReads.add(message.id);
        if (!child.stdin.write(JSON.stringify(message) + "\n")) await once(child.stdin, "drain");
      }).catch(() => socket.close(1011, "App-server request failed"));
    });
    child.on("error", () => socket.close(1011, "Could not start Codex app-server"));
    child.on("exit", () => {
      for (const pending of this.pending.values()) pending.reject(new Error("Codex app-server exited"));
      this.pending.clear();
      socket.close();
    });
    socket.on("close", () => {
      for (const entry of this.requests.values()) entry.abort.abort();
      this.turns.clear();
      child.stdin.end();
      if (child.exitCode === null && child.signalCode === null) child.kill();
      this.socket = null;
    });
  }

  async close() {
    this.socket?.terminate();
    for (const entry of this.requests.values()) entry.abort.abort();
    if (this.child && this.child.exitCode === null && this.child.signalCode === null) {
      this.child.stdin.end();
      this.child.kill();
      const forceExit = setTimeout(() => this.child.kill("SIGKILL"), 5000);
      try { await once(this.child, "exit"); }
      finally { clearTimeout(forceExit); }
    }
    this.websockets.close();
    const closed = once(this.server, "close");
    this.server.close();
    this.server.closeAllConnections();
    await closed;
  }
}

module.exports = { UpstreamWaitBridge, WAITING_MESSAGE, WAIT_HEADER, WAIT_CONTENT_TYPE, Frame, Method, mergeConfig };
