import assert from "node:assert/strict";
import test, { type TestContext } from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { handoffSummaryPayload, needsEscalationCompaction, parsePreparedRoute, registerEscalationCompaction } from "../src/escalation.js";

const lowRoute = { token: "low-ticket", model: "model-small", provider: "anthropic", complexity: "low" as const };
const highRoute = { token: "high-ticket", summary_token: "summary-ticket", model: "model-large", provider: "anthropic", complexity: "high" as const };

function harness(version?: string) {
	const listeners = new Map<string, Array<(event: any, ctx: ExtensionContext) => any>>();
	const messages: string[] = [];
	const errors: string[] = [];
	const branch: any[] = [];
	let aborted = 0;
	let preparations = 0;
	let compactOptions: any;
	let selection: unknown = lowRoute;
	const pi = {
		appendEntry: (customType: string, data: unknown) => branch.push({ type: "custom", customType, data }),
		on: (name: string, callback: (event: any, ctx: ExtensionContext) => any) => {
			listeners.set(name, [...listeners.get(name) ?? [], callback]);
		},
		sendUserMessage: (message: string) => messages.push(message),
	} as unknown as ExtensionAPI;
	const ctx = {
		model: { id: "virtual-model", provider: "weave" },
		sessionManager: { getSessionId: () => "test-session", getBranch: () => branch },
		getContextUsage: () => ({ tokens: 60_000 }),
		isIdle: () => true,
		abort: () => { aborted++; },
		compact: (options: unknown) => { compactOptions = options; },
		hasUI: true,
		ui: { setStatus: () => {}, notify: (message: string) => errors.push(message) },
	} as unknown as ExtensionContext;
	const pending = registerEscalationCompaction(pi, (async () => {
		preparations++;
		return { ok: true, json: async () => selection } as Response;
	}) as typeof fetch, version);
	return {
		ctx, messages, errors, pending,
		select: (route: unknown) => { selection = route; },
		aborted: () => aborted,
		preparations: () => preparations,
		complete: () => compactOptions.onComplete(),
		reject: () => compactOptions.onError(new Error("summary failed")),
		async emit(name: string, event: any = {}) {
			let returned;
			for (const listener of listeners.get(name) ?? []) returned = await listener(event, ctx);
			return returned;
		},
	};
}

function enable(t: TestContext) {
	const oldEnabled = process.env.WEAVE_PI_ESCALATION_COMPACTION;
	const oldKey = process.env.WEAVE_ROUTER_KEY;
	delete process.env.WEAVE_PI_ESCALATION_COMPACTION;
	process.env.WEAVE_ROUTER_KEY = "offline-test-key";
	t.after(() => {
		if (oldEnabled === undefined) delete process.env.WEAVE_PI_ESCALATION_COMPACTION;
		else process.env.WEAVE_PI_ESCALATION_COMPACTION = oldEnabled;
		if (oldKey === undefined) delete process.env.WEAVE_ROUTER_KEY;
		else process.env.WEAVE_ROUTER_KEY = oldKey;
	});
}

test("escalation requires an upward class, different model, substantial history, and summary authorization", () => {
	assert.equal(needsEscalationCompaction(lowRoute, highRoute, 60_000), true);
	assert.equal(needsEscalationCompaction(undefined, highRoute, 60_000), false);
	assert.equal(needsEscalationCompaction(highRoute, lowRoute, 60_000), false);
	assert.equal(needsEscalationCompaction(lowRoute, { ...highRoute, model: lowRoute.model }, 60_000), false);
	assert.equal(needsEscalationCompaction(lowRoute, highRoute, 20_000), false);
	assert.equal(needsEscalationCompaction(lowRoute, { ...highRoute, summary_token: undefined }, 60_000), false);
});

test("handoff response validation rejects incomplete tickets and tolerates unknown classes", () => {
	assert.deepEqual(parsePreparedRoute({ bypass: true }), { bypass: true });
	assert.throws(() => parsePreparedRoute({ model: "model-large" }));
	assert.equal((parsePreparedRoute({ ...highRoute, complexity: "future-class" }) as typeof highRoute).complexity, undefined);
});

test("older Pi warns at startup and preserves routing without handoff preparation", async (t) => {
	enable(t);
	const h = harness("0.82.0");
	await h.emit("session_start");
	assert.match(h.errors[0], /requires Pi 0.83 or newer/);
	assert.equal(await h.emit("before_provider_request", { payload: { messages: [] } }), undefined);
	assert.equal(h.preparations(), 0);
	assert.equal(h.aborted(), 0);
	assert.equal(h.pending(), false);
});

test("explicit opt-out preserves provider requests without preparing a handoff", async (t) => {
	enable(t);
	process.env.WEAVE_PI_ESCALATION_COMPACTION = "0";
	const h = harness();
	assert.equal(await h.emit("before_provider_request", { payload: { messages: [] } }), undefined);
	assert.equal(h.preparations(), 0);
	assert.equal(h.aborted(), 0);
	assert.equal(h.pending(), false);
});

test("Pi defaults to handoff compaction and resumes the reserved model without preparing twice", async (t) => {
	enable(t);
	const h = harness();
	const first = await h.emit("before_provider_request", { payload: { messages: [] } });
	assert.equal(first.weave_handoff, lowRoute.token);
	await h.emit("after_provider_response", { status: 200, headers: { "x-router-model": lowRoute.model } });
	h.select(highRoute);
	assert.equal(await h.emit("before_provider_request", { payload: { messages: [] } }), undefined);
	assert.equal(h.aborted(), 1);
	assert.equal(h.pending(), true);
	assert.deepEqual(await h.emit("session_before_compact"), { cancel: true }, "Pi threshold compaction must defer to the handoff");
	const settled = h.emit("agent_settled");
	assert.equal(h.messages.length, 0);
	h.complete();
	assert.equal(h.messages.length, 1);
	const resumed = await h.emit("before_provider_request", { payload: { messages: ["compacted"] } });
	assert.equal(resumed.weave_handoff, highRoute.token);
	assert.deepEqual(resumed.messages, ["compacted"]);
	assert.equal(h.preparations(), 2);
	assert.equal(h.pending(), false);
	await h.emit("agent_settled");
	await settled;
});

test("new user input invalidates a scheduled handoff and prevents an unsolicited continuation", async (t) => {
	enable(t);
	const h = harness();
	await h.emit("before_provider_request", { payload: {} });
	await h.emit("after_provider_response", { status: 200, headers: { "x-router-model": lowRoute.model } });
	h.select(highRoute);
	await h.emit("before_provider_request", { payload: {} });
	const settled = h.emit("agent_settled");
	await h.emit("input", { source: "interactive" });
	h.complete();
	assert.equal(h.pending(), false);
	assert.equal(h.messages.length, 0);
	await settled;
});

test("failed compaction leaves Pi stopped and reports the failure", async (t) => {
	enable(t);
	const h = harness();
	await h.emit("before_provider_request", { payload: {} });
	await h.emit("after_provider_response", { status: 200, headers: { "x-router-model": lowRoute.model } });
	h.select(highRoute);
	await h.emit("before_provider_request", { payload: {} });
	const settled = h.emit("agent_settled");
	h.reject();
	assert.equal(h.pending(), false);
	assert.equal(h.messages.length, 0);
	assert.match(h.errors[0], /summary failed/);
	await settled;
});

test("invalid preflight response aborts instead of sending the full unreserved request", async (t) => {
	enable(t);
	const h = harness();
	h.select({});
	await h.emit("before_provider_request", { payload: {} });
	assert.equal(h.aborted(), 1);
	assert.equal(h.pending(), false);
	assert.equal(h.errors.length, 1);
});

test("summary requests preserve the cached prefix and do not duplicate the serialized transcript", () => {
	const original = { model: "virtual-model", max_tokens: 8192, system: "original system", tools: [{ name: "bash" }],
		thinking: { type: "enabled", budget_tokens: 4096 }, messages: [{ role: "user", content: "Original task" }] };
	const summary = handoffSummaryPayload(original, { stream: true, system: "summary system", messages: ["serialized duplicate"] }, "summary-ticket");
	assert.equal(summary.system, original.system);
	assert.deepEqual(summary.tools, original.tools);
	assert.deepEqual(summary.thinking, original.thinking);
	assert.deepEqual((summary.messages as unknown[]).slice(0, -1), original.messages);
	assert.equal(original.messages.length, 1);
	assert.doesNotMatch(JSON.stringify(summary), /serialized duplicate/);
	assert.equal(summary.weave_handoff, "summary-ticket");
});

test("restored route classes survive session resume but an unknown served model invalidates them", async (t) => {
	enable(t);
	const h = harness();
	await h.emit("before_provider_request", { payload: {} });
	await h.emit("after_provider_response", { status: 200, headers: { "x-router-model": lowRoute.model } });
	await h.emit("session_start");
	h.select(highRoute);
	await h.emit("before_provider_request", { payload: {} });
	assert.equal(h.pending(), true);
	await h.emit("input", { source: "interactive" });
	h.select(lowRoute);
	await h.emit("before_provider_request", { payload: {} });
	await h.emit("after_provider_response", { status: 200, headers: { "x-router-model": "fallback-model" } });
	await h.emit("session_start");
	h.select(highRoute);
	const dispatched = await h.emit("before_provider_request", { payload: {} });
	assert.equal(dispatched.weave_handoff, highRoute.token);
	assert.equal(h.pending(), false);
});
