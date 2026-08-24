/**
 * One language-server connection.
 *
 * The server is reached through an `LspTransport` rather than a ChildProcess so
 * the protocol logic — handshake, document versioning, request correlation,
 * diagnostics bookkeeping — is exercised in tests without spawning anything.
 */

import { spawn as nodeSpawn, type ChildProcess, type SpawnOptions } from "node:child_process";
import * as path from "node:path";
import { createFrameParser, encodeFrame, pathToUri, type JsonRpcMessage } from "./lsp-protocol.js";
import type { LspDiagnostic } from "./lsp-format.js";

const STDERR_RING_BYTES = 4096;
const SHUTDOWN_BUDGET_MS = 1000;
const SIGKILL_GRACE_MS = 5000;
const METHOD_NOT_FOUND = -32601;

export interface TransportClose {
	code: number | null;
	stderr: string;
}

export interface LspTransport {
	send(data: Buffer): void;
	onMessage(handler: (message: JsonRpcMessage) => void): void;
	onClose(handler: (info: TransportClose) => void): void;
	/** Graceful stop: SIGTERM, then SIGKILL if the peer has not exited. */
	kill(): void;
	/** Synchronous SIGKILL for process-exit paths that cannot await anything. */
	killNow(): void;
}

export type SpawnFn = (command: string, args: string[], options: SpawnOptions) => ChildProcess;

export function spawnTransport(command: string, args: string[], cwd: string, spawnFn: SpawnFn = nodeSpawn): LspTransport {
	const child = spawnFn(command, args, { cwd, shell: false, detached: false, stdio: ["pipe", "pipe", "pipe"] });
	const messageHandlers: Array<(message: JsonRpcMessage) => void> = [];
	const closeHandlers: Array<(info: TransportClose) => void> = [];
	let stderrTail = "";
	let settled = false;

	const appendStderr = (text: string): void => {
		stderrTail = (stderrTail + text).slice(-STDERR_RING_BYTES);
	};
	const emitClose = (code: number | null): void => {
		if (settled) return;
		settled = true;
		for (const handler of closeHandlers) handler({ code, stderr: stderrTail });
	};

	const parser = createFrameParser(
		(message) => {
			for (const handler of messageHandlers) handler(message);
		},
		(error) => {
			appendStderr(`\n${error.message}`);
			child.kill("SIGKILL");
		},
	);

	child.stdout?.on("data", (chunk: Buffer) => parser.push(chunk));
	// Server stderr is diagnostic noise (gopls indexing chatter); it feeds error
	// messages only and is never streamed to the model.
	child.stderr?.on("data", (chunk: Buffer) => appendStderr(chunk.toString("utf8")));
	child.on("close", (code) => emitClose(code));
	child.on("error", (error: Error) => {
		appendStderr(error.message);
		emitClose(null);
	});

	return {
		send(data) {
			try {
				child.stdin?.write(data);
			} catch {
				/* peer gone — the close handler rejects everything pending */
			}
		},
		onMessage(handler) {
			messageHandlers.push(handler);
		},
		onClose(handler) {
			closeHandlers.push(handler);
		},
		kill() {
			child.kill("SIGTERM");
			// `child.killed` flips the instant SIGTERM is *sent*, so escalate on
			// whether the process actually exited.
			const timer = setTimeout(() => {
				if (!settled) child.kill("SIGKILL");
			}, SIGKILL_GRACE_MS);
			timer.unref?.();
		},
		killNow() {
			try {
				child.kill("SIGKILL");
			} catch {
				/* already gone */
			}
		},
	};
}

export interface DocumentState {
	version: number;
	text: string;
}

export type DocumentSyncPlan = { action: "none" } | { action: "open" | "change"; version: number };

/** didOpen at v1, didChange at v+1, nothing when the buffer is unchanged. */
export function planDocumentSync(state: DocumentState | undefined, text: string): DocumentSyncPlan {
	if (!state) return { action: "open", version: 1 };
	if (state.text === text) return { action: "none" };
	return { action: "change", version: state.version + 1 };
}

/** Reject when `signal` aborts, leaving `promise` running (a shared warmup must survive one caller leaving). */
export function abortable<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
	if (!signal) return promise;
	if (signal.aborted) return Promise.reject(new Error("aborted"));
	return new Promise<T>((resolve, reject) => {
		const onAbort = (): void => reject(new Error("aborted"));
		signal.addEventListener("abort", onAbort, { once: true });
		promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", onAbort));
	});
}

function lastLine(text: string): string {
	const lines = text
		.split("\n")
		.map((line) => line.trim())
		.filter(Boolean);
	return lines.length > 0 ? lines[lines.length - 1] : "";
}

interface Pending {
	resolve(value: unknown): void;
	reject(error: Error): void;
	settle(): void;
}

interface DiagnosticsEntry {
	generation: number;
	items: LspDiagnostic[];
}

interface DiagnosticsWaiter {
	uri: string;
	minGeneration: number;
	deliver(items: LspDiagnostic[] | undefined): void;
}

export interface LspClientOptions {
	requestTimeoutMs: number;
	warmupTimeoutMs: number;
}

export class LspClient {
	private nextId = 1;
	private readonly pending = new Map<number, Pending>();
	private readonly documents = new Map<string, DocumentState>();
	private readonly diagnostics = new Map<string, DiagnosticsEntry>();
	private diagnosticsWaiters: DiagnosticsWaiter[] = [];
	private syncChain: Promise<void> = Promise.resolve();
	private initializePromise?: Promise<void>;
	private generation = 0;
	private warmedUp = false;
	private disposed = false;
	private closeInfo?: TransportClose;

	constructor(
		readonly root: string,
		private readonly transport: LspTransport,
		private readonly options: LspClientOptions,
	) {
		transport.onMessage((message) => this.handleMessage(message));
		transport.onClose((info) => this.handleClose(info));
	}

	get dead(): boolean {
		return this.closeInfo !== undefined || this.disposed;
	}

	/** True while any request or diagnostics wait is in flight — the pool must not idle-dispose a busy client. */
	get busy(): boolean {
		return this.pending.size > 0 || this.diagnosticsWaiters.length > 0;
	}

	/** Shared across callers and never cancelled by one of them leaving: a rejected handshake poisons the pool entry. */
	initialize(): Promise<void> {
		if (!this.initializePromise) this.initializePromise = this.performInitialize();
		return this.initializePromise;
	}

	private async performInitialize(): Promise<void> {
		await this.request(
			"initialize",
			{
				processId: process.pid,
				rootUri: pathToUri(this.root),
				rootPath: this.root,
				workspaceFolders: [{ uri: pathToUri(this.root), name: path.basename(this.root) }],
				capabilities: {
					textDocument: {
						synchronization: { didSave: false, dynamicRegistration: false },
						definition: { linkSupport: true },
						references: {},
						hover: { contentFormat: ["markdown", "plaintext"] },
						documentSymbol: { hierarchicalDocumentSymbolSupport: true },
						publishDiagnostics: { relatedInformation: false },
					},
					workspace: { workspaceFolders: true },
					// Declaring workDoneProgress routes indexing chatter into $/progress
					// notifications we drop, instead of showMessage traffic. We
					// deliberately do NOT declare workspace.configuration — but we still
					// answer it if the server asks anyway (see handleServerRequest).
					window: { workDoneProgress: true },
				},
			},
			{ timeoutMs: this.options.warmupTimeoutMs },
		);
		this.notify("initialized", {});
	}

	/** Returns the sync action taken, so callers can tell a real didOpen/didChange from a no-op. */
	async ensureDocument(uri: string, text: string, languageId: string): Promise<DocumentSyncPlan["action"]> {
		// Tool executionMode is parallel and broker requests interleave with the
		// main loop's, so version numbering is serialized on one chain.
		const run = this.syncChain.then(() => {
			const plan = planDocumentSync(this.documents.get(uri), text);
			if (plan.action === "none") return plan.action;
			if (plan.action === "open") {
				this.notify("textDocument/didOpen", { textDocument: { uri, languageId, version: plan.version, text } });
			} else {
				this.notify("textDocument/didChange", {
					textDocument: { uri, version: plan.version },
					contentChanges: [{ text }],
				});
			}
			this.documents.set(uri, { version: plan.version, text });
			return plan.action;
		});
		this.syncChain = run.then(
			() => undefined,
			() => undefined,
		);
		return run;
	}

	diagnosticsGeneration(): number {
		return this.generation;
	}

	async waitForDiagnostics(
		uri: string,
		sinceGeneration: number,
		timeoutMs: number,
		signal?: AbortSignal,
	): Promise<{ items: LspDiagnostic[]; fresh: boolean }> {
		const current = this.diagnostics.get(uri);
		if (current && current.generation > sinceGeneration) return { items: current.items, fresh: true };

		const published = await new Promise<LspDiagnostic[] | undefined>((resolve) => {
			let settled = false;
			const waiter: DiagnosticsWaiter = {
				uri,
				minGeneration: sinceGeneration,
				deliver: (items) => {
					if (settled) return;
					settled = true;
					clearTimeout(timer);
					signal?.removeEventListener("abort", onAbort);
					this.diagnosticsWaiters = this.diagnosticsWaiters.filter((entry) => entry !== waiter);
					resolve(items);
				},
			};
			const onAbort = (): void => waiter.deliver(undefined);
			const timer = setTimeout(() => waiter.deliver(undefined), timeoutMs);
			signal?.addEventListener("abort", onAbort, { once: true });
			this.diagnosticsWaiters.push(waiter);
		});

		if (published) return { items: published, fresh: true };
		return { items: this.diagnostics.get(uri)?.items ?? [], fresh: false };
	}

	request(method: string, params: unknown, options: { signal?: AbortSignal; timeoutMs?: number } = {}): Promise<unknown> {
		if (this.closeInfo) return Promise.reject(new Error(this.closeMessage()));
		const id = this.nextId++;
		// The first real request lands while the server is still indexing, so it
		// inherits the warmup budget rather than the steady-state one.
		const timeoutMs = options.timeoutMs ?? (this.warmedUp ? this.options.requestTimeoutMs : this.options.warmupTimeoutMs);

		return new Promise<unknown>((resolve, reject) => {
			let settled = false;
			const cleanup = (): void => {
				settled = true;
				clearTimeout(timer);
				options.signal?.removeEventListener("abort", onAbort);
				this.pending.delete(id);
			};
			const entry: Pending = {
				resolve: (value) => {
					if (settled) return;
					cleanup();
					this.warmedUp = true;
					resolve(value);
				},
				reject: (error) => {
					if (settled) return;
					cleanup();
					reject(error);
				},
				settle: cleanup,
			};
			const onAbort = (): void => {
				if (settled) return;
				cleanup();
				this.notify("$/cancelRequest", { id });
				reject(new Error("aborted"));
			};
			const timer = setTimeout(() => {
				if (settled) return;
				cleanup();
				// Keep the server alive: it is probably still indexing, and the next
				// request against a warm index usually succeeds.
				this.notify("$/cancelRequest", { id });
				reject(new Error(`${method} timed out after ${Math.round(timeoutMs / 1000)}s (the language server may still be indexing — retry shortly)`));
			}, timeoutMs);

			options.signal?.addEventListener("abort", onAbort, { once: true });
			this.pending.set(id, entry);
			this.transport.send(encodeFrame({ jsonrpc: "2.0", id, method, params }));
		});
	}

	notify(method: string, params: unknown): void {
		if (this.closeInfo) return;
		this.transport.send(encodeFrame({ jsonrpc: "2.0", method, params }));
	}

	async dispose(): Promise<void> {
		if (this.disposed) return;
		this.disposed = true;
		try {
			await this.request("shutdown", null, { timeoutMs: SHUTDOWN_BUDGET_MS });
			this.notify("exit", undefined);
		} catch {
			/* an unresponsive server just gets killed */
		}
		this.transport.kill();
	}

	killNow(): void {
		this.disposed = true;
		this.transport.killNow();
	}

	private closeMessage(): string {
		const detail = lastLine(this.closeInfo?.stderr ?? "");
		const code = this.closeInfo?.code;
		return `language server exited${code === null || code === undefined ? "" : ` (code ${code})`}${detail ? `: ${detail}` : ""}`;
	}

	private handleClose(info: TransportClose): void {
		this.closeInfo = info;
		const error = new Error(this.closeMessage());
		for (const entry of [...this.pending.values()]) entry.reject(error);
		this.pending.clear();
		for (const waiter of [...this.diagnosticsWaiters]) waiter.deliver(undefined);
	}

	private handleMessage(message: JsonRpcMessage): void {
		if (message.method !== undefined) {
			if (message.id === undefined || message.id === null) this.handleNotification(message);
			else this.handleServerRequest(message);
			return;
		}
		if (typeof message.id !== "number") return;
		const entry = this.pending.get(message.id);
		if (!entry) return;
		if (message.error) entry.reject(new Error(message.error.message || "language server error"));
		else entry.resolve(message.result);
	}

	private handleNotification(message: JsonRpcMessage): void {
		if (message.method !== "textDocument/publishDiagnostics") return;
		const params = message.params as { uri?: string; diagnostics?: LspDiagnostic[] } | undefined;
		if (!params?.uri) return;
		this.generation += 1;
		const entry: DiagnosticsEntry = { generation: this.generation, items: params.diagnostics ?? [] };
		this.diagnostics.set(params.uri, entry);
		for (const waiter of [...this.diagnosticsWaiters]) {
			if (waiter.uri === params.uri && entry.generation > waiter.minGeneration) waiter.deliver(entry.items);
		}
	}

	/**
	 * Every server request gets an answer. A server left waiting on a reply it
	 * asked for will stall the whole session, so unknown methods are refused
	 * explicitly rather than ignored.
	 */
	private handleServerRequest(message: JsonRpcMessage): void {
		const reply = (result: unknown): void =>
			this.transport.send(encodeFrame({ jsonrpc: "2.0", id: message.id, result }));

		switch (message.method) {
			case "workspace/configuration": {
				const items = (message.params as { items?: unknown[] } | undefined)?.items ?? [];
				reply(items.map(() => null));
				return;
			}
			case "client/registerCapability":
			case "client/unregisterCapability":
			case "window/workDoneProgress/create":
			case "window/showMessageRequest":
				reply(null);
				return;
			case "workspace/applyEdit":
				// This subsystem is read-only; refusing is the honest answer.
				reply({ applied: false });
				return;
			default:
				this.transport.send(
					encodeFrame({
						jsonrpc: "2.0",
						id: message.id,
						error: { code: METHOD_NOT_FOUND, message: `unsupported request: ${message.method}` },
					}),
				);
		}
	}
}
