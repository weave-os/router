import { compact, VERSION, type ExtensionAPI, type ExtensionContext } from "@mariozechner/pi-coding-agent";
import { streamSimple } from "@mariozechner/pi-ai";
import { getRole, getRouterBaseUrl, isSubagent, PROVIDER_NAME, providerHeaders, resolveRouterKey, ROUTED_MODEL_HEADER } from "./config.js";
import { ROUTED_COMPACTION_CONTINUATION } from "./compaction.js";

type Complexity = "low" | "medium" | "high" | "maximum";
const COMPLEXITY_RANK: Record<Complexity, number> = { low: 0, medium: 1, high: 2, maximum: 3 };
// Below this, Pi's retained recent context can outweigh the history summarized.
const MIN_HANDOFF_TOKENS = 32_768;
const STATUS_KEY = "weave-handoff";
const ROUTE_ENTRY_TYPE = "weave-handoff-route";
const SESSION_ENTRY_TYPE = "weave-handoff-session";

interface RouteSelection {
	token: string;
	session_token: string;
	summary_token?: string;
	model: string;
	provider: string;
	complexity?: Complexity;
}

type PreparedRoute = RouteSelection | { bypass: true };
type HandoffStage = "waiting" | "compacting" | "ready";

interface PendingHandoff {
	stage: HandoffStage;
	route: RouteSelection;
	sessionId: string;
	modelId: string;
	payload: Record<string, unknown>;
}

const HANDOFF_SUMMARY_INSTRUCTIONS = `Create a concise checkpoint for another model to continue this conversation. Do not execute tools or continue the task. Preserve the user's objective and constraints, decisions and reasoning, changed files and exact identifiers, test results, failures, unresolved questions, and remaining work. Distinguish verified facts from assumptions. Recent messages will also be retained; avoid repeating lengthy outputs. Return only the checkpoint, within 3000 tokens.`;

export function handoffSummaryPayload(original: Record<string, unknown>, generated: unknown, token: string, customInstructions?: string): Record<string, unknown> {
	if (!Array.isArray(original.messages) || !isRecord(generated)) throw new Error("Invalid compaction payload");
	return {
		...original,
		stream: generated.stream,
		// Preserve the original system, tools, thinking settings and message
		// prefix so the old model can reuse its cache when the provider permits.
		messages: [...original.messages, { role: "user", content: HANDOFF_SUMMARY_INSTRUCTIONS + (customInstructions ? `\n\nAdditional focus: ${customInstructions}` : "") }],
		weave_handoff: token,
	};
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isComplexity(value: unknown): value is Complexity {
	return typeof value === "string" && Object.hasOwn(COMPLEXITY_RANK, value);
}

export function parsePreparedRoute(value: unknown): PreparedRoute {
	if (!isRecord(value)) throw new Error("Invalid router handoff response");
	if (value.bypass === true) return { bypass: true };
	if (typeof value.token !== "string" || !value.token || typeof value.model !== "string" || !value.model ||
		typeof value.session_token !== "string" || !value.session_token ||
		typeof value.provider !== "string" || !value.provider ||
		(value.summary_token !== undefined && typeof value.summary_token !== "string")) {
		throw new Error("Invalid router handoff selection");
	}
	return {
		token: value.token, session_token: value.session_token, model: value.model, provider: value.provider,
		summary_token: value.summary_token as string | undefined,
		complexity: isComplexity(value.complexity) ? value.complexity : undefined,
	};
}

export function needsEscalationCompaction(previous: Pick<RouteSelection, "model" | "complexity"> | undefined, next: RouteSelection, tokens: number): boolean {
	return previous?.complexity !== undefined && next.complexity !== undefined &&
		previous.model !== next.model && COMPLEXITY_RANK[next.complexity] > COMPLEXITY_RANK[previous.complexity] &&
		tokens >= MIN_HANDOFF_TOKENS && !!next.summary_token;
}

export function registerEscalationCompaction(
	pi: ExtensionAPI,
	request: typeof fetch = fetch,
	version: string = VERSION,
	classifierThreadActive = () => false,
): () => boolean {
	const [major, minor] = version.split(".").map(Number);
	if (major === 0 && minor < 83) {
		pi.on("session_start", (_event, ctx) => {
			if (process.env.WEAVE_PI_ESCALATION_COMPACTION === "0") return;
			const message = "Weave escalation compaction requires Pi 0.83 or newer. Upgrade Pi to enable it; routing will continue without escalation compaction.";
			if (ctx.hasUI) ctx.ui.notify(message, "warning");
			else process.stderr.write(`${message}\n`);
		});
		return () => false;
	}
	let pending: PendingHandoff | undefined;
	let sessionToken: string | undefined;
	let previous: Pick<RouteSelection, "model" | "complexity"> | undefined;
	let dispatched: RouteSelection | undefined;
	let generation = 0;
	let finishContinuation: (() => void) | undefined;

	const clear = () => {
		generation++;
		pending = undefined;
		dispatched = undefined;
		previous = undefined;
		finishContinuation?.();
		finishContinuation = undefined;
	};
	pi.on("session_shutdown", clear);
	pi.on("model_select", clear);
	const restore = (_event: unknown, ctx: ExtensionContext) => {
		clear();
		sessionToken = undefined;
		for (const entry of ctx.sessionManager.getBranch()) {
			if (entry.type === "custom" && entry.customType === SESSION_ENTRY_TYPE && isRecord(entry.data) &&
				entry.data.sessionId === ctx.sessionManager.getSessionId() && typeof entry.data.token === "string") {
				sessionToken = entry.data.token;
			}
			if (entry.type !== "custom" || entry.customType !== ROUTE_ENTRY_TYPE) continue;
			previous = undefined;
			if (isRecord(entry.data) &&
				typeof entry.data.model === "string" && isComplexity(entry.data.complexity)) {
				previous = { model: entry.data.model, complexity: entry.data.complexity };
			}
		}
	};
	pi.on("session_start", restore);
	pi.on("session_tree", restore);
	pi.on("input", (event) => {
		if (event.source !== "extension") {
			generation++;
			pending = undefined;
			dispatched = undefined;
		}
	});

	const fail = (ctx: ExtensionContext, error: unknown) => {
		pending = undefined;
		dispatched = undefined;
		const message = `Weave handoff failed: ${error instanceof Error ? error.message : String(error)}`;
		if (ctx.hasUI) ctx.ui.notify(message, "error");
		else process.stderr.write(`${message}\n`);
	};

	pi.on("before_provider_request", async (event, ctx) => {
		if (classifierThreadActive()) return;
		if (process.env.WEAVE_PI_ESCALATION_COMPACTION === "0" || isSubagent() || ctx.model?.provider !== PROVIDER_NAME) return;
		if (!isRecord(event.payload)) return;
		let payload = sessionToken ? { ...event.payload, weave_session: sessionToken } : event.payload;
		const requestGeneration = generation;
		try {
			if (pending?.stage === "ready") {
				if (pending.sessionId !== ctx.sessionManager.getSessionId() || pending.modelId !== ctx.model.id) {
					throw new Error("Session changed during compaction");
				}
				dispatched = pending.route;
				pending = undefined;
				return { ...payload, weave_handoff: dispatched.token };
			}
			if (pending) throw new Error("A compaction handoff is already in progress");
			const key = resolveRouterKey();
			if (!key) throw new Error("Router key is missing");
			const signals = [AbortSignal.timeout(30_000)];
			if (ctx.signal) signals.push(ctx.signal);
			const response = await request(`${getRouterBaseUrl()}/v1/route/handoff`, {
				method: "POST",
				headers: { ...providerHeaders(getRole(), key), "content-type": "application/json" },
				body: JSON.stringify(payload), signal: AbortSignal.any(signals),
			});
			if (!response.ok) throw new Error(`Route preparation returned HTTP ${response.status}; the router must support Pi handoffs`);
			const prepared = parsePreparedRoute(await response.json());
			if (requestGeneration !== generation || ctx.signal?.aborted) {
				ctx.abort();
				return;
			}
			if ("bypass" in prepared) {
				dispatched = undefined;
				previous = undefined;
				pi.appendEntry(ROUTE_ENTRY_TYPE, {});
				return payload;
			}
			if (sessionToken !== prepared.session_token) {
				sessionToken = prepared.session_token;
				pi.appendEntry(SESSION_ENTRY_TYPE, { sessionId: ctx.sessionManager.getSessionId(), token: sessionToken });
			}
			payload = { ...payload, weave_session: sessionToken };
			if (!needsEscalationCompaction(previous, prepared, ctx.getContextUsage()?.tokens ?? 0)) {
				dispatched = prepared;
				return { ...payload, weave_handoff: prepared.token };
			}
			pending = { stage: "waiting", route: prepared, sessionId: ctx.sessionManager.getSessionId(), modelId: ctx.model.id, payload: structuredClone(payload) };
			// Awaiting compact/abort here deadlocks the active provider request. The
			// aborted signal prevents dispatch; agent_settled owns compaction.
			ctx.abort();
		} catch (error) {
			ctx.abort();
			if (requestGeneration === generation) fail(ctx, error);
		}
	});

	pi.on("after_provider_response", (event) => {
		if (event.status >= 200 && event.status < 300 && dispatched) {
			previous = event.headers[ROUTED_MODEL_HEADER] === dispatched.model ? dispatched : undefined;
			pi.appendEntry(ROUTE_ENTRY_TYPE, previous ? { model: previous.model, complexity: previous.complexity } : {});
		}
		dispatched = undefined;
	});

	pi.on("agent_settled", async (_event, ctx) => {
		if (pending?.stage !== "waiting") {
			finishContinuation?.();
			finishContinuation = undefined;
			return;
		}
		const handoff = pending;
		handoff.stage = "compacting";
		// Pi is idle at agent_settled, so compact() can safely wait for abort.
		// Keep this event pending through the continuation so print mode cannot
		// dispose the runtime between the aborted request and its replacement.
		await new Promise<void>((resolve) => {
			if (ctx.hasUI) ctx.ui.setStatus(STATUS_KEY, `compacting before handoff to ${handoff.route.model}...`);
			ctx.compact({
				onComplete: () => {
					if (ctx.hasUI) ctx.ui.setStatus(STATUS_KEY, undefined);
					if (pending !== handoff) { resolve(); return; }
					handoff.stage = "ready";
					finishContinuation = resolve;
					pi.sendUserMessage(ROUTED_COMPACTION_CONTINUATION, ctx.isIdle() ? undefined : { deliverAs: "followUp" });
				},
				onError: (error) => {
					if (ctx.hasUI) ctx.ui.setStatus(STATUS_KEY, undefined);
					if (pending === handoff) fail(ctx, error);
					resolve();
				},
			});
		});
	});

	pi.on("session_before_compact", async (event, ctx) => {
		// Let the pending handoff run at agent_settled rather than racing Pi's
		// threshold compaction between agent_end and the settled event.
		if (pending?.stage === "waiting") return { cancel: true };
		if (pending?.stage !== "compacting") return;
		const handoff = pending;
		const key = resolveRouterKey();
		if (!ctx.model || !key) return { cancel: true };
		try {
			const summary = await compact(
				event.preparation, ctx.model, key, providerHeaders(getRole(), key), event.customInstructions, event.signal,
				undefined,
				(model, context, options) => streamSimple(model, context, {
					...options,
					onPayload: (payload) => handoffSummaryPayload(handoff.payload, payload, handoff.route.summary_token!, event.customInstructions),
				}),
			);
			if (!summary.summary.trim()) throw new Error("The previous model returned an empty compaction summary");
			if (pending !== handoff || event.signal.aborted) return { cancel: true };
			return { compaction: summary };
		} catch (error) {
			// Pi catches extension errors and otherwise proceeds with its default
			// summarizer. Explicit cancellation prevents a second paid summary.
			if (pending === handoff) fail(ctx, error);
			return { cancel: true };
		}
	});
	return () => pending !== undefined;
}
