import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import {
	PROVIDER_NAME,
	ROUTED_CONTEXT_WINDOW_HEADER,
	ROUTED_MODEL_HEADER,
	WEAVE_MODELS,
} from "../src/config.js";
import { parseRoutedContextWindow } from "../src/context-window.js";
import { registerRoutedModel } from "../src/routed-model.js";

type CapturedHandler = (event: any, ctx: ExtensionContext) => unknown;

interface ProviderRegistration {
	name: string;
	config: { models?: Array<{ id: string; contextWindow: number }> };
}

function extensionHarness() {
	const handlers = new Map<string, CapturedHandler[]>();
	const registrations: ProviderRegistration[] = [];
	const pi = {
		on(event: string, handler: CapturedHandler) {
			const eventHandlers = handlers.get(event) ?? [];
			eventHandlers.push(handler);
			handlers.set(event, eventHandlers);
		},
		registerProvider(name: string, config: ProviderRegistration["config"]) {
			registrations.push({ name, config });
		},
	} as unknown as ExtensionAPI;
	registerRoutedModel(pi);
	return {
		registrations,
		emit(event: string, payload: unknown, ctx: ExtensionContext) {
			for (const handler of handlers.get(event) ?? []) handler(payload, ctx);
		},
	};
}

function context({ modelId = "claude-haiku-4-5", provider = PROVIDER_NAME } = {}) {
	return {
		hasUI: false,
		model: { provider, id: modelId },
		sessionManager: { getBranch: () => [] },
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
