import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import {
	registerCompaction,
	ROUTED_COMPACTION_CONTINUATION,
	repairClampedToolContinuation,
} from "../src/compaction.js";
import { parseRoutedContextWindow } from "../src/context-window.js";

type CapturedHandler = (event: any, ctx: ExtensionContext) => unknown;
type SendOptions = Parameters<ExtensionAPI["sendUserMessage"]>[1];

function extensionHarness(schedule?: (callback: () => void) => unknown) {
	const handlers = new Map<string, CapturedHandler[]>();
	const sentMessages: Array<{ content: string; options?: SendOptions }> = [];
	const pi = {
		on(event: string, handler: CapturedHandler) {
			const eventHandlers = handlers.get(event) ?? [];
			eventHandlers.push(handler);
			handlers.set(event, eventHandlers);
		},
		sendUserMessage(message: string, options?: SendOptions) {
			sentMessages.push({ content: message, options });
		},
	} as unknown as ExtensionAPI;
	registerCompaction(pi, schedule);
	return {
		sentMessages: () => sentMessages,
		emit(event: string, payload: unknown, ctx: ExtensionContext) {
			for (const handler of handlers.get(event) ?? []) handler(payload, ctx);
		},
	};
}

function assistant(totalTokens: number) {
	return {
		role: "assistant",
		content: [],
		usage: {
			input: totalTokens,
			output: 0,
			cacheRead: 0,
			cacheWrite: 0,
			totalTokens,
		},
		stopReason: "stop",
	};
}

function contextHarness(registeredWindow = 200_000, idle = true) {
	let compactCalls = 0;
	let status: string | undefined;
	const branch: any[] = [];
	const ctx = {
		hasUI: true,
		isIdle: () => idle,
		model: { contextWindow: registeredWindow },
		getContextUsage: () => ({ tokens: 0, contextWindow: registeredWindow, percent: 0 }),
		sessionManager: { getBranch: () => branch },
		ui: {
			setStatus(_key: string, value: string | undefined) {
				status = value;
			},
		},
		compact(options?: { onComplete?: (result: unknown) => void }) {
			compactCalls += 1;
			options?.onComplete?.({});
		},
	} as unknown as ExtensionContext;
	return { ctx, compactCalls: () => compactCalls, status: () => status };
}

test("repairs only clamped requests that continue from a tool result", () => {
	const genuineProbe = {
		max_tokens: 1,
		messages: [{ role: "user", content: [{ type: "text", text: "ping" }] }],
		tools: [],
	};
	assert.equal(repairClampedToolContinuation(genuineProbe), false);
	assert.equal(genuineProbe.max_tokens, 1);

	const continuation = {
		max_tokens: 1,
		messages: [
			{ role: "assistant", content: [{ type: "tool_use", id: "tool-1", name: "bash", input: {} }] },
			{ role: "user", content: [{ type: "tool_result", tool_use_id: "tool-1", content: "done" }] },
		],
		tools: [{ name: "bash" }],
	};
	assert.equal(repairClampedToolContinuation(continuation), true);
	assert.equal(continuation.max_tokens, 16_384);

	const normalTurn = { ...continuation, max_tokens: 64_000 };
	assert.equal(repairClampedToolContinuation(normalTurn), false);
	assert.equal(normalTurn.max_tokens, 64_000);
});

test("compacts once after a routed tool loop crosses the virtual context window", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls, status } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(210_910), toolResults: [] }, ctx);

	const continuation = {
		max_tokens: 1,
		messages: [{ role: "user", content: [{ type: "tool_result", tool_use_id: "tool-1", content: "done" }] }],
		tools: [{ name: "bash" }],
	};
	extension.emit("before_provider_request", { type: "before_provider_request", payload: continuation }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(135_652), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(continuation.max_tokens, 16_384);
	assert.equal(compactCalls(), 1);
	assert.equal(status(), undefined);
	assert.deepEqual(extension.sentMessages(), [{ content: ROUTED_COMPACTION_CONTINUATION, options: undefined }]);
});

test("queues a manual-compaction continuation as a follow-up when Pi is active", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness(200_000, false);
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(210_910), toolResults: [] }, ctx);

	const continuation = {
		max_tokens: 1,
		messages: [{ role: "user", content: [{ type: "tool_result", tool_use_id: "tool-1", content: "done" }] }],
		tools: [{ name: "bash" }],
	};
	extension.emit("before_provider_request", { type: "before_provider_request", payload: continuation }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(135_652), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 1);
	assert.deepEqual(extension.sentMessages(), [
		{ content: ROUTED_COMPACTION_CONTINUATION, options: { deliverAs: "followUp" } },
	]);
});

test("does not compact a run that stays below Pi's normal reserve threshold", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(150_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 0);
	assert.deepEqual(extension.sentMessages(), []);
});

test("leaves an over-threshold final turn to Pi's built-in compaction", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(190_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 0);
	assert.deepEqual(extension.sentMessages(), []);
});

test("does not inject a continuation for Pi-owned threshold compaction", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx } = contextHarness();

	extension.emit(
		"session_compact",
		{ type: "session_compact", fromExtension: false, reason: "threshold", willRetry: false },
		ctx,
	);

	assert.deepEqual(extension.sentMessages(), []);
});

test("parseRoutedContextWindow accepts only sane positive integers", () => {
	assert.equal(parseRoutedContextWindow(undefined), undefined);
	assert.equal(parseRoutedContextWindow(""), undefined);
	assert.equal(parseRoutedContextWindow("abc"), undefined);
	assert.equal(parseRoutedContextWindow("1.5"), undefined);
	assert.equal(parseRoutedContextWindow("0"), undefined);
	assert.equal(parseRoutedContextWindow("200000"), 200_000);
	assert.equal(parseRoutedContextWindow("1000000"), 1_000_000);
});

test("prefers the router-served context window so a large model does not compact early", () => {
	// The registered virtual model window is 200k, but the router reports it
	// served a 1M window. A tool loop well above the 200k threshold must not
	// trigger the extension's manual compaction.
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit(
		"after_provider_response",
		{ type: "after_provider_response", status: 200, headers: { "x-router-context-window": "1000000" } },
		ctx,
	);
	extension.emit("turn_end", { type: "turn_end", message: assistant(300_000), toolResults: [] }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(150_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 0);
});

test("without a served-window header the registered model window still gates compaction", () => {
	// Same run shape as above, but no served-window header: the registered 200k
	// window is authoritative, so the run does early-compact.
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(300_000), toolResults: [] }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(150_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 1);
});

test("guards against a reroute to a smaller served context window", () => {
	// The registered model window is 1M (a cap-extended opus/sonnet), but the
	// router served responses from a 200k window (e.g. haiku or a small reroute).
	// The run must compact at the served window, not the registered 1M.
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness(1_000_000);
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit(
		"after_provider_response",
		{ type: "after_provider_response", status: 200, headers: { "x-router-context-window": "200000" } },
		ctx,
	);
	extension.emit("turn_end", { type: "turn_end", message: assistant(250_000), toolResults: [] }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(100_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 1);
});

test("compacts itself when the final turn exceeds a smaller served window", () => {
	// Registered window is 1M, but the router served a 200k window. The final
	// turn (250k) is above the served threshold (183_616) yet well below the
	// registered 1M. Pi budgets against the registered window and would never
	// compact here, so the extension must compact itself instead of deferring.
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness(1_000_000);
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit(
		"after_provider_response",
		{ type: "after_provider_response", status: 200, headers: { "x-router-context-window": "200000" } },
		ctx,
	);
	extension.emit("turn_end", { type: "turn_end", message: assistant(100_000), toolResults: [] }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(250_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 1);
});

test("defers to Pi when the final turn clears the registered threshold (no race)", () => {
	// Registered window is 1M, served 200k, and the final turn (990k) clears Pi's
	// registered threshold (983_616). Pi's built-in compaction will fire after
	// agent_end, so scheduling our own would race it — the extension must defer.
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness(1_000_000);
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit(
		"after_provider_response",
		{ type: "after_provider_response", status: 200, headers: { "x-router-context-window": "200000" } },
		ctx,
	);
	extension.emit("turn_end", { type: "turn_end", message: assistant(990_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 0);
});
