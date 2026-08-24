/**
 * Subagent access to the parent's language servers.
 *
 * Dispatch children are the heaviest potential LSP users and the shortest-lived
 * processes, so letting each spawn its own gopls would pay the indexing cost N
 * times over and then throw the warm index away. Instead the parent exposes its
 * pool over a local socket and children forward queries to it. The socket
 * carries the same Content-Length framing as LSP stdio.
 *
 * The broker takes the orchestration core as a function, so it never reaches
 * into the pool itself and is testable against a fake runner.
 */

import { randomBytes, timingSafeEqual } from "node:crypto";
import * as fs from "node:fs";
import * as net from "node:net";
import * as os from "node:os";
import * as path from "node:path";
import { createFrameParser, encodeFrame, type JsonRpcMessage, type LspOperationParams } from "./lsp-protocol.js";

const HELLO = "broker/hello";
const EXECUTE = "lsp/execute";
const CANCEL = "$/cancel";
const CONNECTION_LOST = "LSP broker connection lost";

export type LspRunner = (params: LspOperationParams, cwd: string, signal?: AbortSignal) => Promise<string>;

export interface LspBrokerHandle {
	socketPath: string;
	token: string;
	close(): Promise<void>;
	/** Synchronous socket removal for `process.on("exit")`, which cannot await `close()`. */
	removeSocket(): void;
}

export interface BrokerDeps {
	makeSocketPath?(): { socketPath: string; cleanup(): void };
	makeToken?(): string;
}

function tokensMatch(expected: string, received: unknown): boolean {
	if (typeof received !== "string" || received.length !== expected.length) return false;
	return timingSafeEqual(Buffer.from(expected), Buffer.from(received));
}

function defaultSocketPath(): { socketPath: string; cleanup(): void } {
	if (process.platform === "win32") {
		// Named pipes live in the kernel namespace: nothing to create or unlink.
		return { socketPath: `\\\\.\\pipe\\weave-pi-lsp-${process.pid}-${randomBytes(6).toString("hex")}`, cleanup: () => undefined };
	}
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), "weave-pi-lsp-"));
	fs.chmodSync(dir, 0o700);
	const socketPath = path.join(dir, "lsp.sock");
	return {
		socketPath,
		cleanup: () => {
			try {
				fs.rmSync(dir, { recursive: true, force: true });
			} catch {
				/* best effort */
			}
		},
	};
}

/**
 * Listen for child connections. Started on demand (right before a fan-out), so
 * a session that never dispatches never opens a socket.
 */
export async function startLspBroker(runner: LspRunner, deps: BrokerDeps = {}): Promise<LspBrokerHandle> {
	const token = (deps.makeToken ?? (() => randomBytes(32).toString("hex")))();
	const { socketPath, cleanup } = (deps.makeSocketPath ?? defaultSocketPath)();
	const connections = new Set<net.Socket>();

	const server = net.createServer((socket) => {
		connections.add(socket);
		socket.on("close", () => connections.delete(socket));
		socket.on("error", () => socket.destroy());

		let authenticated = false;
		const inflight = new Map<number, AbortController>();
		const reply = (message: JsonRpcMessage): void => {
			if (!socket.destroyed) socket.write(encodeFrame(message));
		};

		const parser = createFrameParser(
			(message) => {
				if (!authenticated) {
					// Children share the user's privileges, so the token is not a
					// privilege boundary — it keeps unrelated local processes out.
					if (message.method !== HELLO || !tokensMatch(token, (message.params as { token?: unknown } | undefined)?.token)) {
						socket.destroy();
						return;
					}
					authenticated = true;
					return;
				}

				if (message.method === CANCEL) {
					const id = (message.params as { id?: unknown } | undefined)?.id;
					if (typeof id === "number") inflight.get(id)?.abort();
					return;
				}
				if (message.method !== EXECUTE || typeof message.id !== "number") return;

				const id = message.id;
				const payload = message.params as { params?: LspOperationParams; cwd?: string } | undefined;
				if (!payload?.params) {
					reply({ id, error: { code: -32602, message: "missing lsp params" } });
					return;
				}
				const controller = new AbortController();
				inflight.set(id, controller);
				runner(payload.params, payload.cwd || process.cwd(), controller.signal)
					.then((text) => reply({ id, result: { text } }))
					.catch((error: Error) => reply({ id, error: { code: -32000, message: error.message } }))
					.finally(() => inflight.delete(id));
			},
			() => socket.destroy(),
		);

		socket.on("data", (chunk: Buffer) => parser.push(chunk));
	});

	await new Promise<void>((resolve, reject) => {
		const onError = (error: Error): void => reject(error);
		server.once("error", onError);
		server.listen(socketPath, () => {
			server.removeListener("error", onError);
			resolve();
		});
	});
	// The listener must never be the reason the parent process stays alive.
	server.unref();

	return {
		socketPath,
		token,
		removeSocket: cleanup,
		close(): Promise<void> {
			for (const socket of connections) socket.destroy();
			connections.clear();
			return new Promise<void>((resolve) => {
				server.close(() => {
					cleanup();
					resolve();
				});
			});
		},
	};
}

type ConnectFn = (socketPath: string) => net.Socket;

interface BrokerPending {
	resolve(text: string): void;
	reject(error: Error): void;
}

/** The child half: one lazy connection, shared by every `lsp` call in that process. */
export class LspBrokerClient {
	private nextId = 1;
	private readonly pending = new Map<number, BrokerPending>();
	private connection?: Promise<net.Socket>;

	constructor(
		private readonly socketPath: string,
		private readonly token: string,
		private readonly timeoutMs: number,
		private readonly connectFn: ConnectFn = (target) => net.connect(target),
	) {}

	async execute(params: LspOperationParams, cwd: string, signal?: AbortSignal): Promise<string> {
		const socket = await this.connect();
		const id = this.nextId++;

		return new Promise<string>((resolve, reject) => {
			let settled = false;
			const cleanup = (): void => {
				settled = true;
				clearTimeout(timer);
				signal?.removeEventListener("abort", onAbort);
				this.pending.delete(id);
			};
			const onAbort = (): void => {
				if (settled) return;
				cleanup();
				if (!socket.destroyed) socket.write(encodeFrame({ method: CANCEL, params: { id } }));
				reject(new Error("aborted"));
			};
			const timer = setTimeout(() => {
				if (settled) return;
				cleanup();
				if (!socket.destroyed) socket.write(encodeFrame({ method: CANCEL, params: { id } }));
				reject(new Error("timed out waiting for the LSP broker"));
			}, this.timeoutMs);

			this.pending.set(id, {
				resolve: (text) => {
					if (settled) return;
					cleanup();
					resolve(text);
				},
				reject: (error) => {
					if (settled) return;
					cleanup();
					reject(error);
				},
			});
			signal?.addEventListener("abort", onAbort, { once: true });
			socket.write(encodeFrame({ id, method: EXECUTE, params: { params, cwd } }));
		});
	}

	close(): void {
		const pending = this.connection;
		this.connection = undefined;
		void pending?.then((socket) => socket.destroy()).catch(() => undefined);
	}

	private connect(): Promise<net.Socket> {
		if (this.connection) return this.connection;
		this.connection = new Promise<net.Socket>((resolve, reject) => {
			const socket = this.connectFn(this.socketPath);
			const parser = createFrameParser(
				(message) => {
					if (typeof message.id !== "number") return;
					const entry = this.pending.get(message.id);
					if (!entry) return;
					if (message.error) entry.reject(new Error(message.error.message));
					else entry.resolve(((message.result as { text?: string } | undefined)?.text) ?? "");
				},
				() => socket.destroy(),
			);

			const fail = (error: Error): void => {
				// Drop the cached connection so a later call can retry a live parent.
				this.connection = undefined;
				for (const entry of [...this.pending.values()]) entry.reject(error);
				this.pending.clear();
				reject(error);
			};

			socket.on("data", (chunk: Buffer) => parser.push(chunk));
			socket.on("error", (error: Error) => fail(error));
			socket.on("close", () => fail(new Error(CONNECTION_LOST)));
			socket.on("connect", () => {
				socket.write(encodeFrame({ method: HELLO, params: { token: this.token } }));
				resolve(socket);
			});
			socket.unref?.();
		});
		return this.connection;
	}
}
