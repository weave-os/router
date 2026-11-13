/**
 * `lsp` — code intelligence for pi, which has none natively.
 *
 * One composite call replaces a multi-turn read/grep loop, which saves tokens
 * twice: fewer round trips, and less context growth before compaction.
 *
 * The same tool is registered in two roles. In the main process it drives a
 * pool of language servers directly. In a dispatch child it forwards to the
 * parent's pool over the broker socket, so a fan-out shares one warm server
 * instead of cold-starting one per child. `runLspOperation` is the single
 * orchestration core behind both, and is what the broker serves.
 */

import * as fs from "node:fs";
import * as path from "node:path";
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { StringEnum } from "@mariozechner/pi-ai";
import { Text } from "@mariozechner/pi-tui";
import { Type } from "typebox";
import {
	getLspPrefsPath,
	isSubagent,
	LSP_BROKER_ENV,
	LSP_BROKER_TOKEN_ENV,
	LSP_DIAGNOSTICS_WAIT_MS,
	LSP_IDLE_MS,
	LSP_MAX_REFERENCES,
	LSP_MAX_SERVERS,
	LSP_REQUEST_TIMEOUT_MS,
	LSP_WARMUP_TIMEOUT_MS,
} from "./config.js";
import { LspBrokerClient, startLspBroker, type LspBrokerHandle } from "./lsp-broker.js";
import {
	buildLspOffer,
	detectWorkspaceServers,
	installServer,
	loadDismissedLanguages,
	saveDismissedLanguages,
	type InstallResult,
} from "./lsp-install.js";
import type { LspClient } from "./lsp-client.js";
import {
	createLineReader,
	displayPath,
	formatDiagnostics,
	formatHover,
	formatLocations,
	formatSymbols,
	normalizeLocations,
	type LspDiagnostic,
} from "./lsp-format.js";
import {
	LSP_OPERATIONS,
	pathToUri,
	POSITION_OPERATIONS,
	toLspPosition,
	type LspOperation,
	type LspOperationParams,
} from "./lsp-protocol.js";
import {
	findWorkspaceRoot,
	languageIdFor,
	LSP_SERVERS,
	LspServerPool,
	missingServerText,
	resolveBinary,
	specForFile,
	specForLanguage,
	supportedExtensions,
	type WhichFn,
} from "./lsp-servers.js";

const LspParams = Type.Object({
	operation: StringEnum(LSP_OPERATIONS, {
		description: "definition, references and hover require line + column; documentSymbol and diagnostics take only path.",
	}),
	path: Type.String({ description: "File to query, absolute or relative to the working directory." }),
	line: Type.Optional(Type.Number({ description: "1-based line. Required for definition, references and hover." })),
	column: Type.Optional(
		Type.Number({ description: "1-based column in UTF-16 units, as editors count. Required for definition, references and hover." }),
	),
});

export interface LspRunDeps {
	pool: LspServerPool;
	which?: WhichFn;
	readFile?(target: string): string;
	exists?(target: string): boolean;
	maxReferences?: number;
	diagnosticsWaitMs?: number;
}

/** Models sometimes echo pi's `@file` mention syntax into tool arguments. */
export function normalizeToolPath(rawPath: string, cwd: string): string {
	const stripped = rawPath.trim().replace(/^@/, "");
	return path.isAbsolute(stripped) ? stripped : path.resolve(cwd, stripped);
}

/**
 * Per-operation requiredness. `Type.Union` would express this in the schema but
 * is banned here: it breaks Google's API, which is the same reason `StringEnum`
 * exists. A thrown message is what pi turns into an error the model can act on.
 */
export function assertOperationParams(params: LspOperationParams): void {
	if (!LSP_OPERATIONS.includes(params.operation)) {
		throw new Error(`Unknown lsp operation "${params.operation}". Valid operations: ${LSP_OPERATIONS.join(", ")}.`);
	}
	if (typeof params.path !== "string" || params.path.trim() === "") throw new Error("lsp requires a `path`.");
	if (!POSITION_OPERATIONS.has(params.operation)) return;
	const missing = [
		typeof params.line === "number" ? null : "line",
		typeof params.column === "number" ? null : "column",
	].filter(Boolean);
	if (missing.length > 0) {
		throw new Error(
			`lsp ${params.operation} requires \`${missing.join("` and `")}\` (1-based). Use documentSymbol first to locate the symbol if you do not know its position.`,
		);
	}
}

async function queryClient(
	client: LspClient,
	params: LspOperationParams,
	target: string,
	cwd: string,
	deps: LspRunDeps,
	generation: number,
	syncAction: "open" | "change" | "none",
	signal?: AbortSignal,
): Promise<string> {
	const uri = pathToUri(target);
	const shown = displayPath(target, cwd);
	const readLine = createLineReader(deps.readFile);
	const textDocument = { uri };

	switch (params.operation) {
		case "definition": {
			const position = toLspPosition(params.line as number, params.column as number);
			const result = await client.request("textDocument/definition", { textDocument, position }, { signal });
			const locations = normalizeLocations(result);
			if (locations.length === 0) return `No definition found at ${shown}:${params.line}:${params.column}`;
			return formatLocations(locations, { cwd, readLine, limit: locations.length });
		}
		case "references": {
			const position = toLspPosition(params.line as number, params.column as number);
			const result = await client.request(
				"textDocument/references",
				{ textDocument, position, context: { includeDeclaration: false } },
				{ signal },
			);
			const locations = normalizeLocations(result);
			if (locations.length === 0) return `No references found at ${shown}:${params.line}:${params.column}`;
			return formatLocations(locations, {
				cwd,
				readLine,
				limit: deps.maxReferences ?? LSP_MAX_REFERENCES,
				label: "references",
			});
		}
		case "hover": {
			const position = toLspPosition(params.line as number, params.column as number);
			const result = await client.request("textDocument/hover", { textDocument, position }, { signal });
			const rendered = formatHover(result);
			return rendered || `No hover information at ${shown}:${params.line}:${params.column}`;
		}
		case "documentSymbol": {
			const result = await client.request("textDocument/documentSymbol", { textDocument }, { signal });
			const rendered = formatSymbols(result, cwd);
			return rendered || `No symbols found in ${shown}`;
		}
		case "diagnostics": {
			// A no-op sync means the server already analyzed exactly this buffer, so
			// whatever it last published for the file IS current — demanding a newer
			// generation would burn the whole wait window and then mislabel a
			// perfectly fresh result as stale. Only a real didOpen/didChange (which
			// provokes a republish) requires a publish newer than the pre-sync mark.
			const sinceGeneration = syncAction === "none" ? 0 : generation;
			const { items, fresh } = await client.waitForDiagnostics(
				uri,
				sinceGeneration,
				deps.diagnosticsWaitMs ?? LSP_DIAGNOSTICS_WAIT_MS,
				signal,
			);
			const rendered = formatDiagnostics(items as LspDiagnostic[], target, cwd);
			if (!rendered) return `No diagnostics reported for ${shown}`;
			return fresh ? rendered : `${rendered}\n(may be stale — the language server did not publish in time)`;
		}
	}
}

/**
 * The single code path that answers every query, whoever asked: the main-loop
 * tool, and the broker on behalf of a subagent.
 *
 * Environmental dead ends (unknown file type, server not installed) return
 * text rather than throwing. The model cannot install gopls, and an error
 * invites a retry loop; text lets it fall back to grep.
 */
export async function runLspOperation(
	params: LspOperationParams,
	cwd: string,
	deps: LspRunDeps,
	signal?: AbortSignal,
): Promise<string> {
	assertOperationParams(params);
	const exists = deps.exists ?? fs.existsSync;
	const readFile = deps.readFile ?? ((target: string) => fs.readFileSync(target, "utf8"));

	const target = normalizeToolPath(params.path, cwd);
	if (!exists(target)) throw new Error(`File not found: ${params.path}`);

	const spec = specForFile(target);
	if (!spec) {
		const extension = path.extname(target) || "this file type";
		return `No language server is configured for ${extension}. Supported: ${supportedExtensions().join(", ")}. Use grep/read for this file instead.`;
	}
	const binary = resolveBinary(spec, deps.which);
	if (!binary) return missingServerText(spec);
	const root = findWorkspaceRoot(target, spec.rootMarkers, cwd);

	let lastError: Error | undefined;
	for (let attempt = 0; attempt < 2; attempt++) {
		const client = await deps.pool.acquire(spec, binary, root, signal);
		try {
			// Capture before syncing: an ensureDocument that provokes a publish must
			// count as newer than what we already had cached.
			const generation = client.diagnosticsGeneration();
			const syncAction = await client.ensureDocument(pathToUri(target), readFile(target), languageIdFor(spec, target));
			return await queryClient(client, params, target, cwd, deps, generation, syncAction, signal);
		} catch (error) {
			lastError = error as Error;
			// Only a died-under-us server earns a second spawn. Retrying a timeout or
			// a genuine LSP error would just double the wait.
			if (!client.dead || signal?.aborted) throw lastError;
		}
	}
	throw lastError ?? new Error("lsp request failed");
}

/** What dispatch needs to hand a child access to the parent's pool. */
export interface LspBrokerProvider {
	ensure(): Promise<Record<string, string>>;
	active(): boolean;
}

export interface LspToolDeps {
	pool?: LspServerPool;
	which?: WhichFn;
	brokerClient?: LspBrokerClient;
	startBroker?: typeof startLspBroker;
	prefsPath?: string;
	install?: typeof installServer;
	detect?: typeof detectWorkspaceServers;
}

const TOOL_DESCRIPTION = [
	"Code intelligence through a language server.",
	"definition: where a symbol is defined. references: every use of a symbol. hover: type signature and docs.",
	"documentSymbol: the outline of one file. diagnostics: compiler and type errors for one file.",
	`Supports ${supportedExtensions().join(", ")}, and needs that language's server on PATH (gopls, typescript-language-server, pyright, rust-analyzer).`,
	"Prefer this over grep for anything about symbols — it resolves through imports and types instead of matching text.",
	`References are capped at ${LSP_MAX_REFERENCES}. The first query in a workspace can take a while as the server indexes.`,
].join(" ");

const TOOL_PROMPT_SNIPPET = "Language-server code intelligence: definition, references, hover, documentSymbol, diagnostics (Go, TS/JS, Python, Rust)";

// The description alone does not change tool choice: models reach for text
// search on symbol questions by habit. These land in the system prompt's
// Guidelines section, which is what actually steers the pick.
const TOOL_PROMPT_GUIDELINES = [
	"Use lsp instead of grep for symbol questions — where a symbol is defined or used, its type or signature, a file's structure, or compile errors. Text search matches strings; lsp resolves through imports and types.",
	"To find every usage of a symbol: locate its declaration first (grep or lsp documentSymbol), then call lsp references at that exact line and column — a text match list is not a references answer.",
];

const EXIT_SWEEP_KEY = Symbol.for("weave.pi.lsp.exitSweep");

interface ExitSweepState {
	sweep(): void;
}

/**
 * pi loads extensions with moduleCache:false, so `/reload` re-executes this
 * module. Without the symbol guard every reload would add another exit
 * listener; the guard instead re-points the one listener at the newest pool.
 */
function installExitSweep(sweep: () => void): void {
	const store = globalThis as unknown as Record<symbol, ExitSweepState | undefined>;
	const existing = store[EXIT_SWEEP_KEY];
	if (existing) {
		existing.sweep = sweep;
		return;
	}
	const state: ExitSweepState = { sweep };
	store[EXIT_SWEEP_KEY] = state;
	process.on("exit", () => state.sweep());
}

function toolResult(text: string, params: LspOperationParams) {
	return {
		content: [{ type: "text" as const, text }],
		details: { operation: params.operation, path: params.path },
	};
}

function renderLspCall(args: Partial<LspOperationParams>, theme: { fg(key: string, text: string): string; bold(text: string): string }): Text {
	const position = typeof args.line === "number" ? `:${args.line}${typeof args.column === "number" ? `:${args.column}` : ""}` : "";
	const target = `${args.path ?? ""}${position}`;
	return new Text(
		`${theme.fg("toolTitle", theme.bold("lsp "))}${theme.fg("accent", args.operation ?? "")}${target ? ` ${theme.fg("dim", target)}` : ""}`,
		0,
		0,
	);
}

/**
 * Registers the `lsp` tool for this process's role and, in the main process,
 * returns the broker provider for index.ts to hand to dispatch. dispatch never
 * imports this module — composition stays in index.ts.
 */
export function registerLsp(pi: ExtensionAPI, deps: LspToolDeps = {}): LspBrokerProvider | undefined {
	if (isSubagent()) {
		registerBrokerBackedTool(pi, deps);
		return undefined;
	}
	return registerPoolBackedTool(pi, deps);
}

function registerBrokerBackedTool(pi: ExtensionAPI, deps: LspToolDeps): void {
	const socketPath = process.env[LSP_BROKER_ENV];
	const token = process.env[LSP_BROKER_TOKEN_ENV];
	// A child spawned without a broker simply has no lsp tool, rather than one
	// that always fails.
	if (!deps.brokerClient && (!socketPath || !token)) return;

	const client =
		deps.brokerClient ??
		new LspBrokerClient(socketPath as string, token as string, LSP_WARMUP_TIMEOUT_MS + LSP_REQUEST_TIMEOUT_MS);

	pi.registerTool({
		name: "lsp",
		label: "LSP",
		description: TOOL_DESCRIPTION,
		promptSnippet: TOOL_PROMPT_SNIPPET,
		promptGuidelines: TOOL_PROMPT_GUIDELINES,
		parameters: LspParams,
		executionMode: "parallel",

		async execute(_toolCallId, params, signal, _onUpdate, ctx) {
			assertOperationParams(params as LspOperationParams);
			// The parent may be cold-starting a server on our behalf, so the child
			// sends its own cwd and lets the parent resolve paths against it.
			const text = await client.execute(
				{ ...(params as LspOperationParams), path: normalizeToolPath(params.path, ctx.cwd) },
				ctx.cwd,
				signal,
			);
			return toolResult(text, params as LspOperationParams);
		},

		renderCall: renderLspCall,
	});

	pi.on("session_shutdown", () => client.close());
}

function registerPoolBackedTool(pi: ExtensionAPI, deps: LspToolDeps): LspBrokerProvider {
	const pool =
		deps.pool ??
		new LspServerPool({
			maxServers: LSP_MAX_SERVERS,
			idleMs: LSP_IDLE_MS,
			requestTimeoutMs: LSP_REQUEST_TIMEOUT_MS,
			warmupTimeoutMs: LSP_WARMUP_TIMEOUT_MS,
		});
	const runDeps: LspRunDeps = { pool, which: deps.which };

	pi.registerTool({
		name: "lsp",
		label: "LSP",
		description: TOOL_DESCRIPTION,
		promptSnippet: TOOL_PROMPT_SNIPPET,
		promptGuidelines: TOOL_PROMPT_GUIDELINES,
		parameters: LspParams,
		executionMode: "parallel",

		async execute(_toolCallId, params, signal, _onUpdate, ctx) {
			const text = await runLspOperation(params as LspOperationParams, ctx.cwd, runDeps, signal);
			return toolResult(text, params as LspOperationParams);
		},

		renderCall: renderLspCall,
	});

	registerLspEnable(pi, deps);

	let broker: LspBrokerHandle | undefined;
	let starting: Promise<LspBrokerHandle> | undefined;
	const start = deps.startBroker ?? startLspBroker;

	const provider: LspBrokerProvider = {
		active: () => broker !== undefined,
		async ensure(): Promise<Record<string, string>> {
			if (!starting) {
				starting = start((params, cwd, signal) => runLspOperation(params, cwd, runDeps, signal)).then((handle) => {
					broker = handle;
					return handle;
				});
				starting.catch(() => {
					// A broker that will not listen must not wedge every later fan-out.
					starting = undefined;
				});
			}
			const handle = await starting;
			return { [LSP_BROKER_ENV]: handle.socketPath, [LSP_BROKER_TOKEN_ENV]: handle.token };
		},
	};

	pi.on("session_shutdown", async () => {
		const handle = broker;
		broker = undefined;
		starting = undefined;
		// Close the socket before the servers so no child request arrives at a
		// pool that is already tearing down.
		await handle?.close();
		await pool.shutdownAll();
	});

	installExitSweep(() => {
		pool.killAllSync();
		broker?.removeSocket();
	});
	return provider;
}

const LspEnableParams = Type.Object({
	language: StringEnum(LSP_SERVERS.map((spec) => spec.language) as ["go", "typescript", "python", "rust"], {
		description: "Language whose server to install or dismiss.",
	}),
	action: Type.Optional(
		StringEnum(["install", "dismiss"] as const, {
			description: 'Default "install". "dismiss" records that the user does not want this language server offered again.',
		}),
	),
});

/**
 * The consent-gated half of provisioning, registered in the main process only
 * — subagents never get it, so a fan-out cannot trigger installs. A
 * before_agent_start addendum tells the assistant which detected languages
 * lack a server so it can offer once; this tool is how a user's "yes" (or
 * "stop asking") takes effect.
 */
function registerLspEnable(pi: ExtensionAPI, deps: LspToolDeps): void {
	const prefsPath = deps.prefsPath ?? getLspPrefsPath();
	const install = deps.install ?? installServer;
	const detect = deps.detect ?? detectWorkspaceServers;

	// Lazily built once per session; undefined once resolved (installed,
	// dismissed, or nothing to offer) so the addendum disappears from later turns.
	let offer: string | undefined;
	let offerComputed = false;
	const resetOffer = (): void => {
		offer = undefined;
		offerComputed = false;
	};
	pi.on("session_start", resetOffer);

	pi.on("before_agent_start", (event: { systemPrompt: string }, ctx: { cwd: string }) => {
		if (!offerComputed) {
			offerComputed = true;
			const missing = detect(ctx.cwd).filter((spec) => !resolveBinary(spec, deps.which));
			offer = buildLspOffer(missing, loadDismissedLanguages(prefsPath));
		}
		if (!offer) return undefined;
		return { systemPrompt: `${event.systemPrompt}\n\n${offer}` };
	});

	pi.registerTool({
		name: "lsp_enable",
		label: "LSP enable",
		description: [
			"Install a language server so the lsp tool works for that language, or record the user's wish not to be offered it again.",
			"Only call this after the user has explicitly agreed (install) or declined (dismiss) in conversation — never preemptively.",
		].join(" "),
		parameters: LspEnableParams,

		async execute(_toolCallId, params, signal) {
			const spec = specForLanguage(params.language);
			if (!spec) throw new Error(`Unknown language "${params.language}".`);

			if (params.action === "dismiss") {
				const dismissed = loadDismissedLanguages(prefsPath);
				dismissed.add(spec.language);
				saveDismissedLanguages(prefsPath, dismissed);
				offer = undefined;
				return {
					content: [
						{
							type: "text" as const,
							text: `Noted — the ${spec.language} language server will not be offered again. The user can re-enable it any time by asking to enable ${spec.language} LSP support.`,
						},
					],
					details: { language: spec.language, action: "dismiss", ok: true },
				};
			}

			const result: InstallResult = await install(spec, { which: deps.which }, signal);
			if (result.ok) {
				offer = undefined;
				// An earlier dismissal is void once the user asks for the install.
				const dismissed = loadDismissedLanguages(prefsPath);
				if (dismissed.delete(spec.language)) saveDismissedLanguages(prefsPath, dismissed);
			}
			return {
				content: [{ type: "text" as const, text: result.text }],
				details: { language: spec.language, action: "install", ok: result.ok },
				isError: !result.ok,
			};
		},

		renderCall(args, theme) {
			return new Text(
				`${theme.fg("toolTitle", theme.bold("lsp_enable "))}${theme.fg("accent", args.language ?? "")}${args.action === "dismiss" ? theme.fg("dim", " dismiss") : ""}`,
				0,
				0,
			);
		},
	});
}
