import { randomUUID } from "node:crypto";
import { VERSION, type ExtensionAPI, type ExtensionContext } from "@mariozechner/pi-coding-agent";
import { getRole, getRouterBaseUrl, PROVIDER_NAME, providerHeaders, resolveRouterKey } from "./config.js";

const ENTRY_TYPE = "weave-classifier-thread";
const THREAD_FIELD = "weave_classifier_thread";

interface ClassifierThread {
	sessionId: string;
	routerOrigin: string;
	newChatId: string;
	threadToken?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function notify(ctx: ExtensionContext, message: string) {
	if (ctx.hasUI) ctx.ui.notify(message, "error");
	else process.stderr.write(`${message}\n`);
}

/** Enroll only explicit new sessions; a persisted attempt cannot silently become legacy traffic. */
export function registerClassifierThread(pi: ExtensionAPI, request: typeof fetch = fetch, version = VERSION): () => boolean {
	let thread: ClassifierThread | undefined;
	let pending: Promise<void> | undefined;
	let generation = 0;
	const [major, minor] = version.split(".").map(Number);
	const supported = major > 0 || (major === 0 && minor >= 83);

	const restore = (ctx: ExtensionContext) => {
		generation++;
		pending = undefined;
		thread = undefined;
		// All entries, not just the active branch: tree navigation must not erase enrollment.
		for (const entry of ctx.sessionManager.getEntries()) {
			if (entry.type !== "custom" || entry.customType !== ENTRY_TYPE || !isRecord(entry.data)) continue;
			const saved = entry.data;
			if (saved.sessionId !== ctx.sessionManager.getSessionId()) continue;
			if (typeof saved.routerOrigin !== "string" || typeof saved.newChatId !== "string" ||
				(saved.threadToken !== undefined && typeof saved.threadToken !== "string")) {
				thread = { sessionId: ctx.sessionManager.getSessionId(), routerOrigin: "", newChatId: "" };
				return;
			}
			thread = { sessionId: saved.sessionId, routerOrigin: saved.routerOrigin, newChatId: saved.newChatId, threadToken: saved.threadToken };
		}
	};

	const ensureTicket = async (ctx: ExtensionContext) => {
		if (!thread) return;
		if (thread.sessionId !== ctx.sessionManager.getSessionId() || thread.routerOrigin !== getRouterBaseUrl()) {
			throw new Error("Classifier session or router changed; start a new chat");
		}
		if (thread.threadToken) return;
		if (pending) return pending;
		const enrollment = thread;
		const requestGeneration = generation;
		pending = (async () => {
			const routerKey = resolveRouterKey();
			if (!routerKey) throw new Error("Router key is missing");
			const signals = [AbortSignal.timeout(10_000)];
			if (ctx.signal) signals.push(ctx.signal);
			const response = await request(`${enrollment.routerOrigin}/v1/router/threads`, {
				method: "POST", redirect: "error",
				headers: { ...providerHeaders(getRole(), routerKey), "content-type": "application/json" },
				body: JSON.stringify({ new_chat_id: enrollment.newChatId }), signal: AbortSignal.any(signals),
			});
			if (!response.ok) throw new Error(`Classifier enrollment returned HTTP ${response.status}`);
			const ticket: unknown = await response.json();
			if (!isRecord(ticket) || typeof ticket.thread_token !== "string" || !ticket.thread_token || ticket.thread_token.length > 4096) {
				throw new Error("Invalid classifier enrollment response");
			}
			if (generation !== requestGeneration || ctx.signal?.aborted) throw new Error("Session changed during classifier enrollment");
			enrollment.threadToken = ticket.thread_token;
			pi.appendEntry(ENTRY_TYPE, { ...enrollment });
		})();
		try { await pending; } finally { if (generation === requestGeneration) pending = undefined; }
	};

	pi.on("session_start", async (event, ctx) => {
		restore(ctx);
		if (!thread && process.env.WEAVE_PI_LLM_CLASSIFIER === "1" && supported &&
			ctx.model?.provider === PROVIDER_NAME && (event.reason === "new" || event.reason === "startup") &&
			!ctx.sessionManager.getHeader()?.parentSession &&
			!ctx.sessionManager.getEntries().some(entry => entry.type === "message" || entry.type === "compaction" || entry.type === "branch_summary")) {
			thread = { sessionId: ctx.sessionManager.getSessionId(), routerOrigin: getRouterBaseUrl(), newChatId: randomUUID() };
			// Persist before I/O so a crash/retry cannot create another server thread.
			pi.appendEntry(ENTRY_TYPE, { ...thread });
		}
		try { await ensureTicket(ctx); } catch {
			notify(ctx, "Classifier enrollment failed. The next request will retry the same enrollment; it will not switch strategies.");
		}
	});
	pi.on("session_tree", (_event, ctx) => restore(ctx));
	pi.on("session_shutdown", () => { generation++; pending = undefined; thread = undefined; });
	pi.on("before_provider_request", async (event, ctx) => {
		if (!thread) return;
		const requestGeneration = generation;
		try {
			if (!supported || ctx.model?.provider !== PROVIDER_NAME || !isRecord(event.payload)) {
				throw new Error("Classifier thread requires the supported Weave provider");
			}
			await ensureTicket(ctx);
			if (generation !== requestGeneration || !thread?.threadToken || ctx.signal?.aborted) throw new Error("Classifier thread changed");
			return { ...event.payload, [THREAD_FIELD]: thread.threadToken };
		} catch {
			// Pi catches extension exceptions; only abort prevents an unticketed dispatch.
			ctx.abort();
			notify(ctx, "Classifier request stopped: enrollment or session identity is unavailable. No fallback request was sent.");
		}
	});
	pi.on("session_before_compact", (_event, ctx) => {
		if (!thread) return;
		notify(ctx, "Classifier chats require complete history. Compaction is unavailable; start a new chat.");
		return { cancel: true };
	});
	return () => thread !== undefined;
}
