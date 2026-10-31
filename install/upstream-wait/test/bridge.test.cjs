const assert = require("node:assert/strict");
const { once } = require("node:events");
const http = require("node:http");
const { test } = require("node:test");
const { WebSocket } = require("ws");
const { UpstreamWaitBridge, WAITING_MESSAGE, WAIT_CONTENT_TYPE, Frame, Method } = require("../bridge.cjs");

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

async function fixture(t, serve) {
  const router = http.createServer(serve);
  router.listen(0, "127.0.0.1");
  await once(router, "listening");
  t.after(() => { router.closeAllConnections(); router.close(); });
  const bridge = new UpstreamWaitBridge();
  await bridge.listen();
  t.after(() => bridge.close());
  const notifications = [];
  bridge.socket = { readyState: WebSocket.OPEN, send: (text) => notifications.push(JSON.parse(text)), terminate() {} };
  bridge.observeNotification({ method: Method.TURN_STARTED, params: { threadId: "foreground", turn: { id: "turn-1" } } });
  const url = bridge.providerURL(`http://127.0.0.1:${router.address().port}/v1`);
  return { bridge, url, notifications };
}

function sendFrame(response, type, fields = {}) { response.write(JSON.stringify({ type, ...fields }) + "\n"); }
function bodyFrame(response, body) { sendFrame(response, Frame.BODY, { body: Buffer.from(body).toString("base64") }); }
function startFrames(response) { response.writeHead(200, { "Content-Type": WAIT_CONTENT_TYPE }); }
const body = '{"model":"fixture","stream":true,"input":"authored test"}';
const headers = { "thread-id": "foreground", "Content-Type": "application/json", "X-Weave-Router-Key": "fixture-key" };

test("provider signal, not request arrival, drives a transient wait; native bytes are preserved", async (t) => {
  const arrived = deferred();
  const dispatch = deferred();
  const output = deferred();
  const { bridge, url, notifications } = await fixture(t, async (request, response) => {
    assert.equal(request.url, "/v1/responses");
    assert.equal(request.headers["x-weave-provider-wait"], "1");
    assert.equal(request.headers["x-weave-router-key"], "fixture-key");
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    assert.equal(Buffer.concat(chunks).toString(), body);
    arrived.resolve();
    await dispatch.promise;
    startFrames(response);
    sendFrame(response, Frame.WAITING, { waiting: true });
    await output.promise;
    sendFrame(response, Frame.WAITING, { waiting: false });
    sendFrame(response, Frame.RESPONSE, { status: 200, headers: { "Content-Type": ["text/event-stream"] } });
    bodyFrame(response, "data: exact provider bytes\n\n");
    sendFrame(response, Frame.DONE);
    response.end();
  });
  const received = fetch(url + "/responses", { method: "POST", headers, body });
  await arrived.promise;
  assert.deepEqual(notifications, []);
  dispatch.resolve();
  await waitFor(t, () => notifications.length === 1);
  const waiting = await (await fetch(bridge.endpoint + "/status")).json();
  assert.equal(waiting.waiting, true);
  assert.equal(notifications[0].params.run.statusMessage, WAITING_MESSAGE);
  output.resolve();
  const response = await received;
  assert.equal(response.headers.get("content-type"), "text/event-stream");
  assert.equal(await response.text(), "data: exact provider bytes\n\n");
  assert.deepEqual(notifications.map((event) => event.method), [Method.HOOK_STARTED, Method.HOOK_COMPLETED]);
  assert.equal(notifications[1].params.run.statusMessage, null);
  assert.deepEqual(notifications[0].params.run.entries, []);
  assert.deepEqual(notifications[1].params.run.entries, []);
  assert.equal(notifications[0].params.run.id, notifications[1].params.run.id);
  assert.equal((await (await fetch(bridge.endpoint + "/status")).json()).waiting, false);
});

async function waitFor(t, condition) {
  // A test deadline diagnoses hangs without changing the bridge's stream budgets.
  const { setTimeout } = require("node:timers/promises");
  const deadline = Date.now() + 5000;
  while (!condition()) {
    assert.ok(Date.now() < deadline, "Expected bridge transition did not arrive");
    await setTimeout(5);
  }
}

test("retries get separate waits and final HTTP errors retain status, headers and body", async (t) => {
  const { url, notifications } = await fixture(t, (request, response) => {
    startFrames(response);
    for (const waiting of [true, false, true, false]) sendFrame(response, Frame.WAITING, { waiting });
    sendFrame(response, Frame.RESPONSE, { status: 429, headers: { "Retry-After": ["7"], "Content-Type": ["application/json"] } });
    bodyFrame(response, '{"error":{"message":"fixture rate limit"}}');
    sendFrame(response, Frame.DONE);
    response.end();
  });
  const response = await fetch(url + "/responses", { method: "POST", headers, body });
  assert.equal(response.status, 429);
  assert.equal(response.headers.get("retry-after"), "7");
  assert.equal(await response.text(), '{"error":{"message":"fixture rate limit"}}');
  assert.deepEqual(notifications.map((event) => event.method), [Method.HOOK_STARTED, Method.HOOK_COMPLETED, Method.HOOK_STARTED, Method.HOOK_COMPLETED]);
});

test("older routers forward without invented waiting attribution", async (t) => {
  const { url, notifications } = await fixture(t, (request, response) => {
    response.writeHead(400, { "Content-Type": "application/json" });
    response.end('{"error":"fixture native rejection"}');
  });
  const response = await fetch(url + "/responses", { method: "POST", headers, body });
  assert.equal(response.status, 400);
  assert.equal(await response.text(), '{"error":"fixture native rejection"}');
  assert.deepEqual(notifications, []);
});

test("disconnect cancels upstream and clears the wait before any native output", async (t) => {
  const closed = deferred();
  const { bridge, url, notifications } = await fixture(t, (request, response) => {
    startFrames(response);
    sendFrame(response, Frame.WAITING, { waiting: true });
    response.once("close", closed.resolve);
  });
  const request = http.request(url + "/responses", { method: "POST", headers });
  request.on("error", () => {});
  request.end(body);
  await waitFor(t, () => notifications.length === 1);
  request.destroy();
  await closed.promise;
  await waitFor(t, () => bridge.requests.size === 0);
  assert.equal(notifications.at(-1).method, Method.HOOK_COMPLETED);
});

test("background completion and late old-turn signals cannot clear or restart another turn's wait", async (t) => {
  const { bridge, notifications } = await fixture(t, () => {});
  bridge.observeNotification({ method: Method.TURN_STARTED, params: { threadId: "background", turn: { id: "turn-2" } } });
  const foreground = { turn: bridge.turns.get("foreground") };
  const background = { turn: bridge.turns.get("background") };
  bridge.setWaiting("main-request", foreground, true);
  bridge.setWaiting("background-request", background, true);
  bridge.setWaiting("background-request", background, false);
  assert.ok(foreground.turn.run);
  assert.equal(notifications.at(-1).params.threadId, "background");
  bridge.observeNotification({ method: Method.TURN_COMPLETED, params: { threadId: "foreground", turn: { id: "turn-1" } } });
  const count = notifications.length;
  bridge.setWaiting("main-request", foreground, true);
  assert.equal(notifications.length, count);
});

test("loopback capability and browser-origin checks protect both status and app-server", async (t) => {
  const { bridge } = await fixture(t, () => {});
  assert.equal((await fetch(bridge.endpoint + "/status", { headers: { Origin: "https://example.invalid" } })).status, 403);
  assert.equal((await fetch(bridge.endpoint.replace(bridge.secret, "wrong") + "/status")).status, 403);
  const websocket = new WebSocket(bridge.endpoint.replace(/^http:/, "ws:") + "/rpc", { origin: "https://example.invalid" });
  const [error] = await once(websocket, "error");
  assert.match(error.message, /403/);
});

test("thread routing changes only the Weave endpoint in transient overrides", async (t) => {
  const { bridge } = await fixture(t, () => {});
  bridge.rpc = async () => ({ config: { model_provider: "weave", model_providers: { weave: { base_url: "https://router.example.invalid/v1", http_headers: { "x-api-key": "fixture" } } } } });
  const original = { id: 9, method: "thread/resume", params: { threadId: "saved", config: { "features.memories": false }, sandbox: "workspace-write" } };
  const routed = await bridge.routeThread(original);
  assert.equal(routed.params.sandbox, "workspace-write");
  assert.equal(routed.params.config["features.memories"], false);
  assert.match(routed.params.config.model_providers.weave.base_url, /^http:\/\/127\.0\.0\.1:/);
  assert.deepEqual(original.params.config, { "features.memories": false });
  assert.equal([...bridge.targets.values()].at(-1), "https://router.example.invalid/v1");
});

test("profile tables preserve literal keys and credentials while per-thread endpoints take precedence", async (t) => {
  const { bridge } = await fixture(t, () => {});
  bridge.profileConfig = { model_providers: { weave: { base_url: "https://profile.example.invalid/v1", http_headers: { "X-Fixture": "profile-credential" } } }, mcp_servers: { "server.with.dots": { command: "fixture" } } };
  bridge.rpc = async () => ({ config: { model_providers: { weave: { base_url: "https://base.example.invalid/v1", http_headers: { "X-Fixture": "credential" } } }, mcp_servers: { other: { command: "other-fixture" } } } });
  const routed = await bridge.routeThread({ method: "thread/start", params: { config: { "model_providers.weave.base_url": "https://override.example.invalid/v1" } } });
  assert.equal(routed.params.config.model_providers.weave.http_headers["X-Fixture"], "profile-credential");
  assert.equal(routed.params.config.mcp_servers["server.with.dots"].command, "fixture");
  assert.equal(routed.params.config.mcp_servers.other, undefined);
  assert.equal(routed.params.config["model_providers.weave.base_url"], undefined);
  assert.equal(routed.params.config.model_providers.weave.base_url.startsWith("http://127.0.0.1:"), true);
  assert.equal([...bridge.targets.values()].at(-1), "https://override.example.invalid/v1");
});
