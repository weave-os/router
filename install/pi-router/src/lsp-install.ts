/**
 * Opt-in language-server provisioning.
 *
 * Nothing here runs uninvited: the assistant is told (via a system-prompt
 * addendum) which detected languages lack a server, offers ONCE in
 * conversation ("I can enable Go LSP support if you'd like — just say the
 * word!"), and only a user "yes" relayed through the lsp_enable tool triggers
 * an install. A "no, stop asking" is persisted per language and respected
 * across sessions.
 */

import { spawn as nodeSpawn } from "node:child_process";
import * as fs from "node:fs";
import type { SpawnFn } from "./lsp-client.js";
import {
	defaultWhich,
	detectMarkers,
	installCommandText,
	LSP_SERVERS,
	resolveBinary,
	type LanguageServerSpec,
	type WhichFn,
} from "./lsp-servers.js";

const INSTALL_TIMEOUT_MS = 300_000;
const INSTALL_KILL_GRACE_MS = 5000;
const OUTPUT_TAIL_BYTES = 2048;
const MAX_SCAN_ENTRIES = 50;
const SKIPPED_SCAN_DIRS = new Set(["node_modules", "vendor", "dist", "build", "target"]);

export interface DetectDeps {
	exists?(target: string): boolean;
	readDir?(dir: string): string[];
	isDir?(target: string): boolean;
}

/**
 * Which registry languages are present in a workspace: positive markers at the
 * root or one level down (enough for common monorepo layouts, cheap enough to
 * run at session start).
 */
export function detectWorkspaceServers(cwd: string, deps: DetectDeps = {}): LanguageServerSpec[] {
	const exists = deps.exists ?? fs.existsSync;
	const isDir = deps.isDir ?? ((target: string) => {
		try {
			return fs.statSync(target).isDirectory();
		} catch {
			return false;
		}
	});
	const readDir = deps.readDir ?? ((dir: string) => {
		try {
			return fs.readdirSync(dir);
		} catch {
			return [];
		}
	});

	const roots = [cwd];
	for (const entry of readDir(cwd).slice(0, MAX_SCAN_ENTRIES)) {
		if (entry.startsWith(".") || SKIPPED_SCAN_DIRS.has(entry)) continue;
		const child = `${cwd}/${entry}`;
		if (isDir(child)) roots.push(child);
	}

	return LSP_SERVERS.filter((spec) =>
		roots.some((root) => detectMarkers(spec).some((marker) => exists(`${root}/${marker}`))),
	);
}

interface LspPrefs {
	dismissed: string[];
}

/** Tolerant read: a missing or corrupt prefs file means "nothing dismissed", never an error. */
export function loadDismissedLanguages(prefsPath: string, readFile: (p: string) => string = (p) => fs.readFileSync(p, "utf8")): Set<string> {
	try {
		const parsed = JSON.parse(readFile(prefsPath)) as Partial<LspPrefs>;
		return new Set(Array.isArray(parsed.dismissed) ? parsed.dismissed.filter((entry) => typeof entry === "string") : []);
	} catch {
		return new Set();
	}
}

export function saveDismissedLanguages(
	prefsPath: string,
	dismissed: Set<string>,
	writeFile: (p: string, data: string) => void = (p, data) => fs.writeFileSync(p, data),
): void {
	writeFile(prefsPath, `${JSON.stringify({ dismissed: [...dismissed].sort() }, null, 2)}\n`);
}

/**
 * The system-prompt addendum that turns detection into a conversational offer.
 * Returns undefined when there is nothing to offer (nothing detected missing,
 * or everything relevant was dismissed).
 */
export function buildLspOffer(missing: LanguageServerSpec[], dismissed: Set<string>): string | undefined {
	const offerable = missing.filter((spec) => !dismissed.has(spec.language));
	if (offerable.length === 0) return undefined;
	const languages = offerable.map((spec) => spec.language).join(", ");
	return [
		"## Code intelligence (LSP)",
		`This workspace contains ${languages} code, but the matching language server(s) are not installed, so the \`lsp\` tool cannot serve those files yet.`,
		`At a natural moment, offer ONCE — briefly, in your own words (e.g. "I can enable ${offerable[0].language} LSP support if you'd like — just say the word!") — to enable it.`,
		'If the user agrees, call lsp_enable with {"language": "<language>"} for each language they want.',
		'If they decline or ask not to be asked again, call lsp_enable with {"language": "<language>", "action": "dismiss"} and drop the subject.',
		"Never call lsp_enable without the user's explicit go-ahead, and do not repeat the offer in this session.",
	].join("\n");
}

export interface InstallResult {
	ok: boolean;
	text: string;
}

export interface InstallDeps {
	which?: WhichFn;
	spawnFn?: SpawnFn;
	timeoutMs?: number;
	killGraceMs?: number;
}

/** Run the spec's install command. Only ever called with the user's explicit consent. */
export async function installServer(spec: LanguageServerSpec, deps: InstallDeps = {}, signal?: AbortSignal): Promise<InstallResult> {
	const which = deps.which ?? defaultWhich;
	const commandText = installCommandText(spec);

	if (resolveBinary(spec, which)) {
		return { ok: true, text: `The ${spec.language} language server is already installed — the lsp tool is ready for ${spec.language} files.` };
	}
	if (!which(spec.install.requires)) {
		return {
			ok: false,
			text: `Cannot install the ${spec.language} language server: it needs \`${spec.install.requires}\` on PATH (the install command is \`${commandText}\`). The install-lsps skill covers installing the ${spec.install.requires} toolchain with the user's consent.`,
		};
	}

	const [command, ...args] = spec.install.command;
	const spawnFn = deps.spawnFn ?? nodeSpawn;
	const timeoutMs = deps.timeoutMs ?? INSTALL_TIMEOUT_MS;
	const killGraceMs = deps.killGraceMs ?? INSTALL_KILL_GRACE_MS;

	const outcome = await new Promise<{ code: number | null; output: string; cancelled?: "aborted" | "timed out" }>((resolve) => {
		const child = spawnFn(command, args, { shell: false, stdio: ["ignore", "pipe", "pipe"] });
		let tail = "";
		let settled = false;
		let cancelled: "aborted" | "timed out" | undefined;
		let killTimer: NodeJS.Timeout | undefined;
		const settle = (code: number | null): void => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			if (killTimer) clearTimeout(killTimer);
			signal?.removeEventListener("abort", onAbort);
			resolve({ code, output: tail, cancelled });
		};
		const append = (chunk: Buffer): void => {
			tail = (tail + chunk.toString("utf8")).slice(-OUTPUT_TAIL_BYTES);
		};
		// Cancellation must not settle before the child actually exits — an
		// installer that shrugs off SIGTERM would keep mutating the system after
		// we reported failure. Settle on close, escalating to SIGKILL if needed.
		const cancel = (reason: "aborted" | "timed out"): void => {
			if (settled || cancelled) return;
			cancelled = reason;
			child.kill("SIGTERM");
			killTimer = setTimeout(() => {
				if (!settled) child.kill("SIGKILL");
			}, killGraceMs);
			killTimer.unref?.();
		};
		const onAbort = (): void => cancel("aborted");
		const timer = setTimeout(() => cancel("timed out"), timeoutMs);
		timer.unref?.();

		child.stdout?.on("data", append);
		child.stderr?.on("data", append);
		child.on("close", (code) => settle(code));
		child.on("error", (error: Error) => {
			append(Buffer.from(error.message));
			settle(null);
		});
		if (signal) {
			if (signal.aborted) onAbort();
			else signal.addEventListener("abort", onAbort, { once: true });
		}
	});

	const lastOutputLine =
		outcome.output
			.split("\n")
			.map((line) => line.trim())
			.filter(Boolean)
			.pop() ?? "";
	if (outcome.cancelled) {
		return {
			ok: false,
			text: `Installing the ${spec.language} language server ${outcome.cancelled} (\`${commandText}\`); the installer process was stopped. The install may be partial — re-run \`${commandText}\` to complete it.`,
		};
	}
	if (outcome.code !== 0) {
		return {
			ok: false,
			text: `Installing the ${spec.language} language server failed (\`${commandText}\`${outcome.code === null ? "" : `, exit ${outcome.code}`})${lastOutputLine ? `: ${lastOutputLine}` : ""}`,
		};
	}
	if (!resolveBinary(spec, which)) {
		return {
			ok: false,
			text: `\`${commandText}\` succeeded but its binary is still not resolvable — the install location is probably not on PATH. Ask the user to add it and restart pi.`,
		};
	}
	return { ok: true, text: `Installed the ${spec.language} language server — the lsp tool now works for ${spec.language} files in this session.` };
}
