import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { registerCompaction, repairClampedToolContinuation } from "../src/compaction.js";

type CapturedHandler = (event: any, ctx: ExtensionContext) => unknown;

function extensionHarness(schedule?: (callback: () => void) => unknown) {
	const handlers = new Map<string, CapturedHandler[]>();
	const pi = {
		on(event: string, handler: CapturedHandler) {
			const eventHandlers = handlers.get(event) ?? [];
			eventHandlers.push(handler);
			handlers.set(event, eventHandlers);
		},
	} as unknown as ExtensionAPI;
	registerCompaction(pi, schedule);
	return {
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

function contextHarness(modelWindow = 200_000) {
	let compactCalls = 0;
	let status: string | undefined;
	const branch: any[] = [];
	const ctx = {
		hasUI: true,
		model: { contextWindow: modelWindow },
		getContextUsage: () => ({ tokens: 0, contextWindow: modelWindow, percent: 0 }),
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
});

test("does not compact a run that stays below Pi's normal reserve threshold", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(150_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 0);
});

test("leaves an over-threshold final turn to Pi's built-in compaction", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(190_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	assert.equal(compactCalls(), 0);
});

test("budgets compaction off the served x-router-context-window, not the requested model", () => {
	const extension = extensionHarness((callback) => callback());
	// ctx.model is the client's requested model (200K); the router's header
	// reports the window of the model that actually served the response (1M).
	const { ctx, compactCalls } = contextHarness();
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit(
		"after_provider_response",
		{ type: "after_provider_response", status: 200, headers: { "x-router-context-window": "1000000" } },
		ctx,
	);
	extension.emit("turn_end", { type: "turn_end", message: assistant(250_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	// 250K is above the 200K requested-model budget but inside the served 1M
	// budget, so the extension must not pre-compact.
	assert.equal(compactCalls(), 0);
});

test("compacts when the served window is smaller than the requested model's", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness(1_000_000);
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit(
		"after_provider_response",
		{ type: "after_provider_response", status: 200, headers: { "x-router-context-window": "200000" } },
		ctx,
	);
	extension.emit("turn_end", { type: "turn_end", message: assistant(250_000), toolResults: [] }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(50_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	// highWater 250K exceeds the served 200K budget even though the requested
	// model window is 1M, so a mid-loop compaction is required.
	assert.equal(compactCalls(), 1);
});

test("does not shrink the budget when the served window header is absent", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness(1_000_000);
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(250_000), toolResults: [] }, ctx);
	extension.emit("turn_end", { type: "turn_end", message: assistant(50_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	// Without the routed window, the 1M requested budget applies and the run
	// stays below it, so Pi's ordinary threshold compaction owns the case.
	assert.equal(compactCalls(), 0);
});

test("compacts an over-threshold final turn when the served window is smaller than the requested budget", () => {
	const extension = extensionHarness((callback) => callback());
	const { ctx, compactCalls } = contextHarness(1_000_000);
	extension.emit("agent_start", { type: "agent_start" }, ctx);
	extension.emit(
		"after_provider_response",
		{ type: "after_provider_response", status: 200, headers: { "x-router-context-window": "200000" } },
		ctx,
	);
	extension.emit("turn_end", { type: "turn_end", message: assistant(190_000), toolResults: [] }, ctx);
	extension.emit("agent_end", { type: "agent_end", messages: [] }, ctx);

	// 190K sits above the served 200K budget threshold (183,616) but below the
	// requested 1M budget - Pi would not compact, so the extension must.
	assert.equal(compactCalls(), 1);
});
