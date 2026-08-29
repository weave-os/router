/**
 * Which language server serves which file, and the pool of live ones.
 *
 * The registry is a data table rather than a class hierarchy: adding a language
 * is one row, and every behavioral difference between servers is already
 * expressible as data (binary, root markers, install hint).
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { abortable, LspClient, spawnTransport, type LspClientOptions } from "./lsp-client.js";

export interface ServerBinary {
	command: string;
	args: string[];
}

export interface ServerInstall {
	/** Toolchain binary that must already exist for the install to be possible. */
	requires: string;
	command: string[];
}

export interface LanguageServerSpec {
	id: string;
	/** Model/user-facing language name ("go"), distinct from the server id ("gopls"). */
	language: string;
	/** Lowercased file extension (with dot) to LSP languageId. */
	languages: Record<string, string>;
	/** Tried in order; the first one on PATH wins. */
	binaries: ServerBinary[];
	rootMarkers: string[];
	install: ServerInstall;
	/**
	 * Where the install command drops its binary when that directory is not on
	 * PATH ("~" expands to the home dir). A function of the environment because
	 * the toolchains honor overrides (GOBIN / GOPATH / CARGO_HOME) — hardcoding
	 * the defaults would lose a successful install to a non-default location.
	 */
	fallbackDirs(env: NodeJS.ProcessEnv): string[];
}

/** `go install` target: $GOBIN, else <first GOPATH element>/bin, else ~/go/bin. */
export function goBinDirs(env: NodeJS.ProcessEnv): string[] {
	const dirs: string[] = [];
	if (env.GOBIN?.trim()) dirs.push(env.GOBIN.trim());
	const gopathFirst = env.GOPATH?.split(path.delimiter)[0]?.trim();
	dirs.push(gopathFirst ? path.join(gopathFirst, "bin") : "~/go/bin");
	return dirs;
}

/** rustup/cargo bin dir: $CARGO_HOME/bin, else ~/.cargo/bin. */
export function cargoBinDirs(env: NodeJS.ProcessEnv): string[] {
	const cargoHome = env.CARGO_HOME?.trim();
	return [cargoHome ? path.join(cargoHome, "bin") : "~/.cargo/bin"];
}

export const LSP_SERVERS: LanguageServerSpec[] = [
	{
		id: "gopls",
		language: "go",
		languages: { ".go": "go" },
		binaries: [{ command: "gopls", args: ["serve"] }],
		rootMarkers: ["go.work", "go.mod"],
		install: { requires: "go", command: ["go", "install", "golang.org/x/tools/gopls@latest"] },
		fallbackDirs: goBinDirs,
	},
	{
		id: "typescript",
		language: "typescript",
		languages: {
			".ts": "typescript",
			".tsx": "typescriptreact",
			".mts": "typescript",
			".cts": "typescript",
			".js": "javascript",
			".jsx": "javascriptreact",
			".mjs": "javascript",
			".cjs": "javascript",
		},
		binaries: [{ command: "typescript-language-server", args: ["--stdio"] }],
		rootMarkers: ["tsconfig.json", "jsconfig.json", "package.json", ".git"],
		install: { requires: "npm", command: ["npm", "i", "-g", "typescript-language-server", "typescript"] },
		fallbackDirs: () => [],
	},
	{
		id: "pyright",
		language: "python",
		languages: { ".py": "python", ".pyi": "python" },
		binaries: [
			{ command: "pyright-langserver", args: ["--stdio"] },
			{ command: "basedpyright-langserver", args: ["--stdio"] },
		],
		rootMarkers: ["pyrightconfig.json", "pyproject.toml", "setup.py", "requirements.txt", ".git"],
		install: { requires: "npm", command: ["npm", "i", "-g", "pyright"] },
		fallbackDirs: () => [],
	},
	{
		id: "rust-analyzer",
		language: "rust",
		languages: { ".rs": "rust" },
		binaries: [{ command: "rust-analyzer", args: [] }],
		rootMarkers: ["Cargo.toml"],
		install: { requires: "rustup", command: ["rustup", "component", "add", "rust-analyzer"] },
		fallbackDirs: cargoBinDirs,
	},
];

export function specForLanguage(language: string): LanguageServerSpec | undefined {
	return LSP_SERVERS.find((spec) => spec.language === language.toLowerCase());
}

export function installCommandText(spec: LanguageServerSpec): string {
	return spec.install.command.join(" ");
}

/**
 * Markers that positively indicate the language is present in a directory.
 * `.git` is a root-walk fallback, not evidence of any language.
 */
export function detectMarkers(spec: LanguageServerSpec): string[] {
	return spec.rootMarkers.filter((marker) => marker !== ".git");
}

export function specForFile(filePath: string): LanguageServerSpec | undefined {
	const extension = path.extname(filePath).toLowerCase();
	if (!extension) return undefined;
	return LSP_SERVERS.find((spec) => extension in spec.languages);
}

export function languageIdFor(spec: LanguageServerSpec, filePath: string): string {
	return spec.languages[path.extname(filePath).toLowerCase()] ?? "plaintext";
}

export function supportedExtensions(): string[] {
	return LSP_SERVERS.flatMap((spec) => Object.keys(spec.languages)).sort();
}

export type ExistsFn = (target: string) => boolean;

/** Nearest ancestor holding a marker wins; `fallbackCwd` when the file sits outside any project. */
export function findWorkspaceRoot(filePath: string, markers: string[], fallbackCwd: string, exists: ExistsFn = fs.existsSync): string {
	let current = path.dirname(path.resolve(filePath));
	while (true) {
		for (const marker of markers) {
			if (exists(path.join(current, marker))) return current;
		}
		const parent = path.dirname(current);
		if (parent === current) return fallbackCwd;
		current = parent;
	}
}

export type WhichFn = (command: string) => string | undefined;

function isExecutable(candidate: string): boolean {
	try {
		if (!fs.statSync(candidate).isFile()) return false;
		// Windows has no X_OK bit; presence on PATH with a known extension is the test.
		if (process.platform !== "win32") fs.accessSync(candidate, fs.constants.X_OK);
		return true;
	} catch {
		return false;
	}
}

/** Hand-rolled PATH scan — the package ships raw TS with peer deps only, so no `which` dependency. */
export function defaultWhich(command: string): string | undefined {
	if (command.includes("/") || command.includes(path.sep)) return isExecutable(command) ? command : undefined;
	const extensions = process.platform === "win32" ? ["", ".cmd", ".exe", ".bat"] : [""];
	for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
		if (!dir) continue;
		for (const extension of extensions) {
			const candidate = path.join(dir, command + extension);
			if (isExecutable(candidate)) return candidate;
		}
	}
	return undefined;
}

function expandHome(dir: string): string {
	return dir.startsWith("~/") ? path.join(os.homedir(), dir.slice(2)) : dir;
}

export function resolveBinary(
	spec: LanguageServerSpec,
	which: WhichFn = defaultWhich,
	env: NodeJS.ProcessEnv = process.env,
): ServerBinary | undefined {
	for (const binary of spec.binaries) {
		// Fallback candidates are absolute, which defaultWhich checks directly;
		// routing them through `which` keeps a single injectable seam.
		const candidates = [binary.command, ...spec.fallbackDirs(env).map((dir) => path.join(expandHome(dir), binary.command))];
		for (const candidate of candidates) {
			const resolved = which(candidate);
			if (resolved) return { command: resolved, args: binary.args };
		}
	}
	return undefined;
}

export function missingServerText(spec: LanguageServerSpec): string {
	const names = spec.binaries.map((binary) => binary.command).join(" or ");
	return [
		`No language server for this file type: ${names} is not on PATH.`,
		`The user can enable it (install: ${installCommandText(spec)}), or ask the assistant to via the lsp_enable tool.`,
		"Until then, use grep/read for this file instead.",
	].join("\n");
}

export interface PoolOptions extends LspClientOptions {
	maxServers: number;
	idleMs: number;
}

export interface PoolDeps {
	createClient?(spec: LanguageServerSpec, binary: ServerBinary, root: string, options: LspClientOptions): LspClient;
	now?(): number;
}

interface PoolEntry {
	client: LspClient;
	ready: Promise<LspClient>;
	lastUsed: number;
	/** False until initialize succeeds — pending entries are exempt from idle and LRU eviction. */
	initialized: boolean;
	idleTimer?: NodeJS.Timeout;
}

function defaultCreateClient(
	_spec: LanguageServerSpec,
	binary: ServerBinary,
	root: string,
	options: LspClientOptions,
): LspClient {
	return new LspClient(root, spawnTransport(binary.command, binary.args, root), options);
}

/**
 * Live servers keyed by workspace root + language. Servers are expensive to
 * start (gopls indexes a module) and cheap to keep, so they are spawned lazily,
 * shared by every caller including broker-connected subagents, and reclaimed on
 * idle or LRU pressure.
 */
export class LspServerPool {
	private readonly entries = new Map<string, PoolEntry>();
	private readonly createClient: NonNullable<PoolDeps["createClient"]>;
	private readonly now: () => number;

	constructor(
		private readonly options: PoolOptions,
		deps: PoolDeps = {},
	) {
		this.createClient = deps.createClient ?? defaultCreateClient;
		this.now = deps.now ?? Date.now;
	}

	get size(): number {
		return this.entries.size;
	}

	async acquire(spec: LanguageServerSpec, binary: ServerBinary, root: string, signal?: AbortSignal): Promise<LspClient> {
		const key = `${root} ${spec.id}`;
		const existing = this.entries.get(key);
		if (existing && existing.client.dead) {
			this.forget(key, existing);
			existing.client.killNow();
		}

		let entry = this.entries.get(key);
		if (!entry) {
			const client = this.createClient(spec, binary, root, this.options);
			const created: PoolEntry = { client, ready: Promise.resolve(client), lastUsed: this.now(), initialized: false };
			created.ready = client.initialize().then(() => {
				// Only now does the entry become evictable: an idle timer or LRU pass
				// during the (up to warmup-length) handshake would dispose a client
				// its original caller is still awaiting.
				created.initialized = true;
				if (this.entries.get(key) === created) this.armIdleTimer(key, created);
				return client;
			});
			this.entries.set(key, created);
			// A failed handshake must not stay cached as a poisoned promise.
			created.ready.catch(() => {
				if (this.entries.get(key) === created) this.forget(key, created);
				client.killNow();
			});
			entry = created;
		}

		entry.lastUsed = this.now();
		if (entry.initialized) this.armIdleTimer(key, entry);
		this.evictOverCap(key);
		return abortable(entry.ready, signal);
	}

	async shutdownAll(): Promise<void> {
		const entries = [...this.entries.entries()];
		this.entries.clear();
		await Promise.all(
			entries.map(async ([, entry]) => {
				if (entry.idleTimer) clearTimeout(entry.idleTimer);
				try {
					await entry.client.dispose();
				} catch {
					/* one stuck server must not block the rest of shutdown */
				}
			}),
		);
	}

	/** Emergency sweep for `process.on("exit")`, where nothing async can run. */
	killAllSync(): void {
		for (const [, entry] of this.entries) {
			if (entry.idleTimer) clearTimeout(entry.idleTimer);
			entry.client.killNow();
		}
		this.entries.clear();
	}

	private forget(key: string, entry: PoolEntry): void {
		if (entry.idleTimer) clearTimeout(entry.idleTimer);
		if (this.entries.get(key) === entry) this.entries.delete(key);
	}

	private armIdleTimer(key: string, entry: PoolEntry): void {
		if (entry.idleTimer) clearTimeout(entry.idleTimer);
		entry.idleTimer = setTimeout(() => {
			// A slow request (or diagnostics wait) can outlive a short idle window;
			// "idle" means no in-flight work, not merely no recent acquire.
			if (entry.client.busy && !entry.client.dead) {
				this.armIdleTimer(key, entry);
				return;
			}
			this.forget(key, entry);
			void entry.client.dispose();
		}, this.options.idleMs);
		entry.idleTimer.unref?.();
	}

	private evictOverCap(keepKey: string): void {
		while (this.entries.size > Math.max(1, this.options.maxServers)) {
			let oldestKey: string | undefined;
			let oldestEntry: PoolEntry | undefined;
			for (const [key, entry] of this.entries) {
				if (key === keepKey) continue;
				// Still initializing = someone is awaiting it. The cap is soft during
				// warmup; the overflow is reclaimed on the next settled acquire.
				if (!entry.initialized) continue;
				if (!oldestEntry || entry.lastUsed < oldestEntry.lastUsed) {
					oldestKey = key;
					oldestEntry = entry;
				}
			}
			if (!oldestKey || !oldestEntry) return;
			this.forget(oldestKey, oldestEntry);
			void oldestEntry.client.dispose();
		}
	}
}
