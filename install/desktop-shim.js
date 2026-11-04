#!/usr/bin/env node
// Local HTTPS proxy for Claude Desktop's Code tab and Cowork. Desktop forces
// ANTHROPIC_BASE_URL to Anthropic, but gives both the OS proxy (here, the PAC
// file served at /proxy.pac) and the OS certificate store. This proxy terminates
// TLS for api.anthropic.com only and sends Desktop's Messages API calls to the
// Weave Router with the router key added. Everything else, including other apps'
// api.anthropic.com traffic and every other host, passes through untouched.

"use strict";

const { execFile } = require("node:child_process");
const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");
const tls = require("node:tls");

const INTERCEPT_HOST = "api.anthropic.com";
const ROUTER_KEY_HEADER = "x-weave-router-key";
// Desktop's model picker can't list Weave Auto, so Desktop's picks are routed
// instead of honoring the CLI's passthrough header from ~/.claude/settings.json.
const MODEL_PASSTHROUGH_HEADER = "x-weave-model-passthrough";
// Claude Code's user agent names its entrypoint: "claude-desktop" for the Code
// tab, "local-agent" for Cowork.
const DESKTOP_USER_AGENT = /[(,]\s*(claude-desktop|local-agent)(?![\w-])/;
const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "proxy-connection",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
]);

function loadConfig(file) {
  const parsed = JSON.parse(fs.readFileSync(file, "utf8"));
  for (const field of ["router_base_url", "cert_file", "key_file"]) {
    if (typeof parsed[field] !== "string" || parsed[field] === "") {
      throw new Error(`${file}: missing ${field}`);
    }
  }
  return {
    host: parsed.listen_host || "127.0.0.1",
    port: Number(parsed.listen_port ?? 47821),
    router: new URL(parsed.router_base_url),
    routerKey: typeof parsed.router_key === "string" ? parsed.router_key : "",
    anthropic: new URL(parsed.anthropic_origin || `https://${INTERCEPT_HOST}`),
    cert: fs.readFileSync(parsed.cert_file),
    key: fs.readFileSync(parsed.key_file),
  };
}

function routesToRouter(req) {
  const path = req.url;
  const isMessages = path === "/v1/messages" || path.startsWith("/v1/messages/") || path.startsWith("/v1/messages?");
  return isMessages && DESKTOP_USER_AGENT.test(req.headers["user-agent"] || "");
}

// Loopback is shared by every OS account and the proxy adds the installing
// user's router key, so it only serves that user's processes. peerUid finds
// the OS user owning the client end of a loopback connection.
function peerUid(socket, callback) {
  // Match the whole 4-tuple: another user can reuse the owner's source port
  // from a different loopback address such as 127.0.0.2.
  if (process.platform === "linux") {
    // /proc/net/tcp writes IPv4 endpoints as little-endian hex, e.g. 0100007F:B8CD.
    const procEndpoint = (address, port) => {
      const octets = address.replace(/^::ffff:/, "").split(".").reverse();
      const ip = octets.map((octet) => Number(octet).toString(16).padStart(2, "0")).join("");
      return `${ip}:${port.toString(16).padStart(4, "0")}`.toUpperCase();
    };
    const client = procEndpoint(socket.remoteAddress, socket.remotePort);
    const proxy = procEndpoint(socket.localAddress, socket.localPort);
    for (const row of fs.readFileSync("/proc/net/tcp", "utf8").split("\n").slice(1)) {
      const [, local, remote, , , , , uid] = row.trim().split(/\s+/);
      if (local === client && remote === proxy) return callback(Number(uid));
    }
    return callback(null);
  }
  // lsof without root lists only the caller's own processes, so another
  // user's client is simply not found.
  const connection = `n${socket.remoteAddress}:${socket.remotePort}->${socket.localAddress}:${socket.localPort}`;
  execFile("lsof", ["-nP", "-a", `-iTCP:${socket.remotePort}`, "-sTCP:ESTABLISHED", "-Fun"], (_err, out = "") => {
    let uid = null;
    for (const line of out.split("\n")) {
      if (line.startsWith("u")) uid = Number(line.slice(1));
      if (line === connection) return callback(uid);
    }
    callback(null);
  });
}

function pacFile(port) {
  return `function FindProxyForURL(url, host) {
  if (host === "${INTERCEPT_HOST}") return "PROXY 127.0.0.1:${port}";
  return "DIRECT";
}
`;
}

function forward(cfg, req, res) {
  const toRouter = routesToRouter(req);
  const target = toRouter ? cfg.router : cfg.anthropic;
  const headers = {};
  for (const [name, value] of Object.entries(req.headers)) {
    if (!HOP_BY_HOP.has(name) && name !== ROUTER_KEY_HEADER) headers[name] = value;
  }
  if (toRouter) delete headers[MODEL_PASSTHROUGH_HEADER];
  headers.host = target.host;
  if (toRouter && cfg.routerKey) headers[ROUTER_KEY_HEADER] = cfg.routerKey;

  const client = target.protocol === "http:" ? http : https;
  const upstream = client.request(
    {
      protocol: target.protocol,
      hostname: target.hostname,
      port: target.port || (target.protocol === "http:" ? 80 : 443),
      method: req.method,
      path: target.pathname.replace(/\/+$/, "") + req.url,
      headers,
    },
    (upstreamRes) => {
      const out = {};
      for (const [name, value] of Object.entries(upstreamRes.headers)) {
        if (!HOP_BY_HOP.has(name)) out[name] = value;
      }
      res.writeHead(upstreamRes.statusCode, out);
      res.flushHeaders();
      // A body cut off mid-stream must reach Desktop as a broken response, not
      // a connection that silently stays open.
      upstreamRes.on("close", () => {
        if (!upstreamRes.complete) res.destroy();
      });
      upstreamRes.pipe(res);
    },
  );
  upstream.setNoDelay(true);
  upstream.on("error", (err) => {
    if (res.destroyed) return;
    console.error(`${req.method} ${req.url} -> ${target.host}: ${err.message}`);
    if (!res.headersSent) {
      res.writeHead(502, { "content-type": "application/json" });
      res.end(JSON.stringify({
        type: "error",
        error: { type: "api_error", message: `Weave Router desktop proxy could not reach ${target.host}: ${err.message}` },
      }));
    } else {
      res.destroy(err);
    }
  });
  // Desktop's Stop closes the response; stop the upstream generation with it.
  res.on("close", () => {
    if (!res.writableFinished) upstream.destroy();
  });
  req.pipe(upstream);
}

function start(cfg) {
  const intercepted = http.createServer((req, res) => forward(cfg, req, res));
  const secureContext = tls.createSecureContext({ cert: cfg.cert, key: cfg.key });

  const proxy = http.createServer((req, res) => {
    if (req.method === "GET" && req.url === "/proxy.pac") {
      res.writeHead(200, { "content-type": "application/x-ns-proxy-autoconfig" });
      res.end(pacFile(proxy.address().port));
      return;
    }
    if (req.method === "GET" && req.url === "/health") {
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok\n");
      return;
    }
    res.writeHead(405, { "content-type": "text/plain" });
    res.end("CONNECT only\n");
  });

  proxy.on("connect", (req, socket, head) => {
    socket.on("error", () => socket.destroy());
    peerUid(socket, (uid) => {
      if (uid === process.getuid()) tunnel(req, socket, head);
      else socket.end("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n");
    });
  });

  function tunnel(req, socket, head) {
    let target;
    try {
      target = new URL(`http://${req.url}`);
    } catch {
      socket.end("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n");
      return;
    }
    const host = target.hostname.replace(/^\[|\]$/g, "");
    if (host === INTERCEPT_HOST) {
      socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head.length) socket.unshift(head);
      const tlsSocket = new tls.TLSSocket(socket, { isServer: true, secureContext, ALPNProtocols: ["http/1.1"] });
      tlsSocket.on("error", () => tlsSocket.destroy());
      intercepted.emit("connection", tlsSocket);
      return;
    }
    const upstream = net.connect(Number(target.port) || 443, host, () => {
      socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head.length) upstream.write(head);
      upstream.pipe(socket);
      socket.pipe(upstream);
    });
    upstream.on("error", () => {
      socket.end("HTTP/1.1 502 Bad Gateway\r\n\r\n");
    });
  }

  proxy.listen(cfg.port, cfg.host, () => {
    const address = proxy.address();
    console.log(`Weave Router desktop proxy listening on ${address.address}:${address.port}, router ${cfg.router.origin}`);
  });
  return proxy;
}

module.exports = { loadConfig, peerUid, routesToRouter, start };

if (require.main === module) {
  const file = process.argv[2];
  if (!file) {
    console.error("usage: desktop-shim.js <config.json>");
    process.exit(2);
  }
  start(loadConfig(file));
}
