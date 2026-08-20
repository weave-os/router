/**
 * Compaction safeguards for Pi's routed provider.
 *
 * Pi 0.74 checks automatic compaction only after an entire agent loop. A long
 * tool loop can therefore cross the virtual model's context window between
 * turns. Pi then clamps the next request to max_tokens=1, which looks exactly
 * like an SDK quota probe to the router and produces a one-token response.
 *
 * Preserve real probes, but restore a usable budget for a clamped request that
 * contains an actual tool result. Once the agent finishes, compact using the
 * highest context reading seen during the run; routed providers tokenize and
 * report caches differently, so the final response alone is not authoritative.
 */

import type {
	AgentEndEvent,
	BeforeProviderRequestEvent,
	ExtensionAPI,
	ExtensionContext,
	SessionCompactEvent,
	TurnEndEvent,
} from "@mariozechner/pi-coding-agent";
import type { AssistantMessage } from "@mariozechner/pi-ai";
import { ROUTED_CONTEXT_WINDOW_HEADER } from "./config.js";

const PROBE_MAX_TOKENS = 4;
const CONTINUATION_MAX_TOKENS = 16_384;
const COMPACTION_RESERVE_TOKENS = 16_384;
const STATUS_KEY = "weave-compaction";

interface ProviderPayload {
	max_tokens?: unknown;
	messages?: unknown;
	tools?: unknown;
}

interface ProviderMessage {
	content?: unknown;
}

type Schedule = (callback: () => void) => unknown;

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}

function containsToolResult(messages: unknown): boolean {
	if (!Array.isArray(messages)) return false;
	return messages.some((message: unknown) => {
		if (!isRecord(message)) return false;
		const content = (message as ProviderMessage).content;
		return Array.isArray(content) && content.some((block: unknown) => isRecord(block) && block.type === "tool_result");
	});
}

/**
 * Parse the router-served context window header, rejecting absent, fractional,
 * or out-of-range values. The header reflects what the router actually served
 * for a request, which can differ from the requested model's registered window
 * (reroutes to a smaller window, e.g. minimax-m2.7 204_800 or haiku 200_000).
 */
export function parseRoutedContextWindow(value: string | undefined): number | undefined {
	if (!value) return undefined;
	if (!/^[1-9]\d*$/.test(value)) return undefined;
	const contextWindow = Number(value);
	return Number.isSafeInteger(contextWindow) && contextWindow > 0 ? contextWindow : undefined;
}

/**
 * Repair only the Pi context-exhaustion shape. Genuine SDK probes have no tool
 * result transcript and remain at max_tokens=1..4 for the router to hard-pin.
 */
export function repairClampedToolContinuation(payload: unknown): boolean {
	if (!isRecord(payload)) return false;
	const body = payload as ProviderPayload;
	if (typeof body.max_tokens !== "number" || body.max_tokens < 1 || body.max_tokens > PROBE_MAX_TOKENS) return false;
	if (!Array.isArray(body.tools) || body.tools.length === 0) return false;
	if (!containsToolResult(body.messages)) return false;
	body.max_tokens = CONTINUATION_MAX_TOKENS;
	return true;
}

function contextTokens(message: AssistantMessage): number {
	const usage = message.usage;
	if (usage.totalTokens > 0) return usage.totalTokens;
	return usage.input + usage.output + usage.cacheRead + usage.cacheWrite;
}

function latestCompactionId(ctx: ExtensionContext): string | undefined {
	const branch = ctx.sessionManager.getBranch();
	for (let i = branch.length - 1; i >= 0; i--) {
		const entry = branch[i];
		if (entry?.type === "compaction") return entry.id;
	}
	return undefined;
}

export function registerCompaction(pi: ExtensionAPI, schedule: Schedule = (callback) => setTimeout(callback, 0)): void {
	let highWaterTokens = 0;
	let lastTurnTokens = 0;
	let repairedContinuation = false;
	let compactionScheduled = false;
	// Smallest served context window observed on a response this run. The router
	// can reroute any request regardless of the requested model, so the served
	// window is authoritative for the compaction budget. Tracking the minimum
	// (not the latest or the max) is the conservative choice: if the router ever
	// routed to a smaller window during the run, that window could clamp again,
	// so the budget must respect it. Fall back to the registered model window
	// until the first routed response.
	let servedContextWindow: number | undefined;

	const resetRun = () => {
		highWaterTokens = 0;
		lastTurnTokens = 0;
		repairedContinuation = false;
		servedContextWindow = undefined;
	};

	const finishCompaction = (ctx: ExtensionContext) => {
		compactionScheduled = false;
		resetRun();
		if (ctx.hasUI) ctx.ui.setStatus(STATUS_KEY, undefined);
	};

	pi.on("session_start", resetRun);
	pi.on("agent_start", resetRun);
	pi.on("session_compact", (_event: SessionCompactEvent, ctx: ExtensionContext) => finishCompaction(ctx));

	pi.on("before_provider_request", (event: BeforeProviderRequestEvent) => {
		if (repairClampedToolContinuation(event.payload)) repairedContinuation = true;
	});

	pi.on("after_provider_response", (event, _ctx: ExtensionContext) => {
		if (event.status < 200 || event.status >= 300) return;
		const contextWindow = parseRoutedContextWindow(event.headers?.[ROUTED_CONTEXT_WINDOW_HEADER]);
		if (contextWindow === undefined) return;
		if (servedContextWindow === undefined || contextWindow < servedContextWindow) {
			servedContextWindow = contextWindow;
		}
	});

	pi.on("turn_end", (event: TurnEndEvent) => {
		if (event.message.role !== "assistant") return;
		lastTurnTokens = contextTokens(event.message as AssistantMessage);
		highWaterTokens = Math.max(highWaterTokens, lastTurnTokens);
	});

	pi.on("agent_end", (_event: AgentEndEvent, ctx: ExtensionContext) => {
		if (process.env.WEAVE_PI_AUTO_COMPACTION === "0" || compactionScheduled) return;
		const registeredWindow = ctx.model?.contextWindow ?? ctx.getContextUsage()?.contextWindow ?? 0;
		const contextWindow = servedContextWindow ?? registeredWindow;
		if (contextWindow <= COMPACTION_RESERVE_TOKENS) return;
		const servedThreshold = contextWindow - COMPACTION_RESERVE_TOKENS;
		const registeredThreshold = registeredWindow - COMPACTION_RESERVE_TOKENS;
		// Pi's built-in check runs immediately after this event and budgets against
		// the REGISTERED model window. If the final turn clears Pi's registered
		// threshold, Pi will compact asynchronously — deferring is correct, and
		// scheduling our own would race that async compaction. Otherwise Pi will not
		// act, so we compact ourselves when the run crossed the SERVED threshold
		// (which can be smaller than the registered window after a reroute).
		if (lastTurnTokens > registeredThreshold) return;
		if (!repairedContinuation && highWaterTokens <= servedThreshold) return;

		compactionScheduled = true;
		const previousCompactionId = latestCompactionId(ctx);

		// Pi is still settling agent_end while extension handlers run. Schedule
		// manual compaction for the next event-loop turn to avoid abort/wait
		// recursion, and let Pi's own auto-compaction win if it already ran.
		schedule(() => {
			if (!compactionScheduled) return;
			const currentCompactionId = latestCompactionId(ctx);
			if (currentCompactionId !== previousCompactionId) {
				finishCompaction(ctx);
				return;
			}
			if (ctx.hasUI) ctx.ui.setStatus(STATUS_KEY, "compacting routed context...");
			ctx.compact({
				onComplete: () => finishCompaction(ctx),
				onError: (error) => {
					compactionScheduled = false;
					if (ctx.hasUI) ctx.ui.setStatus(STATUS_KEY, `compaction failed: ${error.message}`);
				},
			});
		});
	});
}
