// Behavior tests for install/desktop-shim.js: Messages API calls through the
// CONNECT proxy reach the router with the router key, everything else reaches
// Anthropic untouched, and other hosts are tunneled byte-for-byte.

"use strict";

const assert = require("node:assert/strict");
const { execFileSync } = require("node:child_process");
const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");
const { once } = require("node:events");
const test = require("node:test");
const tls = require("node:tls");

const shim = require("../desktop-shim.js");

const work = fs.mkdtempSync(path.join(os.tmpdir(), "desktop-shim-test-"));
process.on("exit", () => fs.rmSync(work, { recursive: true, force: true }));

function openssl(...args) {
  execFileSync("openssl", args, { cwd: work, stdio: "ignore" });
}

function makeCerts() {
  fs.writeFileSync(path.join(work, "ca.cnf"), [
    "[req]", "distinguished_name = dn", "prompt = no", "x509_extensions = v3_ca",
    "[dn]", "CN = test proxy", "[v3_ca]",
    "basicConstraints = critical,CA:TRUE,pathlen:0",
    "keyUsage = critical,keyCertSign,cRLSign",
    "nameConstraints = critical,permitted;DNS:api.anthropic.com",
    "subjectKeyIdentifier = hash", "",
  ].join("\n"));
  fs.writeFileSync(path.join(work, "leaf.ext"), [
    "basicConstraints = critical,CA:FALSE",
    "extendedKeyUsage = serverAuth",
    "subjectAltName = DNS:api.anthropic.com",
    "authorityKeyIdentifier = keyid", "",
  ].join("\n"));
  openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-config", "ca.cnf", "-keyout", "ca-key.pem", "-out", "ca.pem");
  openssl("req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=api.anthropic.com", "-keyout", "leaf-key.pem", "-out", "leaf.csr");
  openssl("x509", "-req", "-days", "2", "-in", "leaf.csr", "-CA", "ca.pem", "-CAkey", "ca-key.pem", "-set_serial", "1", "-extfile", "leaf.ext", "-out", "leaf.pem");
}

function recordingServer(handler) {
  const seen = [];
  const server = http.createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => { body += chunk; });
    req.on("end", () => {
      seen.push({ method: req.method, url: req.url, headers: req.headers, body });
      handler(req, res);
    });
  });
  return { server, seen };
}

async function listen(server) {
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return server.address().port;
}

async function connectTunnel(proxyPort, authority) {
  const req = http.request({ host: "127.0.0.1", port: proxyPort, method: "CONNECT", path: authority });
  req.end();
  const [res, socket] = await once(req, "connect");
  assert.equal(res.statusCode, 200);
  return socket;
}

async function viaProxy(proxyPort, ca, { method = "GET", reqPath, headers = {}, body }) {
  const socket = await connectTunnel(proxyPort, "api.anthropic.com:443");
  const tlsSocket = tls.connect({ socket, servername: "api.anthropic.com", ca });
  await once(tlsSocket, "secureConnect");
  const req = https.request({
    host: "api.anthropic.com", method, path: reqPath, headers,
    createConnection: () => tlsSocket,
  });
  if (body) req.write(body);
  req.end();
  const [res] = await once(req, "response");
  const chunks = [];
  for await (const chunk of res) chunks.push({ at: Date.now(), text: chunk.toString() });
  tlsSocket.destroy();
  return { res, chunks, text: chunks.map((c) => c.text).join("") };
}

test("desktop shim", async (t) => {
  makeCerts();
  const ca = fs.readFileSync(path.join(work, "ca.pem"));

  const router = recordingServer((req, res) => {
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.write("event: message_start\n\n");
    setTimeout(() => res.end("event: message_stop\n\n"), 300);
  });
  const anthropic = recordingServer((req, res) => {
    res.writeHead(200, { "content-type": "application/json" });
    res.end('{"ok":true}');
  });
  const routerPort = await listen(router.server);
  const anthropicPort = await listen(anthropic.server);
  const echo = net.createServer((socket) => socket.pipe(socket));
  const echoPort = await listen(echo);

  const configFile = path.join(work, "config.json");
  fs.writeFileSync(configFile, JSON.stringify({
    listen_port: 0,
    router_base_url: `http://127.0.0.1:${routerPort}/prefix`,
    router_key: "rk_desktop_test",
    anthropic_origin: `http://127.0.0.1:${anthropicPort}`,
    cert_file: path.join(work, "leaf.pem"),
    key_file: path.join(work, "leaf-key.pem"),
  }));
  const proxy = shim.start(shim.loadConfig(configFile));
  await once(proxy, "listening");
  const proxyPort = proxy.address().port;
  t.after(() => {
    proxy.close();
    router.server.closeAllConnections();
    router.server.close();
    anthropic.server.closeAllConnections();
    anthropic.server.close();
    echo.close();
  });

  await t.test("Messages API calls go to the router with the router key and stream", async () => {
    const { res, chunks, text } = await viaProxy(proxyPort, ca, {
      method: "POST",
      reqPath: "/v1/messages?beta=true",
      headers: { authorization: "Bearer sk-ant-oat-subscription", "content-type": "application/json", "x-weave-router-key": "rk_spoofed" },
      body: '{"model":"claude-sonnet-5","stream":true}',
    });
    assert.equal(res.statusCode, 200);
    assert.equal(text, "event: message_start\n\nevent: message_stop\n\n");
    assert.ok(chunks.length >= 2 && chunks.at(-1).at - chunks[0].at >= 200, "first SSE event was buffered until the stream ended");
    const got = router.seen.at(-1);
    assert.equal(got.url, "/prefix/v1/messages?beta=true");
    assert.equal(got.headers["x-weave-router-key"], "rk_desktop_test");
    assert.equal(got.headers.authorization, "Bearer sk-ant-oat-subscription");
    assert.equal(got.headers.host, `127.0.0.1:${routerPort}`);
    assert.equal(got.body, '{"model":"claude-sonnet-5","stream":true}');
    assert.equal(anthropic.seen.length, 0);
  });

  await t.test("token counting goes to the router too", async () => {
    await viaProxy(proxyPort, ca, { method: "POST", reqPath: "/v1/messages/count_tokens?beta=true", body: "{}" });
    assert.equal(router.seen.at(-1).url, "/prefix/v1/messages/count_tokens?beta=true");
  });

  await t.test("everything else on api.anthropic.com goes to Anthropic without the router key", async () => {
    const { text } = await viaProxy(proxyPort, ca, { reqPath: "/api/oauth/profile", headers: { authorization: "Bearer sk-ant-oat-subscription" } });
    assert.equal(text, '{"ok":true}');
    const got = anthropic.seen.at(-1);
    assert.equal(got.url, "/api/oauth/profile");
    assert.equal(got.headers["x-weave-router-key"], undefined);
    assert.equal(got.headers.authorization, "Bearer sk-ant-oat-subscription");
  });

  await t.test("other hosts are tunneled untouched", async () => {
    const socket = await connectTunnel(proxyPort, `127.0.0.1:${echoPort}`);
    socket.write("ping");
    const [data] = await once(socket, "data");
    assert.equal(data.toString(), "ping");
    socket.destroy();
  });

  await t.test("health endpoint answers", async () => {
    const res = await new Promise((resolve) => http.get({ host: "127.0.0.1", port: proxyPort, path: "/health" }, resolve));
    assert.equal(res.statusCode, 200);
    res.resume();
  });
});
