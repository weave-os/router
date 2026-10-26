import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionContext } from "@mariozechner/pi-coding-agent";
import { routingThread } from "../src/metadata.js";

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
