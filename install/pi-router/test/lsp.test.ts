import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import test from "node:test";
import { LspBrokerClient, startLspBroker } from "../src/lsp-broker.js";
import { LspClient, planDocumentSync, type LspTransport, type TransportClose } from "../src/lsp-client.js";
import {
	createLineReader,
	formatDiagnostics,
	formatHover,
	formatLocations,
	formatSymbols,
	normalizeLocations,
} from "../src/lsp-format.js";
import {
	createFrameParser,
	encodeFrame,
	fromLspPosition,
	pathToUri,
	toLspPosition,
	uriToPath,
	type JsonRpcMessage,
} from "../src/lsp-protocol.js";
import {
	findWorkspaceRoot,
	LSP_SERVERS,
	LspServerPool,
	missingServerText,
	resolveBinary,
	specForFile,
} from "../src/lsp-servers.js";
import {
	buildLspOffer,
	detectWorkspaceServers,
	installServer,
	loadDismissedLanguages,
	saveDismissedLanguages,
} from "../src/lsp-install.js";
import { assertOperationParams, normalizeToolPath, registerLsp, runLspOperation } from "../src/lsp.js";

const CLIENT_OPTIONS = { requestTimeoutMs: 1000, warmupTimeoutMs: 1000 };
const FAST_OPTIONS = { requestTimeoutMs: 20, warmupTimeoutMs: 20 };
const GOPLS = LSP_SERVERS[0];
const GOPLS_BINARY = { command: "/usr/bin/gopls", args: ["serve"] };

// ---------- harnesses ----------

interface FakeTransport {
	transport: LspTransport;
	sent: JsonRpcMessage[];
	receive(message: JsonRpcMessage): void;
	close(info: TransportClose): void;
	onRequest(method: string, handler: (message: JsonRpcMessage) => void): void;
	killed(): boolean;
}

/** Drives LspClient without a process: every frame it writes lands in `sent`. */
function fakeTransport(options: { autoHandshake?: boolean } = {}): FakeTransport {
	const sent: JsonRpcMessage[] = [];
	const handlers = new Map<string, (message: JsonRpcMessage) => void>();
	let messageHandler: (message: JsonRpcMessage) => void = () => undefined;
	let closeHandler: (info: TransportClose) => void = () => undefined;
	let killed = false;

	const parser = createFrameParser((message) => {
		sent.push(message);
		if (options.autoHandshake && (message.method === "initialize" || message.method === "shutdown")) {
			queueMicrotask(() => messageHandler({ jsonrpc: "2.0", id: message.id, result: { capabilities: {} } }));
			return;
		}
		const handler = message.method ? handlers.get(message.method) : undefined;
		if (handler) queueMicrotask(() => handler(message));
	});

	return {
		transport: {
			send: (data) => parser.push(data),
			onMessage: (handler) => {
				messageHandler = handler;
			},
			onClose: (handler) => {
				closeHandler = handler;
			},
			kill: () => {
				killed = true;
			},
			killNow: () => {
				killed = true;
			},
		},
		sent,
		receive: (message) => messageHandler(message),
		close: (info) => closeHandler(info),
		onRequest: (method, handler) => handlers.set(method, handler),
		killed: () => killed,
	};
}

function tempDir(): string {
	return fs.mkdtempSync(path.join(os.tmpdir(), "weave-lsp-test-"));
}

function goProject(): { dir: string; file: string } {
	const dir = tempDir();
	fs.writeFileSync(path.join(dir, "go.mod"), "module example.com/x\n");
	const file = path.join(dir, "main.go");
	fs.writeFileSync(file, "package main\n\nfunc Main() {}\n");
	return { dir, file };
}

function poolThatMustNotSpawn(): LspServerPool {
	return new LspServerPool(
		{ maxServers: 1, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: () => {
				throw new Error("the pool must not be touched for this input");
			},
		},
	);
}

// ---------- lsp-protocol ----------

test("the frame parser reassembles a message fed one byte at a time", () => {
	const received: JsonRpcMessage[] = [];
	const parser = createFrameParser((message) => received.push(message));
	const frame = encodeFrame({ id: 7, method: "textDocument/hover" });
	for (const byte of frame) parser.push(Buffer.from([byte]));
	assert.deepEqual(received, [{ id: 7, method: "textDocument/hover" }]);
});

test("the frame parser splits several frames out of one chunk", () => {
	const received: JsonRpcMessage[] = [];
	const parser = createFrameParser((message) => received.push(message));
	parser.push(Buffer.concat([encodeFrame({ id: 1 }), encodeFrame({ id: 2 }), encodeFrame({ id: 3 })]));
	assert.deepEqual(
		received.map((message) => message.id),
		[1, 2, 3],
	);
});

test("Content-Length counts UTF-8 bytes, not characters", () => {
	const frame = encodeFrame({ text: "héllo — ünïcode ✓" });
	const declared = Number(/Content-Length: (\d+)/.exec(frame.toString("ascii", 0, 60))?.[1]);
	const bodyBytes = frame.length - frame.indexOf("\r\n\r\n") - 4;
	assert.equal(declared, bodyBytes);
	assert.notEqual(declared, JSON.stringify({ text: "héllo — ünïcode ✓" }).length);

	const received: JsonRpcMessage[] = [];
	createFrameParser((message) => received.push(message)).push(frame);
	assert.deepEqual(received, [{ text: "héllo — ünïcode ✓" } as unknown as JsonRpcMessage]);
});

test("the frame parser tolerates extra headers and a malformed body without losing the stream", () => {
	const received: JsonRpcMessage[] = [];
	const parser = createFrameParser((message) => received.push(message));
	const body = Buffer.from("not json", "utf8");
	parser.push(
		Buffer.concat([
			Buffer.from(`Content-Length: ${body.length}\r\nContent-Type: application/vscode-jsonrpc\r\n\r\n`, "ascii"),
			body,
			encodeFrame({ id: 99 }),
		]),
	);
	assert.deepEqual(received, [{ id: 99 }]);
});

test("the frame parser reports an overflowing peer instead of buffering forever", () => {
	const errors: Error[] = [];
	const received: JsonRpcMessage[] = [];
	const parser = createFrameParser(
		(message) => received.push(message),
		(error) => errors.push(error),
	);
	// A header promising more than the cap, followed by a body that never ends.
	parser.push(Buffer.from("Content-Length: 999999999\r\n\r\n", "ascii"));
	parser.push(Buffer.alloc(17 * 1024 * 1024));
	assert.equal(errors.length, 1);
	assert.match(errors[0].message, /exceeded/);

	// Once tripped it stays quiet so the owner can tear the peer down.
	parser.push(encodeFrame({ id: 1 }));
	assert.deepEqual(received, []);
});

test("file paths survive a URI round trip with spaces and non-ASCII", () => {
	for (const original of ["/tmp/plain.go", "/tmp/with space/mod ule.go", "/tmp/ünïcode/日本語.ts"]) {
		assert.equal(uriToPath(pathToUri(original)), original);
	}
	assert.match(pathToUri("/tmp/with space/a.go"), /%20/);
});

test("positions convert between 1-based tool arguments and 0-based LSP", () => {
	assert.deepEqual(toLspPosition(41, 8), { line: 40, character: 7 });
	assert.deepEqual(fromLspPosition({ line: 40, character: 7 }), { line: 41, column: 8 });
	// Line 0 is not addressable in the 1-based model, and must not go negative.
	assert.deepEqual(toLspPosition(0, 0), { line: 0, character: 0 });
	assert.deepEqual(fromLspPosition(undefined), { line: 1, column: 1 });
});

// ---------- lsp-servers ----------

test("specForFile maps extensions to servers and leaves unknown ones unmatched", () => {
	assert.equal(specForFile("/a/main.go")?.id, "gopls");
	assert.equal(specForFile("/a/App.tsx")?.id, "typescript");
	assert.equal(specForFile("/a/mod.PY")?.id, "pyright");
	assert.equal(specForFile("/a/lib.rs")?.id, "rust-analyzer");
	assert.equal(specForFile("/a/notes.txt"), undefined);
	assert.equal(specForFile("/a/Makefile"), undefined);
});

test("findWorkspaceRoot picks the nearest marker, not the outermost", () => {
	const root = tempDir();
	const inner = path.join(root, "services", "api");
	fs.mkdirSync(inner, { recursive: true });
	fs.mkdirSync(path.join(root, ".git"));
	fs.writeFileSync(path.join(inner, "go.mod"), "module api\n");

	assert.equal(findWorkspaceRoot(path.join(inner, "main.go"), GOPLS.rootMarkers, root), inner);
	// A file above the nested module still belongs to the outer tree.
	assert.equal(
		findWorkspaceRoot(path.join(root, "tool.go"), ["go.mod", ".git"], root),
		root,
	);
});

test("findWorkspaceRoot falls back to the working directory when nothing marks a root", () => {
	const root = tempDir();
	assert.equal(findWorkspaceRoot(path.join(root, "stray.go"), ["go.mod"], "/fallback/cwd"), "/fallback/cwd");
});

test("resolveBinary takes the first candidate on PATH", () => {
	const pyright = LSP_SERVERS.find((spec) => spec.id === "pyright");
	assert.ok(pyright);
	assert.deepEqual(resolveBinary(pyright, (command) => (command === "basedpyright-langserver" ? "/opt/bin/based" : undefined)), {
		command: "/opt/bin/based",
		args: ["--stdio"],
	});
	assert.deepEqual(resolveBinary(pyright, () => "/usr/bin/found"), { command: "/usr/bin/found", args: ["--stdio"] });
	assert.equal(resolveBinary(pyright, () => undefined), undefined);
});

test("missingServerText names the binary and how to install it", () => {
	const text = missingServerText(GOPLS);
	assert.match(text, /gopls is not on PATH/);
	assert.match(text, /go install golang\.org\/x\/tools\/gopls@latest/);
});

test("the pool reuses one server per root and language", async () => {
	let spawns = 0;
	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				spawns += 1;
				return new LspClient(root, fakeTransport({ autoHandshake: true }).transport, options);
			},
		},
	);
	const first = await pool.acquire(GOPLS, GOPLS_BINARY, "/repo");
	const second = await pool.acquire(GOPLS, GOPLS_BINARY, "/repo");
	assert.equal(first, second);
	assert.equal(spawns, 1);

	await pool.acquire(GOPLS, GOPLS_BINARY, "/other-repo");
	assert.equal(spawns, 2);
	assert.equal(pool.size, 2);
});

test("the pool evicts the least recently used server once over its cap", async () => {
	let spawns = 0;
	let clock = 0;
	const pool = new LspServerPool(
		{ maxServers: 2, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			now: () => (clock += 1000),
			createClient: (_spec, _binary, root, options) => {
				spawns += 1;
				return new LspClient(root, fakeTransport({ autoHandshake: true }).transport, options);
			},
		},
	);
	await pool.acquire(GOPLS, GOPLS_BINARY, "/a");
	await pool.acquire(GOPLS, GOPLS_BINARY, "/b");
	await pool.acquire(GOPLS, GOPLS_BINARY, "/c");
	assert.equal(pool.size, 2);
	assert.equal(spawns, 3);

	// /a was the oldest, so it went; asking for it again pays a fresh spawn.
	await pool.acquire(GOPLS, GOPLS_BINARY, "/a");
	assert.equal(spawns, 4);
});

test("entries still initializing are exempt from LRU eviction, so concurrent cold acquires all complete", async () => {
	const fakes: FakeTransport[] = [];
	const initIds: Array<{ fake: FakeTransport; id: number }> = [];
	const pool = new LspServerPool(
		{ maxServers: 1, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				const fake = fakeTransport();
				fakes.push(fake);
				fake.onRequest("initialize", (message) => initIds.push({ fake, id: message.id as number }));
				fake.onRequest("shutdown", (message) => fake.receive({ id: message.id as number, result: null }));
				return new LspClient(root, fake.transport, options);
			},
		},
	);

	// Two cold acquires over a cap of 1 — both handshakes still pending.
	const first = pool.acquire(GOPLS, GOPLS_BINARY, "/a");
	const second = pool.acquire(GOPLS, GOPLS_BINARY, "/b");
	await new Promise((resolve) => setImmediate(resolve));
	assert.equal(fakes.length, 2);
	assert.deepEqual(
		fakes.map((fake) => fake.killed()),
		[false, false],
	);

	for (const { fake, id } of initIds) fake.receive({ id, result: { capabilities: {} } });
	const [clientA, clientB] = await Promise.all([first, second]);
	assert.equal(clientA.dead, false);
	assert.equal(clientB.dead, false);

	// With both settled, the next acquire enforces the cap again.
	const third = pool.acquire(GOPLS, GOPLS_BINARY, "/c");
	await new Promise((resolve) => setImmediate(resolve));
	assert.equal(fakes.slice(0, 2).some((fake) => fake.killed()), true);
	for (const { fake, id } of initIds.slice(2)) fake.receive({ id, result: { capabilities: {} } });
	await third;
});

test("the idle timer defers disposal while a request is in flight", async () => {
	const fakes: FakeTransport[] = [];
	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 40, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				const fake = fakeTransport({ autoHandshake: true });
				fakes.push(fake);
				return new LspClient(root, fake.transport, options);
			},
		},
	);
	const client = await pool.acquire(GOPLS, GOPLS_BINARY, "/repo");
	const pending = client.request("textDocument/references", {});

	// Two idle windows pass with the request still outstanding — no disposal.
	await new Promise((resolve) => setTimeout(resolve, 100));
	assert.equal(fakes[0].killed(), false);

	const id = fakes[0].sent.find((message) => message.method === "textDocument/references")?.id as number;
	fakes[0].receive({ id, result: [] });
	await pending;
	// Once idle for real, the next window reclaims it.
	await new Promise((resolve) => setTimeout(resolve, 100));
	assert.equal(fakes[0].killed(), true);
});

test("the pool replaces a server that died rather than handing it out again", async () => {
	let spawns = 0;
	const transports: FakeTransport[] = [];
	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				spawns += 1;
				const fake = fakeTransport({ autoHandshake: true });
				transports.push(fake);
				return new LspClient(root, fake.transport, options);
			},
		},
	);
	const first = await pool.acquire(GOPLS, GOPLS_BINARY, "/repo");
	transports[0].close({ code: 1, stderr: "gopls: out of memory" });
	assert.equal(first.dead, true);

	const second = await pool.acquire(GOPLS, GOPLS_BINARY, "/repo");
	assert.notEqual(first, second);
	assert.equal(spawns, 2);
});

// ---------- lsp-client ----------

test("planDocumentSync opens once, versions each change, and skips an unchanged buffer", () => {
	assert.deepEqual(planDocumentSync(undefined, "a"), { action: "open", version: 1 });
	assert.deepEqual(planDocumentSync({ version: 1, text: "a" }, "a"), { action: "none" });
	assert.deepEqual(planDocumentSync({ version: 1, text: "a" }, "b"), { action: "change", version: 2 });
	assert.deepEqual(planDocumentSync({ version: 7, text: "a" }, "b"), { action: "change", version: 8 });
});

test("the client syncs a document as didOpen then didChange, and stays quiet when it has not moved", async () => {
	const fake = fakeTransport();
	const client = new LspClient("/repo", fake.transport, CLIENT_OPTIONS);
	const uri = "file:///repo/main.go";

	assert.equal(await client.ensureDocument(uri, "one", "go"), "open");
	assert.equal(await client.ensureDocument(uri, "one", "go"), "none");
	assert.equal(await client.ensureDocument(uri, "two", "go"), "change");

	const notifications = fake.sent.filter((message) => message.method?.startsWith("textDocument/did"));
	assert.deepEqual(
		notifications.map((message) => message.method),
		["textDocument/didOpen", "textDocument/didChange"],
	);
	assert.equal((notifications[0].params as { textDocument: { version: number; languageId: string } }).textDocument.version, 1);
	assert.equal((notifications[0].params as { textDocument: { languageId: string } }).textDocument.languageId, "go");
	assert.equal((notifications[1].params as { textDocument: { version: number } }).textDocument.version, 2);
});

test("concurrent requests resolve against their own ids", async () => {
	const fake = fakeTransport();
	const client = new LspClient("/repo", fake.transport, CLIENT_OPTIONS);
	const hover = client.request("textDocument/hover", {});
	const definition = client.request("textDocument/definition", {});

	const ids = fake.sent.map((message) => message.id as number);
	assert.equal(new Set(ids).size, 2);
	// Answer out of order: correlation must be by id, not arrival.
	fake.receive({ id: ids[1], result: "definition-result" });
	fake.receive({ id: ids[0], result: "hover-result" });

	assert.equal(await hover, "hover-result");
	assert.equal(await definition, "definition-result");
});

test("the client answers workspace/configuration with one null per item", () => {
	const fake = fakeTransport();
	// eslint-disable-next-line no-new
	new LspClient("/repo", fake.transport, CLIENT_OPTIONS);
	fake.receive({ id: 41, method: "workspace/configuration", params: { items: [{ section: "go" }, { section: "gopls" }] } });

	const reply = fake.sent.find((message) => message.id === 41);
	assert.deepEqual(reply?.result, [null, null]);
});

test("the client refuses an unknown server request instead of leaving it hanging", () => {
	const fake = fakeTransport();
	// eslint-disable-next-line no-new
	new LspClient("/repo", fake.transport, CLIENT_OPTIONS);
	fake.receive({ id: 55, method: "workspace/inventedRequest", params: {} });

	const reply = fake.sent.find((message) => message.id === 55);
	assert.equal(reply?.error?.code, -32601);
	assert.match(reply?.error?.message ?? "", /workspace\/inventedRequest/);
});

test("a server that exits rejects everything in flight with its exit code and last stderr line", async () => {
	const fake = fakeTransport();
	const client = new LspClient("/repo", fake.transport, CLIENT_OPTIONS);
	const pending = client.request("textDocument/hover", {});
	fake.close({ code: 2, stderr: "loading packages\ngopls: fatal: cannot find module\n" });

	await assert.rejects(() => pending, /code 2.*cannot find module/s);
	assert.equal(client.dead, true);
});

test("a request that outlives its budget cancels upstream but leaves the server running", async () => {
	const fake = fakeTransport();
	const client = new LspClient("/repo", fake.transport, FAST_OPTIONS);
	await assert.rejects(() => client.request("textDocument/references", {}), /still be indexing/);

	assert.ok(fake.sent.some((message) => message.method === "$/cancelRequest"));
	assert.equal(fake.killed(), false);
	assert.equal(client.dead, false);
});

test("waitForDiagnostics resolves on a publish newer than the caller's generation", async () => {
	const fake = fakeTransport();
	const client = new LspClient("/repo", fake.transport, CLIENT_OPTIONS);
	const uri = "file:///repo/app.py";
	const generation = client.diagnosticsGeneration();
	const pending = client.waitForDiagnostics(uri, generation, 1000);

	fake.receive({
		method: "textDocument/publishDiagnostics",
		params: { uri, diagnostics: [{ range: { start: { line: 9, character: 4 }, end: { line: 9, character: 5 } }, severity: 1, message: "boom" }] },
	});

	const result = await pending;
	assert.equal(result.fresh, true);
	assert.equal(result.items.length, 1);
});

test("waitForDiagnostics gives up rather than blocking when the server stays silent", async () => {
	const fake = fakeTransport();
	const client = new LspClient("/repo", fake.transport, CLIENT_OPTIONS);
	const result = await client.waitForDiagnostics("file:///repo/app.py", 0, 20);
	assert.equal(result.fresh, false);
	assert.deepEqual(result.items, []);
});

// ---------- lsp-format ----------

test("normalizeLocations understands every shape a server may answer with", () => {
	const range = { start: { line: 40, character: 7 }, end: { line: 40, character: 12 } };
	assert.deepEqual(normalizeLocations(null), []);
	assert.deepEqual(normalizeLocations({ uri: "file:///r/a.go", range }), [{ path: "/r/a.go", line: 41, column: 8 }]);
	assert.deepEqual(normalizeLocations([{ uri: "file:///r/a.go", range }]), [{ path: "/r/a.go", line: 41, column: 8 }]);
	// LocationLink must land on the name, not the whole declaration body.
	assert.deepEqual(
		normalizeLocations([
			{
				targetUri: "file:///r/b.go",
				targetRange: { start: { line: 3, character: 0 }, end: { line: 20, character: 1 } },
				targetSelectionRange: range,
			},
		]),
		[{ path: "/r/b.go", line: 41, column: 8 }],
	);
});

test("a capped references list says how many it is hiding", () => {
	const locations = Array.from({ length: 247 }, (_unused, index) => ({ path: "/r/a.go", line: index + 1, column: 3 }));
	const text = formatLocations(locations, { cwd: "/r", readLine: () => undefined, limit: 100, label: "references" });
	const lines = text.split("\n");
	assert.equal(lines[0], "247 references (showing first 100)");
	assert.equal(lines.length, 101);
	assert.equal(lines[1], "a.go:1:3:");
});

test("locations render grep-style with the source line, relative when inside the working directory", () => {
	const readLine = createLineReader(() => "func Main() {\n\treturn\n}");
	const text = formatLocations([{ path: "/r/pkg/a.go", line: 1, column: 6 }], { cwd: "/r", readLine, limit: 10 });
	assert.equal(text, "pkg/a.go:1:6: func Main() {");
	// Outside the working directory the absolute path is kept.
	const outside = formatLocations([{ path: "/elsewhere/b.go", line: 1, column: 1 }], { cwd: "/r", readLine: () => undefined, limit: 10 });
	assert.equal(outside, "/elsewhere/b.go:1:1:");
});

test("hover renders markup, a language-tagged string, and an array of both", () => {
	assert.equal(formatHover({ contents: { kind: "markdown", value: "**Main** does a thing" } }), "**Main** does a thing");
	assert.equal(formatHover({ contents: { language: "go", value: "func Main()" } }), "```go\nfunc Main()\n```");
	assert.equal(formatHover({ contents: ["plain text", { language: "go", value: "func Main()" }] }), "plain text\n\n```go\nfunc Main()\n```");
	assert.equal(formatHover(null), "");
});

test("symbols render as a tree when hierarchical and flat when the server sends SymbolInformation", () => {
	const range = { start: { line: 4, character: 0 }, end: { line: 9, character: 1 } };
	const tree = formatSymbols(
		[{ name: "Server", kind: 23, range, selectionRange: range, children: [{ name: "Start", kind: 6, detail: "func()", range, selectionRange: range }] }],
		"/r",
	);
	assert.equal(tree, "Struct Server :5\n  Method Start func() :5");

	const flat = formatSymbols(
		[{ name: "Start", kind: 12, containerName: "Server", location: { uri: "file:///r/a.go", range } }],
		"/r",
	);
	assert.equal(flat, "Function Start (in Server) a.go:5");
});

test("diagnostics lead with a severity summary and tag their source", () => {
	const at = (line: number) => ({ start: { line, character: 4 }, end: { line, character: 9 } });
	const text = formatDiagnostics(
		[
			{ range: at(9), severity: 1, source: "pyright", message: '"x" is not defined' },
			{ range: at(20), severity: 2, source: "pyright", message: "unused import\n  os" },
		],
		"/r/app.py",
		"/r",
	);
	assert.equal(
		text,
		['1 error, 1 warning', 'error app.py:10:5: "x" is not defined [pyright]', "warning app.py:21:5: unused import os [pyright]"].join("\n"),
	);
	assert.equal(formatDiagnostics([], "/r/app.py", "/r"), "");
});

// ---------- lsp tool core ----------

test("position operations refuse to run without line and column, and name what is missing", () => {
	assert.throws(() => assertOperationParams({ operation: "definition", path: "a.go" }), /`line` and `column`/);
	assert.throws(() => assertOperationParams({ operation: "hover", path: "a.go", line: 4 }), /`column`/);
	// File-scoped operations need neither.
	assert.doesNotThrow(() => assertOperationParams({ operation: "documentSymbol", path: "a.go" }));
	assert.doesNotThrow(() => assertOperationParams({ operation: "diagnostics", path: "a.go" }));
	assert.throws(() => assertOperationParams({ operation: "rename" as never, path: "a.go" }), /Unknown lsp operation/);
});

test("tool paths tolerate the @ prefix models emit and resolve against the working directory", () => {
	assert.equal(normalizeToolPath("@src/foo.go", "/repo"), "/repo/src/foo.go");
	assert.equal(normalizeToolPath("  src/foo.go  ", "/repo"), "/repo/src/foo.go");
	assert.equal(normalizeToolPath("/abs/foo.go", "/repo"), "/abs/foo.go");
});

test("an uninstalled language server is reported as text with an install hint, not an error", async () => {
	const { dir, file } = goProject();
	const text = await runLspOperation({ operation: "documentSymbol", path: file }, dir, {
		pool: poolThatMustNotSpawn(),
		which: () => undefined,
	});
	assert.match(text, /gopls is not on PATH/);
	assert.match(text, /go install golang\.org\/x\/tools\/gopls@latest/);
});

test("an unsupported file type lists what is supported instead of failing", async () => {
	const dir = tempDir();
	const file = path.join(dir, "notes.txt");
	fs.writeFileSync(file, "hello\n");
	const text = await runLspOperation({ operation: "documentSymbol", path: file }, dir, { pool: poolThatMustNotSpawn() });
	assert.match(text, /No language server is configured for \.txt/);
	assert.match(text, /\.go/);
});

test("a path that does not exist throws so the model corrects it", async () => {
	const dir = tempDir();
	await assert.rejects(
		() => runLspOperation({ operation: "documentSymbol", path: path.join(dir, "absent.go") }, dir, { pool: poolThatMustNotSpawn() }),
		/File not found/,
	);
});

test("a server that dies mid-request is respawned exactly once", async () => {
	const { dir, file } = goProject();
	let spawns = 0;
	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				spawns += 1;
				const attempt = spawns;
				const fake = fakeTransport({ autoHandshake: true });
				fake.onRequest("textDocument/documentSymbol", (message) => {
					if (attempt === 1) {
						fake.close({ code: 2, stderr: "gopls: fatal" });
						return;
					}
					const range = { start: { line: 2, character: 5 }, end: { line: 2, character: 9 } };
					fake.receive({ id: message.id, result: [{ name: "Main", kind: 12, range, selectionRange: range }] });
				});
				return new LspClient(root, fake.transport, options);
			},
		},
	);

	const text = await runLspOperation({ operation: "documentSymbol", path: file }, dir, { pool, which: () => "/usr/bin/gopls" });
	assert.equal(spawns, 2);
	assert.equal(text, "Function Main :3");
});

test("a request that fails without killing the server is not retried", async () => {
	const { dir, file } = goProject();
	let spawns = 0;
	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				spawns += 1;
				const fake = fakeTransport({ autoHandshake: true });
				fake.onRequest("textDocument/documentSymbol", (message) => {
					fake.receive({ id: message.id, error: { code: -32603, message: "internal server error" } });
				});
				return new LspClient(root, fake.transport, options);
			},
		},
	);

	await assert.rejects(
		() => runLspOperation({ operation: "documentSymbol", path: file }, dir, { pool, which: () => "/usr/bin/gopls" }),
		/internal server error/,
	);
	assert.equal(spawns, 1);
});

test("a definition query opens the document and reports the resolved site", async () => {
	const { dir, file } = goProject();
	const target = path.join(dir, "other.go");
	fs.writeFileSync(target, "package main\n\nfunc Helper() {}\n");
	let opened: string | undefined;

	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				const fake = fakeTransport({ autoHandshake: true });
				fake.onRequest("textDocument/didOpen", (message) => {
					opened = (message.params as { textDocument: { uri: string } }).textDocument.uri;
				});
				fake.onRequest("textDocument/definition", (message) => {
					fake.receive({
						id: message.id,
						result: { uri: pathToUri(target), range: { start: { line: 2, character: 5 }, end: { line: 2, character: 11 } } },
					});
				});
				return new LspClient(root, fake.transport, options);
			},
		},
	);

	const text = await runLspOperation({ operation: "definition", path: file, line: 3, column: 6 }, dir, {
		pool,
		which: () => "/usr/bin/gopls",
	});
	assert.equal(opened, pathToUri(file));
	assert.equal(text, "other.go:3:6: func Helper() {}");
});

test("a definition with no result is reported as text at the queried position", async () => {
	const { dir, file } = goProject();
	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				const fake = fakeTransport({ autoHandshake: true });
				fake.onRequest("textDocument/definition", (message) => fake.receive({ id: message.id, result: null }));
				return new LspClient(root, fake.transport, options);
			},
		},
	);
	const text = await runLspOperation({ operation: "definition", path: file, line: 3, column: 6 }, dir, {
		pool,
		which: () => "/usr/bin/gopls",
	});
	assert.equal(text, "No definition found at main.go:3:6");
});

test("repeat diagnostics on an unchanged file returns the cached publish as fresh, without waiting", async () => {
	const { dir, file } = goProject();
	const pool = new LspServerPool(
		{ maxServers: 4, idleMs: 60_000, ...CLIENT_OPTIONS },
		{
			createClient: (_spec, _binary, root, options) => {
				const fake = fakeTransport({ autoHandshake: true });
				fake.onRequest("textDocument/didOpen", (message) => {
					const uri = (message.params as { textDocument: { uri: string } }).textDocument.uri;
					fake.receive({
						method: "textDocument/publishDiagnostics",
						params: { uri, diagnostics: [{ range: { start: { line: 0, character: 0 }, end: { line: 0, character: 1 } }, severity: 2, message: "unused" }] },
					});
				});
				return new LspClient(root, fake.transport, options);
			},
		},
	);
	const deps = { pool, which: () => "/usr/bin/gopls", diagnosticsWaitMs: 60_000 };

	const first = await runLspOperation({ operation: "diagnostics", path: file }, dir, deps);
	assert.match(first, /unused/);
	// The 60s wait budget makes the assertion sharp: a second call that demanded
	// a newer publish would hang here instead of returning the current cache.
	const second = await runLspOperation({ operation: "diagnostics", path: file }, dir, deps);
	assert.match(second, /unused/);
	assert.doesNotMatch(second, /may be stale/);
});

test("fallback bin dirs honor GOBIN, the first GOPATH element, and CARGO_HOME", () => {
	const seen: string[] = [];
	const which = (command: string) => {
		seen.push(command);
		return undefined;
	};

	resolveBinary(GOPLS, which, { GOBIN: "/custom/gobin", GOPATH: `/work/gopath${path.delimiter}/second/gopath` });
	assert.ok(seen.includes(path.join("/custom/gobin", "gopls")));
	assert.ok(seen.includes(path.join("/work/gopath", "bin", "gopls")));
	assert.ok(!seen.some((candidate) => candidate.includes(path.join("second", "gopath"))));

	seen.length = 0;
	resolveBinary(GOPLS, which, {});
	assert.ok(seen.some((candidate) => candidate.endsWith(path.join("go", "bin", "gopls"))));

	seen.length = 0;
	resolveBinary(RUST_SPEC, which, { CARGO_HOME: "/opt/cargo" });
	assert.ok(seen.includes(path.join("/opt/cargo", "bin", "rust-analyzer")));
});

// ---------- lsp-install ----------

const TS_SPEC = LSP_SERVERS.find((spec) => spec.id === "typescript")!;
const RUST_SPEC = LSP_SERVERS.find((spec) => spec.id === "rust-analyzer")!;

/** Minimal fake ChildProcess for installServer: emits output then closes. */
function fakeInstallProcess(code: number | null, output: string) {
	const handlers = new Map<string, (arg: unknown) => void>();
	const stream = { on: (_event: string, handler: (chunk: Buffer) => void) => queueMicrotask(() => output && handler(Buffer.from(output))) };
	queueMicrotask(() => queueMicrotask(() => handlers.get("close")?.(code)));
	return {
		stdout: stream,
		stderr: { on: () => undefined },
		on: (event: string, handler: (arg: unknown) => void) => handlers.set(event, handler),
		kill: () => true,
	};
}

test("workspace detection reads positive markers at the root and one level down", () => {
	const dir = tempDir();
	fs.writeFileSync(path.join(dir, "go.mod"), "module x\n");
	fs.mkdirSync(path.join(dir, "web"));
	fs.writeFileSync(path.join(dir, "web", "tsconfig.json"), "{}");
	assert.deepEqual(
		detectWorkspaceServers(dir).map((spec) => spec.id),
		["gopls", "typescript"],
	);
});

test("workspace detection ignores dot dirs and vendored trees, and .git is never a language signal", () => {
	const dir = tempDir();
	fs.mkdirSync(path.join(dir, ".git"));
	fs.mkdirSync(path.join(dir, "node_modules", "dep"), { recursive: true });
	fs.writeFileSync(path.join(dir, "node_modules", "dep", "Cargo.toml"), "");
	assert.deepEqual(detectWorkspaceServers(dir), []);
});

test("dismissals round-trip through the prefs file and tolerate a corrupt one", () => {
	const prefsPath = path.join(tempDir(), ".weave_lsp.json");
	assert.deepEqual([...loadDismissedLanguages(prefsPath)], []);
	saveDismissedLanguages(prefsPath, new Set(["go", "rust"]));
	assert.deepEqual([...loadDismissedLanguages(prefsPath)].sort(), ["go", "rust"]);
	fs.writeFileSync(prefsPath, "not json{");
	assert.deepEqual([...loadDismissedLanguages(prefsPath)], []);
});

test("the offer names missing languages, instructs consent-gated lsp_enable, and honors dismissals", () => {
	const offer = buildLspOffer([GOPLS, TS_SPEC], new Set());
	assert.ok(offer);
	assert.match(offer, /go, typescript/);
	assert.match(offer, /lsp_enable/);
	assert.match(offer, /explicit go-ahead/);

	const partial = buildLspOffer([GOPLS, TS_SPEC], new Set(["go"]));
	assert.ok(partial);
	assert.doesNotMatch(partial, /go,/);
	assert.equal(buildLspOffer([GOPLS], new Set(["go"])), undefined);
	assert.equal(buildLspOffer([], new Set()), undefined);
});

test("installServer refuses without the language toolchain, naming it", async () => {
	const result = await installServer(RUST_SPEC, { which: () => undefined });
	assert.equal(result.ok, false);
	assert.match(result.text, /rustup/);
	assert.match(result.text, /rustup component add rust-analyzer/);
});

test("installServer runs the registry argv and reports success once the binary resolves", async () => {
	let spawned: { command: string; args: string[] } | undefined;
	let installed = false;
	const result = await installServer(RUST_SPEC, {
		which: (command) => {
			if (command === "rustup") return "/usr/bin/rustup";
			return installed && command === "rust-analyzer" ? "/usr/bin/rust-analyzer" : undefined;
		},
		spawnFn: (command, args) => {
			spawned = { command, args };
			installed = true;
			return fakeInstallProcess(0, "info: installing component") as never;
		},
	});
	assert.deepEqual(spawned, { command: "rustup", args: ["component", "add", "rust-analyzer"] });
	assert.equal(result.ok, true);
	assert.match(result.text, /lsp tool now works for rust/);
});

test("a cancelled install settles only after the child exits, escalating SIGTERM to SIGKILL", async () => {
	const signals: string[] = [];
	const handlers = new Map<string, (arg: unknown) => void>();
	// A child that ignores SIGTERM and dies only on SIGKILL.
	const stubbornChild = {
		stdout: { on: () => undefined },
		stderr: { on: () => undefined },
		on: (event: string, handler: (arg: unknown) => void) => handlers.set(event, handler),
		kill: (signal: string) => {
			signals.push(signal);
			if (signal === "SIGKILL") queueMicrotask(() => handlers.get("close")?.(null));
			return true;
		},
	};

	const started = Date.now();
	const result = await installServer(RUST_SPEC, {
		which: (command) => (command === "rustup" ? "/usr/bin/rustup" : undefined),
		spawnFn: () => stubbornChild as never,
		timeoutMs: 30,
		killGraceMs: 40,
	});

	// Settled via close after the SIGKILL escalation — not at SIGTERM time.
	assert.deepEqual(signals, ["SIGTERM", "SIGKILL"]);
	assert.ok(Date.now() - started >= 60, "must wait out timeout + kill grace before settling");
	assert.equal(result.ok, false);
	assert.match(result.text, /timed out/);
	assert.match(result.text, /stopped/);
});

test("a failed install surfaces the exit code and last output line", async () => {
	const result = await installServer(RUST_SPEC, {
		which: (command) => (command === "rustup" ? "/usr/bin/rustup" : undefined),
		spawnFn: () => fakeInstallProcess(1, "error: no such component\n") as never,
	});
	assert.equal(result.ok, false);
	assert.match(result.text, /exit 1/);
	assert.match(result.text, /no such component/);
});

interface FakeTool {
	execute(toolCallId: string, params: unknown, signal?: AbortSignal): Promise<{ content: Array<{ text: string }>; isError?: boolean }>;
}

/** Registers the real main-process tools against a fake pi; returns them by name. */
function lspToolHarness(deps: Parameters<typeof registerLsp>[1]) {
	const tools = new Map<string, FakeTool>();
	const handlers = new Map<string, Array<(event: unknown, ctx: unknown) => unknown>>();
	const pi = {
		registerTool: (tool: { name: string }) => tools.set(tool.name, tool as unknown as FakeTool),
		on: (event: string, handler: (event: unknown, ctx: unknown) => unknown) => {
			handlers.set(event, [...(handlers.get(event) ?? []), handler]);
		},
	} as never;
	registerLsp(pi, deps);
	return {
		tool: (name: string) => tools.get(name),
		emit: (event: string, payload: unknown, ctx: unknown) => (handlers.get(event) ?? []).map((handler) => handler(payload, ctx)),
	};
}

test("lsp_enable dismiss persists the opt-out and the offer stops being injected", async () => {
	const dir = tempDir();
	fs.writeFileSync(path.join(dir, "go.mod"), "module x\n");
	const prefsPath = path.join(dir, ".weave_lsp.json");
	const harness = lspToolHarness({
		prefsPath,
		which: () => undefined,
		detect: () => [GOPLS],
		startBroker: (() => Promise.reject(new Error("not under test"))) as never,
	});

	const [before] = harness.emit("before_agent_start", { systemPrompt: "base" }, { cwd: dir }) as Array<{ systemPrompt: string } | undefined>;
	assert.match(before?.systemPrompt ?? "", /Code intelligence \(LSP\)/);

	const result = await harness.tool("lsp_enable")!.execute("t1", { language: "go", action: "dismiss" });
	assert.match(result.content[0].text, /will not be offered again/);
	assert.deepEqual([...loadDismissedLanguages(prefsPath)], ["go"]);

	const [after] = harness.emit("before_agent_start", { systemPrompt: "base" }, { cwd: dir }) as Array<{ systemPrompt: string } | undefined>;
	assert.equal(after, undefined);

	// A fresh session re-detects but still respects the persisted dismissal.
	harness.emit("session_start", {}, { cwd: dir });
	const [nextSession] = harness.emit("before_agent_start", { systemPrompt: "base" }, { cwd: dir }) as Array<{ systemPrompt: string } | undefined>;
	assert.equal(nextSession, undefined);
});

test("lsp_enable install runs the injected installer and a user-requested install clears an old dismissal", async () => {
	const dir = tempDir();
	const prefsPath = path.join(dir, ".weave_lsp.json");
	saveDismissedLanguages(prefsPath, new Set(["go"]));
	const installedFor: string[] = [];
	const harness = lspToolHarness({
		prefsPath,
		which: () => undefined,
		detect: () => [],
		install: async (spec) => {
			installedFor.push(spec.language);
			return { ok: true, text: "Installed the go language server" };
		},
		startBroker: (() => Promise.reject(new Error("not under test"))) as never,
	});

	const result = await harness.tool("lsp_enable")!.execute("t1", { language: "go" });
	assert.ok(!result.isError);
	assert.deepEqual(installedFor, ["go"]);
	assert.deepEqual([...loadDismissedLanguages(prefsPath)], []);
});

// ---------- lsp-broker (real sockets) ----------

test("a child reaches the parent's runner over the broker socket", async () => {
	const handle = await startLspBroker(async (params, cwd) => `${params.operation} ${params.path} @ ${cwd}`);
	const client = new LspBrokerClient(handle.socketPath, handle.token, 5000);
	try {
		const text = await client.execute({ operation: "hover", path: "/repo/a.go", line: 2, column: 3 }, "/child/cwd");
		assert.equal(text, "hover /repo/a.go @ /child/cwd");
	} finally {
		client.close();
		await handle.close();
	}
});

test("the broker hangs up on a connection that does not present the token", async () => {
	const handle = await startLspBroker(async () => "must not be served");
	const client = new LspBrokerClient(handle.socketPath, "0".repeat(handle.token.length), 2000);
	try {
		await assert.rejects(
			() => client.execute({ operation: "hover", path: "/repo/a.go", line: 1, column: 1 }, "/child"),
			/connection lost/,
		);
	} finally {
		client.close();
		await handle.close();
	}
});

test("a failure inside the parent's runner re-throws in the child", async () => {
	const handle = await startLspBroker(async () => {
		throw new Error("language server exited (code 2): gopls: fatal");
	});
	const client = new LspBrokerClient(handle.socketPath, handle.token, 5000);
	try {
		await assert.rejects(
			() => client.execute({ operation: "references", path: "/repo/a.go", line: 1, column: 1 }, "/child"),
			/gopls: fatal/,
		);
	} finally {
		client.close();
		await handle.close();
	}
});

test("a broker that goes away rejects the child's in-flight request rather than hanging", async () => {
	const handle = await startLspBroker(() => new Promise<string>(() => undefined));
	const client = new LspBrokerClient(handle.socketPath, handle.token, 5000);
	const pending = client.execute({ operation: "hover", path: "/repo/a.go", line: 1, column: 1 }, "/child");
	// Let the hello and the request land before the parent disappears.
	await new Promise((resolve) => setTimeout(resolve, 50));
	await handle.close();

	await assert.rejects(() => pending, /LSP broker connection lost/);
	client.close();
});

test("the broker socket is removed when it closes", async () => {
	const handle = await startLspBroker(async () => "ok");
	if (process.platform !== "win32") assert.equal(fs.existsSync(handle.socketPath), true);
	await handle.close();
	assert.equal(fs.existsSync(handle.socketPath), false);
});
