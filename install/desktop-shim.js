#!/usr/bin/env node
// Local HTTPS proxy for Claude Desktop's Code tab. Desktop forces
// ANTHROPIC_BASE_URL to Anthropic, but honors HTTPS_PROXY, so this proxy
// terminates TLS for api.anthropic.com only and sends Messages API calls to the
// Weave Router with the router key added. Every other request, and every other
// host, goes through untouched, so Desktop's own sign-in keeps working.

"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");
const tls = require("node:tls");

const INTERCEPT_HOST = "api.anthropic.com";
const ROUTER_KEY_HEADER = "x-weave-router-key";
const PROXY_USER = "weave";
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
    proxyToken: typeof parsed.proxy_token === "string" ? parsed.proxy_token : "",
    anthropic: new URL(parsed.anthropic_origin || `https://${INTERCEPT_HOST}`),
    cert: fs.readFileSync(parsed.cert_file),
    key: fs.readFileSync(parsed.key_file),
  };
}

function routesToRouter(path) {
  return path === "/v1/messages" || path.startsWith("/v1/messages/") || path.startsWith("/v1/messages?");
}

// Loopback is shared by every OS user on the machine, so the proxy only adds
// the router key for clients that know the per-install token in HTTPS_PROXY.
function proxyAuthorized(cfg, header) {
  if (!cfg.proxyToken) return true;
  const expected = Buffer.from(`Basic ${Buffer.from(`${PROXY_USER}:${cfg.proxyToken}`).toString("base64")}`);
  const presented = Buffer.from(header || "");
  return presented.length === expected.length && crypto.timingSafeEqual(presented, expected);
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
    if (!proxyAuthorized(cfg, req.headers["proxy-authorization"])) {
      socket.end(`HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm="weave-router"\r\nContent-Length: 0\r\n\r\n`);
      return;
    }
    const target = new URL(`http://${req.url}`);
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
  });

  proxy.listen(cfg.port, cfg.host, () => {
    const address = proxy.address();
    console.log(`Weave Router desktop proxy listening on ${address.address}:${address.port}, router ${cfg.router.origin}`);
  });
  return proxy;
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
