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

	const resetRun = () => {
		highWaterTokens = 0;
		lastTurnTokens = 0;
		repairedContinuation = false;
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

	pi.on("turn_end", (event: TurnEndEvent) => {
		if (event.message.role !== "assistant") return;
		lastTurnTokens = contextTokens(event.message as AssistantMessage);
		highWaterTokens = Math.max(highWaterTokens, lastTurnTokens);
	});

	pi.on("agent_end", (_event: AgentEndEvent, ctx: ExtensionContext) => {
		if (process.env.WEAVE_PI_AUTO_COMPACTION === "0" || compactionScheduled) return;
		const contextWindow = ctx.model?.contextWindow ?? ctx.getContextUsage()?.contextWindow ?? 0;
		if (contextWindow <= COMPACTION_RESERVE_TOKENS) return;
		const threshold = contextWindow - COMPACTION_RESERVE_TOKENS;
		// Pi's built-in check runs immediately after this event and owns the
		// ordinary final-turn threshold case. Starting another compaction while
		// that async summary is in flight would race it.
		if (lastTurnTokens > threshold) return;
		if (!repairedContinuation && highWaterTokens <= threshold) return;

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
