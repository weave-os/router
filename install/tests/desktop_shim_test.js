// Behavior tests for install/desktop-shim.js: Claude Desktop's Messages API
// calls through the CONNECT proxy reach the router with the router key, every
// other request reaches Anthropic untouched, and other hosts are tunneled.

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

const CODE_TAB_UA = "claude-cli/2.1.309 (external, claude-desktop, agent-sdk/0.3.309)";
const COWORK_UA = "claude-cli/2.1.309 (external, local-agent, agent-sdk/0.3.309)";

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

async function openRequest(proxyPort, ca, { method = "GET", reqPath, headers = {}, body }) {
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
  return { res, tlsSocket };
}

function withTimeout(promise, ms, what) {
  let timer;
  const timeout = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${what} did not happen within ${ms}ms`)), ms); });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

async function viaProxy(proxyPort, ca, options) {
  const { res, tlsSocket } = await openRequest(proxyPort, ca, options);
  const chunks = [];
  for await (const chunk of res) chunks.push({ at: Date.now(), text: chunk.toString() });
  tlsSocket.destroy();
  return { res, chunks, text: chunks.map((c) => c.text).join("") };
}

test("desktop shim", async (t) => {
  makeCerts();
  const ca = fs.readFileSync(path.join(work, "ca.pem"));

  let heldStreamClosed;
  const heldStreamClosedPromise = new Promise((resolve) => { heldStreamClosed = resolve; });
  const router = recordingServer((req, res) => {
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.write("event: message_start\n\n");
    if (req.url.includes("hold")) {
      res.on("close", heldStreamClosed);
      return;
    }
    if (req.url.includes("cut")) {
      setTimeout(() => res.socket.destroy(), 50);
      return;
    }
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

  await t.test("Code tab Messages calls go to the router with the router key and stream", async () => {
    const { res, chunks, text } = await viaProxy(proxyPort, ca, {
      method: "POST",
      reqPath: "/v1/messages?beta=true",
      headers: { "user-agent": CODE_TAB_UA, authorization: "Bearer sk-ant-oat-subscription", "content-type": "application/json", "x-weave-router-key": "rk_spoofed" },
      body: '{"model":"claude-sonnet-5","stream":true}',
    });
    assert.equal(res.statusCode, 200);
    assert.equal(text, "event: message_start\n\nevent: message_stop\n\n");
    assert.ok(chunks.length >= 2 && chunks.at(-1).at - chunks[0].at >= 200, "first SSE event was buffered until the stream ended");
    const routerRequest = router.seen.at(-1);
    assert.equal(routerRequest.url, "/prefix/v1/messages?beta=true");
    assert.equal(routerRequest.headers["x-weave-router-key"], "rk_desktop_test");
    assert.equal(routerRequest.headers.authorization, "Bearer sk-ant-oat-subscription");
    assert.equal(routerRequest.headers.host, `127.0.0.1:${routerPort}`);
    assert.equal(routerRequest.body, '{"model":"claude-sonnet-5","stream":true}');
    assert.equal(anthropic.seen.length, 0);
  });

  await t.test("Cowork token counting goes to the router too", async () => {
    await viaProxy(proxyPort, ca, { method: "POST", reqPath: "/v1/messages/count_tokens?beta=true", headers: { "user-agent": COWORK_UA }, body: "{}" });
    assert.equal(router.seen.at(-1).url, "/prefix/v1/messages/count_tokens?beta=true");
  });

  await t.test("Messages calls from other apps go to Anthropic without the router key", async () => {
    for (const userAgent of ["claude-cli/2.1.309 (external, cli)", "claude-cli/2.1.309 (external, claude-desktop-3p)", "anthropic-sdk-python"]) {
      const before = router.seen.length;
      const { text } = await viaProxy(proxyPort, ca, { method: "POST", reqPath: "/v1/messages", headers: { "user-agent": userAgent }, body: "{}" });
      assert.equal(text, '{"ok":true}');
      assert.equal(router.seen.length, before, `${userAgent} reached the router`);
      assert.equal(anthropic.seen.at(-1).headers["x-weave-router-key"], undefined);
    }
  });

  await t.test("everything else on api.anthropic.com goes to Anthropic without the router key", async () => {
    const { text } = await viaProxy(proxyPort, ca, { reqPath: "/api/oauth/profile", headers: { "user-agent": CODE_TAB_UA, authorization: "Bearer sk-ant-oat-subscription" } });
    assert.equal(text, '{"ok":true}');
    const anthropicRequest = anthropic.seen.at(-1);
    assert.equal(anthropicRequest.url, "/api/oauth/profile");
    assert.equal(anthropicRequest.headers["x-weave-router-key"], undefined);
    assert.equal(anthropicRequest.headers.authorization, "Bearer sk-ant-oat-subscription");
  });

  await t.test("other hosts are tunneled untouched", async () => {
    const socket = await connectTunnel(proxyPort, `127.0.0.1:${echoPort}`);
    socket.write("ping");
    const [data] = await once(socket, "data");
    assert.equal(data.toString(), "ping");
    socket.destroy();
  });

  await t.test("stopping a streamed answer cancels the router request", async () => {
    const { res, tlsSocket } = await openRequest(proxyPort, ca, { method: "POST", reqPath: "/v1/messages?hold", headers: { "user-agent": CODE_TAB_UA }, body: "{}" });
    await once(res, "data");
    tlsSocket.destroy();
    await withTimeout(heldStreamClosedPromise, 2000, "router-side close after Stop");
  });

  await t.test("a router stream cut mid-answer fails the client response", async () => {
    const { res, tlsSocket } = await openRequest(proxyPort, ca, { method: "POST", reqPath: "/v1/messages?cut", headers: { "user-agent": CODE_TAB_UA }, body: "{}" });
    const finished = new Promise((resolve) => {
      res.on("close", resolve);
      res.on("error", () => {});
      res.resume();
    });
    await withTimeout(finished, 2000, "client response close after upstream cut");
    assert.equal(res.complete, false);
    tlsSocket.destroy();
  });

  await t.test("the PAC file sends only api.anthropic.com to the proxy", async () => {
    const res = await new Promise((resolve) => http.get({ host: "127.0.0.1", port: proxyPort, path: "/proxy.pac" }, resolve));
    let pac = "";
    for await (const chunk of res) pac += chunk;
    const FindProxyForURL = new Function(`${pac}; return FindProxyForURL;`)();
    assert.equal(FindProxyForURL("https://api.anthropic.com/v1/messages", "api.anthropic.com"), `PROXY 127.0.0.1:${proxyPort}`);
    assert.equal(FindProxyForURL("https://claude.ai/", "claude.ai"), "DIRECT");
  });

  await t.test("health endpoint answers", async () => {
    const res = await new Promise((resolve) => http.get({ host: "127.0.0.1", port: proxyPort, path: "/health" }, resolve));
    assert.equal(res.statusCode, 200);
    res.resume();
  });
});
