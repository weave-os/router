#!/usr/bin/env node
// Local HTTPS proxy for Claude Desktop's Code tab. Desktop forces
// ANTHROPIC_BASE_URL to Anthropic, but honors HTTPS_PROXY, so this proxy
// terminates TLS for api.anthropic.com only and sends Messages API calls to the
// Weave Router with the router key added. Every other request, and every other
// host, goes through untouched, so Desktop's own sign-in keeps working.

"use strict";

const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");
const tls = require("node:tls");

const INTERCEPT_HOST = "api.anthropic.com";
const ROUTER_KEY_HEADER = "x-weave-router-key";
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
  const raw = JSON.parse(fs.readFileSync(file, "utf8"));
  for (const field of ["router_base_url", "cert_file", "key_file"]) {
    if (typeof raw[field] !== "string" || raw[field] === "") {
      throw new Error(`${file}: missing ${field}`);
    }
  }
  return {
    host: raw.listen_host || "127.0.0.1",
    port: Number(raw.listen_port ?? 47821),
    router: new URL(raw.router_base_url),
    routerKey: typeof raw.router_key === "string" ? raw.router_key : "",
    anthropic: new URL(raw.anthropic_origin || `https://${INTERCEPT_HOST}`),
    cert: fs.readFileSync(raw.cert_file),
    key: fs.readFileSync(raw.key_file),
  };
}

function routesToRouter(path) {
  return path === "/v1/messages" || path.startsWith("/v1/messages/") || path.startsWith("/v1/messages?");
}

function upstreamPath(base, path) {
  const prefix = base.pathname.replace(/\/+$/, "");
  return prefix + path;
}

function forward(cfg, req, res) {
  const toRouter = routesToRouter(req.url);
  const target = toRouter ? cfg.router : cfg.anthropic;
  const headers = {};
  for (const [name, value] of Object.entries(req.headers)) {
    if (!HOP_BY_HOP.has(name) && name !== ROUTER_KEY_HEADER) headers[name] = value;
  }
  headers.host = target.host;
  if (toRouter && cfg.routerKey) headers[ROUTER_KEY_HEADER] = cfg.routerKey;

  const client = target.protocol === "http:" ? http : https;
  const upstream = client.request(
    {
      protocol: target.protocol,
      hostname: target.hostname,
      port: target.port || (target.protocol === "http:" ? 80 : 443),
      method: req.method,
      path: upstreamPath(target, req.url),
      headers,
    },
    (upstreamRes) => {
      const out = {};
      for (const [name, value] of Object.entries(upstreamRes.headers)) {
        if (!HOP_BY_HOP.has(name)) out[name] = value;
      }
      res.writeHead(upstreamRes.statusCode || 502, out);
      res.flushHeaders();
      upstreamRes.pipe(res);
    },
  );
  upstream.setNoDelay(true);
  upstream.on("error", (err) => {
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
  req.on("aborted", () => upstream.destroy());
  req.pipe(upstream);
}

function start(cfg) {
  const intercepted = http.createServer((req, res) => forward(cfg, req, res));
  const secureContext = tls.createSecureContext({ cert: cfg.cert, key: cfg.key });

  const proxy = http.createServer((req, res) => {
    if (req.method === "GET" && req.url === "/health") {
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok\n");
      return;
    }
    res.writeHead(405, { "content-type": "text/plain" });
    res.end("CONNECT only\n");
  });

  proxy.on("connect", (req, socket, head) => {
    const [host, port] = splitHostPort(req.url);
    socket.on("error", () => socket.destroy());
    if (host === INTERCEPT_HOST) {
      socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head && head.length) socket.unshift(head);
      const tlsSocket = new tls.TLSSocket(socket, { isServer: true, secureContext, ALPNProtocols: ["http/1.1"] });
      tlsSocket.on("error", () => tlsSocket.destroy());
      intercepted.emit("connection", tlsSocket);
      return;
    }
    const upstream = net.connect(port, host, () => {
      socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head && head.length) upstream.write(head);
      upstream.pipe(socket);
      socket.pipe(upstream);
    });
    upstream.on("error", () => {
      socket.end("HTTP/1.1 502 Bad Gateway\r\n\r\n");
    });
  });

  proxy.listen(cfg.port, cfg.host, () => {
    const address = proxy.address();
    console.log(`Weave Router desktop proxy listening on ${address.address}:${address.port}, router ${cfg.router.origin}`);
  });
  return proxy;
}

function splitHostPort(authority) {
  const idx = authority.lastIndexOf(":");
  if (idx === -1) return [authority, 443];
  return [authority.slice(0, idx).replace(/^\[|\]$/g, ""), Number(authority.slice(idx + 1)) || 443];
}

module.exports = { loadConfig, routesToRouter, start };

if (require.main === module) {
  const file = process.argv[2];
  if (!file) {
    console.error("usage: desktop-shim.js <config.json>");
    process.exit(2);
  }
  start(loadConfig(file));
}
