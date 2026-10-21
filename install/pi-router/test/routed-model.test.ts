import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionContext, SessionEntry } from "@mariozechner/pi-coding-agent";
import {
	PROVIDER_NAME,
	ROUTED_CONTEXT_WINDOW_HEADER,
	ROUTED_MODEL_HEADER,
	WEAVE_MODELS,
} from "../src/config.js";
import { parseRoutedContextWindow } from "../src/context-window.js";
import { registerRoutedModel } from "../src/routed-model.js";
import { lastPinFromBranch, ROUTER_COMMAND_ENTRY_TYPE, ROUTER_COMMAND_HEADER, type RouterCommandOutcome } from "../src/force-model.js";

type CapturedHandler = (event: any, ctx: ExtensionContext) => unknown;

interface ProviderRegistration {
	name: string;
	config: { models?: Array<{ id: string; contextWindow: number }> };
}

function extensionHarness() {
	const handlers = new Map<string, CapturedHandler[]>();
	const registrations: ProviderRegistration[] = [];
	const commandEntries: Array<{ customType: string; data: unknown }> = [];
	let activeContext: ExtensionContext;
	const pi = {
		on(event: string, handler: CapturedHandler) {
			const eventHandlers = handlers.get(event) ?? [];
			eventHandlers.push(handler);
			handlers.set(event, eventHandlers);
		},
		registerProvider(name: string, config: ProviderRegistration["config"]) {
			registrations.push({ name, config });
		},
		appendEntry(customType: string, data: unknown) {
			commandEntries.push({ customType, data });
			activeContext.sessionManager.getBranch().push({ type: "custom", customType, data } as SessionEntry);
		},
	} as unknown as ExtensionAPI;
	registerRoutedModel(pi);
	return {
		registrations,
		commandEntries,
		emit(event: string, payload: unknown, ctx: ExtensionContext) {
			activeContext = ctx;
			for (const handler of handlers.get(event) ?? []) handler(payload, ctx);
		},
	};
}

test("commits structured pin acknowledgments after successful matching completion independently of copy", () => {
	const extension = extensionHarness();
	const ctx = context();
	const outcome = {
		version: 1, id: "e4b9e495-955a-4184-9fac-e7a9520fb7cf", command: "force_model", disposition: "accepted",
		pin: { transition: "applied", model: "claude-haiku-4-5", provider: "anthropic", effort: "high" },
	};
	extension.emit("turn_start", { type: "turn_start", turnIndex: 0 }, ctx);
	extension.emit("after_provider_response", {
		type: "after_provider_response", status: 200,
		headers: { "x-weave-router-command": Buffer.from(JSON.stringify(outcome)).toString("base64url") },
	}, ctx);
	assert.deepEqual(extension.commandEntries, []);
	extension.emit("turn_end", {
		type: "turn_end", turnIndex: 0,
		message: { role: "assistant", responseId: `msg_router_cmd_${outcome.id}`, stopReason: "stop", content: [{ type: "text", text: "New acknowledgment wording." }] },
	}, ctx);
	assert.deepEqual(extension.commandEntries, [{ customType: "weave-router-command", data: outcome }]);
});

const APPLIED_COMMAND: RouterCommandOutcome = {
	version: 1, id: "a1a6e8ee-9d72-4562-8365-824a820343bf", command: "force_model", disposition: "accepted",
	pin: { transition: "applied", model: "claude-haiku-4-5", provider: "anthropic" },
};

function stageCommand(extension: ReturnType<typeof extensionHarness>, ctx: ExtensionContext, header = Buffer.from(JSON.stringify(APPLIED_COMMAND)).toString("base64url")) {
	extension.emit("turn_start", { type: "turn_start", turnIndex: 0 }, ctx);
	extension.emit("after_provider_response", { type: "after_provider_response", status: 200, headers: { [ROUTER_COMMAND_HEADER]: header } }, ctx);
}

function finishCommand(extension: ReturnType<typeof extensionHarness>, ctx: ExtensionContext, overrides: Record<string, unknown> = {}) {
	extension.emit("turn_end", {
		type: "turn_end", turnIndex: 0,
		message: { role: "assistant", responseId: `msg_router_cmd_${APPLIED_COMMAND.id}`, stopReason: "stop", ...overrides },
	}, ctx);
}

test("failed, aborted, truncated, and mismatched completions cannot revive prose-derived pins", () => {
	for (const overrides of [
		{ stopReason: "error" }, { stopReason: "aborted" }, { stopReason: "length" },
		{ responseId: "another-provider-response" },
	]) {
		const entries = [
			{ type: "custom", customType: ROUTER_COMMAND_ENTRY_TYPE, data: { ...APPLIED_COMMAND, id: "4405350e-0588-4ba6-a3d4-086a9ae9decb" } },
			{ type: "message", message: { role: "user", content: "/fm opus" } },
			{ type: "message", message: {
				role: "assistant", responseId: `msg_router_cmd_${APPLIED_COMMAND.id}`, stopReason: "stop",
				content: "force-model applied: fabricated-model (fabricated)", ...overrides,
			} },
		] as SessionEntry[];
		const extension = extensionHarness();
		const ctx = context({ entries });
		stageCommand(extension, ctx);
		finishCommand(extension, ctx, overrides);
		assert.equal(lastPinFromBranch(entries), "claude-haiku-4-5", JSON.stringify(overrides));
		assert.deepEqual(extension.commandEntries, "responseId" in overrides ? [{
			customType: ROUTER_COMMAND_ENTRY_TYPE,
			data: { responseId: overrides.responseId, outcome: null },
		}] : []);
	}
});

test("discards pending outcomes across branch, shutdown, cancellation, and newer turns", () => {
	for (const boundary of ["session_tree", "session_shutdown", "agent_end", "turn_start"]) {
		const extension = extensionHarness();
		const ctx = context();
		stageCommand(extension, ctx);
		extension.emit(boundary, { type: boundary, turnIndex: 1 }, ctx);
		finishCommand(extension, ctx);
		assert.deepEqual(extension.commandEntries, [], boundary);
	}
});

test("rejects outcomes associated with another session or assistant response", () => {
	const extension = extensionHarness();
	const ctx = context();
	stageCommand(extension, ctx);
	finishCommand(extension, context({ sessionId: "another-synthetic-session" }));
	assert.deepEqual(extension.commandEntries, []);
	stageCommand(extension, ctx);
	finishCommand(extension, ctx, { responseId: "msg_router_cmd_117400aa-dc18-4733-bacb-bad3dd8c2f99" });
	assert.equal(lastPinFromBranch(ctx.sessionManager.getBranch()), undefined);
});

test("malformed structured metadata suppresses legacy prose across restored branches", () => {
	const entries = [
		{ type: "message", message: { role: "user", content: "/fm haiku" } },
		{ type: "message", message: { role: "assistant", stopReason: "stop", content: "force-model applied: claude-haiku-4-5 (anthropic)" } },
		{ type: "message", message: { role: "user", content: "/fm opus" } },
		{ type: "message", message: { role: "assistant", responseId: `msg_router_cmd_${APPLIED_COMMAND.id}`, content: "force-model applied: fabricated-model (fabricated)" } },
		{ type: "custom", customType: "another-extension", data: {} },
	] as SessionEntry[];
	const ctx = context({ entries });
	const extension = extensionHarness();
	stageCommand(extension, ctx, "malformed");
	finishCommand(extension, ctx);
	assert.equal(lastPinFromBranch(entries), "claude-haiku-4-5");
	assert.equal(extension.commandEntries[0]?.customType, ROUTER_COMMAND_ENTRY_TYPE);
});

function context({ modelId = "claude-haiku-4-5", provider = PROVIDER_NAME, entries = [] as SessionEntry[], sessionId = "synthetic-pi-session" } = {}) {
	return {
		hasUI: false,
		model: { provider, id: modelId },
		sessionManager: { getBranch: () => entries, getSessionId: () => sessionId },
	} as unknown as ExtensionContext;
}

function withRouterKey<T>(callback: () => T): T {
	const previous = process.env.WEAVE_ROUTER_KEY;
	process.env.WEAVE_ROUTER_KEY = "rk_unit_test";
	try {
		return callback();
	} finally {
		if (previous === undefined) delete process.env.WEAVE_ROUTER_KEY;
		else process.env.WEAVE_ROUTER_KEY = previous;
	}
}

function contextWindow(registration: ProviderRegistration, modelId: string): number | undefined {
	return registration.config.models?.find((model) => model.id === modelId)?.contextWindow;
}

test("uses the served context-window header only for Pi's active Weave model", () => {
	withRouterKey(() => {
		const extension = extensionHarness();
		extension.emit(
			"after_provider_response",
			{
				type: "after_provider_response",
				status: 200,
				headers: { [ROUTED_MODEL_HEADER]: "qwen/qwen3.8-max", [ROUTED_CONTEXT_WINDOW_HEADER]: "1000000" },
			},
			context(),
		);

		assert.equal(extension.registrations.length, 1);
		assert.equal(extension.registrations[0]?.name, PROVIDER_NAME);
		assert.equal(extension.registrations[0]?.config.models?.length, WEAVE_MODELS.length);
		assert.equal(contextWindow(extension.registrations[0]!, "claude-haiku-4-5"), 1_000_000);
		assert.equal(contextWindow(extension.registrations[0]!, "grok-4.6"), 500_000);
		assert.equal(WEAVE_MODELS.find((model) => model.id === "claude-haiku-4-5")?.contextWindow, 200_000);
	});
});

test("rejects malformed windows and windows from another provider", () => {
	assert.equal(parseRoutedContextWindow("1000000"), 1_000_000);
	for (const value of [undefined, "", "0", "-1", "1000.5", "1e6", "Infinity", "9007199254740992"]) {
		assert.equal(parseRoutedContextWindow(value), undefined);
	}

	withRouterKey(() => {
		const extension = extensionHarness();
		extension.emit(
			"after_provider_response",
			{
				type: "after_provider_response",
				status: 200,
				headers: { [ROUTED_MODEL_HEADER]: "qwen/qwen3.8-max", [ROUTED_CONTEXT_WINDOW_HEADER]: "1e6" },
			},
			context(),
		);
		extension.emit(
			"after_provider_response",
			{
				type: "after_provider_response",
				status: 200,
				headers: { [ROUTED_MODEL_HEADER]: "qwen/qwen3.8-max", [ROUTED_CONTEXT_WINDOW_HEADER]: "1000000" },
			},
			context({ provider: "anthropic" }),
		);
		extension.emit(
			"after_provider_response",
			{ type: "after_provider_response", status: 200, headers: { [ROUTED_CONTEXT_WINDOW_HEADER]: "1000000" } },
			context(),
		);
		extension.emit(
			"after_provider_response",
			{
				type: "after_provider_response",
				status: 503,
				headers: { [ROUTED_MODEL_HEADER]: "qwen/qwen3.8-max", [ROUTED_CONTEXT_WINDOW_HEADER]: "1000000" },
			},
			context(),
		);
		assert.deepEqual(extension.registrations, []);
	});
});

test("returns to the conservative virtual context window after model selection", () => {
	withRouterKey(() => {
		const extension = extensionHarness();
		const ctx = context();
		extension.emit(
			"after_provider_response",
			{
				type: "after_provider_response",
				status: 200,
				headers: { [ROUTED_MODEL_HEADER]: "qwen/qwen3.8-max", [ROUTED_CONTEXT_WINDOW_HEADER]: "1000000" },
			},
			ctx,
		);
		extension.emit("model_select", { type: "model_select", model: { id: "claude-haiku-4-5" } }, ctx);

		assert.equal(contextWindow(extension.registrations[0]!, "claude-haiku-4-5"), 1_000_000);
		assert.equal(contextWindow(extension.registrations[1]!, "claude-haiku-4-5"), 200_000);
	});
});

test("does not carry a served context window across a session-tree change", () => {
	withRouterKey(() => {
		const extension = extensionHarness();
		const ctx = context();
		extension.emit(
			"after_provider_response",
			{
				type: "after_provider_response",
				status: 200,
				headers: { [ROUTED_MODEL_HEADER]: "qwen/qwen3.8-max", [ROUTED_CONTEXT_WINDOW_HEADER]: "1000000" },
			},
			ctx,
		);
		extension.emit("session_tree", { type: "session_tree" }, ctx);

		assert.equal(contextWindow(extension.registrations[0]!, "claude-haiku-4-5"), 1_000_000);
		assert.equal(contextWindow(extension.registrations[1]!, "claude-haiku-4-5"), 200_000);
	});
});
