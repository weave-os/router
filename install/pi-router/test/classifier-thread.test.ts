import assert from "node:assert/strict";
import test, { type TestContext } from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { registerClassifierThread } from "../src/classifier-thread.js";
import { PROVIDER_NAME } from "../src/config.js";
import { registerEscalationCompaction } from "../src/escalation.js";

function enable(t: TestContext) {
	for (const [name, value] of Object.entries({ WEAVE_PI_LLM_CLASSIFIER: "1", WEAVE_ROUTER_KEY: "test-key", WEAVE_ROUTER_URL: "https://router.example.test" })) {
		const previous = process.env[name];
		process.env[name] = value;
		t.after(() => { if (previous === undefined) delete process.env[name]; else process.env[name] = previous; });
	}
}

function harness(version = "0.83.0") {
	const listeners = new Map<string, Array<(event: any, ctx: ExtensionContext) => any>>();
	const entries: any[] = [];
	const calls: { url: string; body: { new_chat_id: string }; options: RequestInit }[] = [];
	const notices: string[] = [];
	let aborted = 0;
	let status = 200;
	let respond: (() => Promise<void>) | undefined;
	const pi = {
		on: (name: string, callback: (event: any, ctx: ExtensionContext) => any) => listeners.set(name, [...listeners.get(name) ?? [], callback]),
		appendEntry: (customType: string, data: unknown) => entries.push({ type: "custom", customType, data }),
	} as unknown as ExtensionAPI;
	const ctx = {
		model: { provider: PROVIDER_NAME, id: "virtual-model" },
		sessionManager: { getSessionId: () => "session-a", getEntries: () => entries, getBranch: () => entries, getHeader: () => ({}) },
		abort: () => { aborted++; }, hasUI: true,
		ui: { notify: (message: string) => notices.push(message) },
	} as unknown as ExtensionContext;
	const request = (async (url: string, options: RequestInit) => {
		assert.ok(entries.some(entry => entry.data?.newChatId), "persist idempotency key before HTTP");
		calls.push({ url, body: JSON.parse(options.body as string), options });
		await respond?.();
		return new Response(JSON.stringify({ thread_token: `ticket-${calls.at(-1)!.body.new_chat_id}` }), { status });
	}) as typeof fetch;
	const active = registerClassifierThread(pi, request, version);
	return {
		pi, ctx, entries, calls, notices, active, aborted: () => aborted,
		status: (value: number) => { status = value; },
		defer: (callback: () => Promise<void>) => { respond = callback; },
		async emit(name: string, event: any = {}) {
			let response;
			for (const listener of listeners.get(name) ?? []) {
				const next = await listener(event, ctx);
				if (next !== undefined) { response = next; if (name === "before_provider_request") event.payload = next; }
			}
			return response;
		},
	};
}

test("fresh Pi chat obtains one ticket and restores it across resume, reload, tree and opt-out", async t => {
	enable(t);
	const h = harness();
	await h.emit("session_start", { reason: "startup" });
	const first = await h.emit("before_provider_request", { payload: { messages: [] } });
	assert.match(first.weave_classifier_thread, /^ticket-/);
	assert.equal(h.calls[0].url, "https://router.example.test/v1/router/threads");
	assert.equal(h.calls[0].options.redirect, "error");
	process.env.WEAVE_PI_LLM_CLASSIFIER = "0";
	for (const reason of ["resume", "reload"]) {
		await h.emit("session_start", { reason });
		assert.deepEqual(await h.emit("before_provider_request", { payload: { messages: [] } }), first);
	}
	await h.emit("session_tree");
	assert.deepEqual(await h.emit("before_provider_request", { payload: { messages: [] } }), first);
	assert.equal(h.calls.length, 1);
	assert.equal(h.aborted(), 0);
});

test("unticketed resumed, forked, populated, compacted, old-version and opt-out chats keep legacy strategy", async t => {
	enable(t);
	for (const reason of ["resume", "reload", "fork"]) {
		const h = harness();
		await h.emit("session_start", { reason });
		assert.equal(h.active(), false);
		assert.equal(h.calls.length, 0);
	}
	for (const type of ["message", "compaction", "branch_summary"]) {
		const h = harness(); h.entries.push({ type });
		await h.emit("session_start", { reason: "startup" });
		assert.equal(h.active(), false);
	}
	const old = harness("0.82.0");
	await old.emit("session_start", { reason: "startup" });
	assert.equal(old.active(), false);
	process.env.WEAVE_PI_LLM_CLASSIFIER = "0";
	const off = harness();
	await off.emit("session_start", { reason: "new" });
	assert.equal(await off.emit("before_provider_request", { payload: {} }), undefined);
	assert.equal(off.aborted(), 0);
});

test("failed handshake blocks dispatch and retries the persisted id, including after restart", async t => {
	enable(t);
	const h = harness(); h.status(503);
	await h.emit("session_start", { reason: "new" });
	assert.equal(await h.emit("before_provider_request", { payload: {} }), undefined);
	assert.equal(h.aborted(), 1);
	const restarted = harness(); restarted.entries.push(...h.entries);
	await restarted.emit("session_start", { reason: "resume" });
	assert.equal(restarted.calls[0].body.new_chat_id, h.calls[0].body.new_chat_id);
	assert.ok((await restarted.emit("before_provider_request", { payload: {} })).weave_classifier_thread);
	assert.equal(new Set(h.calls.map(call => call.body.new_chat_id)).size, 1);
});

test("separate new chats and child processes never reuse a parent's ticket", async t => {
	enable(t);
	const parent = harness(), child = harness();
	await parent.emit("session_start", { reason: "new" });
	await child.emit("session_start", { reason: "startup" });
	assert.notEqual(parent.calls[0].body.new_chat_id, child.calls[0].body.new_chat_id);
	parent.ctx.sessionManager.getSessionId = () => "session-b";
	await parent.emit("session_start", { reason: "fork" });
	assert.equal(parent.active(), false);
	assert.equal(await parent.emit("before_provider_request", { payload: {} }), undefined);
});

test("enrollment is not inherited by seeded parent sessions", async t => {
	enable(t);
	const h = harness();
	h.ctx.sessionManager.getHeader = () => ({ parentSession: "parent" }) as any;
	await h.emit("session_start", { reason: "startup" });
	assert.equal(h.active(), false);
});

test("classifier blocks compaction and bypasses legacy handoff requests", async t => {
	enable(t);
	const h = harness();
	registerEscalationCompaction(h.pi, (async () => { assert.fail("legacy handoff must not run"); }) as typeof fetch, "0.83.0", h.active);
	await h.emit("session_start", { reason: "new" });
	assert.ok((await h.emit("before_provider_request", { payload: {} })).weave_classifier_thread);
	assert.deepEqual(await h.emit("session_before_compact"), { cancel: true });
	assert.equal(h.calls.length, 1);
});

test("changed origin, model or malformed persisted state fails closed without leaking tickets", async t => {
	enable(t);
	const h = harness();
	await h.emit("session_start", { reason: "new" });
	process.env.WEAVE_ROUTER_URL = "https://other.example.test";
	assert.equal(await h.emit("before_provider_request", { payload: {} }), undefined);
	assert.equal(h.aborted(), 1);
	assert.equal(h.calls.length, 1);
	process.env.WEAVE_ROUTER_URL = "https://router.example.test";
	h.ctx.model!.provider = "other-provider";
	await h.emit("before_provider_request", { payload: {} });
	assert.equal(h.aborted(), 2);
	h.entries.push({ type: "custom", customType: "weave-classifier-thread", data: { sessionId: "session-a", threadToken: 17 } });
	await h.emit("session_start", { reason: "reload" });
	await h.emit("before_provider_request", { payload: {} });
	assert.equal(h.aborted(), 3);
	assert.ok(h.notices.every(notice => !notice.includes("ticket-")));
});

test("late handshake completion cannot attach a previous chat's token", async t => {
	enable(t);
	const h = harness();
	let complete!: () => void;
	h.defer(() => new Promise<void>(resolve => { complete = resolve; }));
	const starting = h.emit("session_start", { reason: "new" });
	h.ctx.sessionManager.getSessionId = () => "session-b";
	await h.emit("session_start", { reason: "resume" });
	complete(); await starting;
	assert.equal(h.active(), false);
	assert.equal(h.entries.filter(entry => entry.data?.threadToken).length, 0);
});
