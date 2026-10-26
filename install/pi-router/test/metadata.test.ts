import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { PROVIDER_NAME } from "../src/config.js";
import { registerMetadata, routingThread } from "../src/metadata.js";

test("routing metadata is only added to the router provider", () => {
	const previous = { ...process.env };
	try {
		delete process.env.WEAVE_PI_SUBAGENT;
		const listeners: Array<(event: { payload: unknown }, ctx: ExtensionContext) => unknown> = [];
		const extension = {
			on: (event: string, listener: typeof listeners[number]) => {
				assert.equal(event, "before_provider_request");
				listeners.push(listener);
			},
		} as unknown as ExtensionAPI;
		registerMetadata(extension);
		assert.equal(listeners.length, 1);
		for (const provider of [PROVIDER_NAME, "anthropic", "openai", undefined]) {
			const ctx = {
				model: provider ? { provider } : undefined,
				sessionManager: { getSessionId: () => "test-session" },
			} as ExtensionContext;
			const payload = { messages: [], metadata: { user_id: "caller-user" } };
			assert.equal(listeners[0]({ payload }, ctx), payload);
			assert.equal(payload.metadata.user_id, "pi:test-session");
			if (provider === PROVIDER_NAME) {
				assert.deepEqual((payload as { weave_routing?: unknown }).weave_routing, {
					conversation_id: "pi:test-session",
					thread_id: "test-session",
					parent_thread_id: undefined,
				});
			} else {
				assert.equal("weave_routing" in payload, false);
			}
		}
	} finally {
		process.env = previous;
	}
});

test("Pi keeps root controls across resume and allocates a fresh fork thread", () => {
	const previous = { ...process.env };
	try {
		delete process.env.WEAVE_PI_SUBAGENT;
		let sessionId = "parent-session";
		const ctx = { sessionManager: { getSessionId: () => sessionId } } as ExtensionContext;
		const parent = routingThread(ctx);
		assert.equal(parent.conversation_id, "pi:parent-session");
		assert.deepEqual(routingThread(ctx), parent);
		process.env.WEAVE_PI_SUBAGENT = "1";
		process.env.WEAVE_PI_CONVERSATION_ID = parent.conversation_id;
		process.env.WEAVE_PI_PARENT_THREAD_ID = parent.thread_id;
		sessionId = "child-session";
		const child = routingThread(ctx);
		assert.equal(child.conversation_id, parent.conversation_id);
		assert.equal(child.parent_thread_id, parent.thread_id);
		assert.notEqual(child.thread_id, parent.thread_id);
		assert.deepEqual(routingThread(ctx), child);
		sessionId = "forked-child-session";
		assert.notEqual(routingThread(ctx).thread_id, child.thread_id);
		assert.equal(routingThread(ctx).conversation_id, parent.conversation_id);
		process.env.WEAVE_PI_PARENT_THREAD_ID = child.thread_id;
		assert.equal(routingThread(ctx).parent_thread_id, child.thread_id);
	} finally {
		process.env = previous;
	}
});
