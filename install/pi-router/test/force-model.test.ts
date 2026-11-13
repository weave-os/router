import assert from "node:assert/strict";
import test from "node:test";
import type {
	ExtensionAPI,
	ExtensionCommandContext,
	SessionEntry,
} from "@mariozechner/pi-coding-agent";
import {
	lastPinFromBranch,
	parseRouterCommandHeader,
	ROUTER_COMMAND_ENTRY_TYPE,
	type RouterCommandOutcome,
	parseForceModelAcknowledgement,
	registerForceModelCommands,
} from "../src/force-model.js";
import { aggregateSavings } from "../src/savings.js";
import { updateRouterStatus } from "../src/ui.js";

type CommandOptions = Parameters<ExtensionAPI["registerCommand"]>[1];
type SendOptions = Parameters<ExtensionAPI["sendUserMessage"]>[1];

function extensionHarness() {
	const commands = new Map<string, CommandOptions>();
	const sent: Array<{ content: string; options: SendOptions }> = [];
	const pi = {
		registerCommand(name: string, options: CommandOptions) {
			commands.set(name, options);
		},
		sendUserMessage(content: string, options?: SendOptions) {
			sent.push({ content, options });
		},
	} as unknown as ExtensionAPI;
	registerForceModelCommands(pi);
	return { commands, sent };
}

function commandContext(idle = true) {
	const notifications: Array<{ message: string; level: string }> = [];
	const ctx = {
		isIdle: () => idle,
		ui: {
			notify(message: string, level: string) {
				notifications.push({ message, level });
			},
		},
	} as unknown as ExtensionCommandContext;
	return { ctx, notifications };
}

let nextEntryId = 0;
function messageEntry(role: "user" | "assistant", content: string | Array<{ type: "text"; text: string }>, responseId?: string): SessionEntry {
	nextEntryId += 1;
	return {
		type: "message",
		id: `entry-${nextEntryId}`,
		parentId: null,
		timestamp: "2026-07-31T00:00:00.000Z",
		message: { role, content, ...(role === "assistant" ? { stopReason: "stop", responseId } : {}) },
	} as unknown as SessionEntry;
}

test("registers the Claude-parity command aliases", () => {
	const { commands } = extensionHarness();
	assert.deepEqual([...commands.keys()], ["fm", "force-model", "force-parent", "ufm", "unforce-model"]);
});

test("forwards one canonical force-model turn with the argument verbatim", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx } = commandContext();
	// The extension only trims and forwards; the router owns what counts as a
	// model name. It now reads the whole argument as the name, so "haiku fix
	// the tests" is rejected there rather than split into a pin plus a prompt.
	await commands.get("fm")?.handler("  haiku fix the tests  ", ctx);
	await commands.get("force-model")?.handler("gpt-5", ctx);
	assert.deepEqual(sent, [
		{ content: "/force-model haiku fix the tests", options: undefined },
		{ content: "/force-model gpt-5", options: undefined },
	]);
});

test("queues commands as follow-ups while Pi is streaming", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx } = commandContext(false);
	await commands.get("ufm")?.handler("", ctx);
	assert.deepEqual(sent, [{ content: "/unforce-model", options: { deliverAs: "followUp" } }]);
});

test("missing force-model arguments warn without starting a turn", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx, notifications } = commandContext();
	await commands.get("fm")?.handler("   ", ctx);
	assert.deepEqual(sent, []);
	assert.deepEqual(notifications, [{ message: "Usage: /fm <model-id>", level: "warning" }]);
});

test("parses applied, cleared, and rejected router acknowledgements", () => {
	assert.deepEqual(
		parseForceModelAcknowledgement(
			"✦ **Weave Router** → force-model applied: claude-haiku-4-5 (anthropic) · Use /unforce-model to clear",
		),
		{ kind: "applied", model: "claude-haiku-4-5" },
	);
	assert.deepEqual(parseForceModelAcknowledgement("Weave Router: force-model cleared; resuming automatic routing"), {
		kind: "cleared",
	});
	assert.deepEqual(parseForceModelAcknowledgement("force-model: \"nope\" isn't a recognized model"), {
		kind: "noop",
	});
});

test("restores the newest effective pin and ignores quoted acknowledgements", () => {
	const branch = [
		messageEntry("user", "/force-model opus"),
		messageEntry("assistant", "force-model applied: claude-opus-4-8 (anthropic)"),
		messageEntry("user", "/force-model private-model"),
		messageEntry("assistant", "force-model: \"private-model\" isn't a recognized model"),
		messageEntry("user", "Someone said: /force-model haiku"),
		messageEntry("assistant", [{ type: "text", text: "force-model applied: fake-model (quoted)" }]),
	];
	assert.equal(lastPinFromBranch(branch), "claude-opus-4-8");

	branch.push(messageEntry("user", "/ufm"));
	branch.push(messageEntry("assistant", "✦ **Weave Router** → force-model cleared · resuming automatic model selection"));
	assert.equal(lastPinFromBranch(branch), undefined);
});

test("restores timestamp-based legacy acknowledgments but never infers new structured responses from prose", () => {
	const branch = [
		messageEntry("user", "/fm haiku"),
		messageEntry("assistant", "force-model applied: claude-haiku-4-5 (anthropic)", "msg_router_cmd_18bead3d5f15e800"),
	];
	assert.equal(lastPinFromBranch(branch), "claude-haiku-4-5");
	branch.push(
		messageEntry("user", "/fm opus"),
		messageEntry("assistant", "force-model applied: fabricated-model (fabricated)", "msg_router_cmd_38d8f277-e561-4937-82c9-fb6de407eb15"),
	);
	assert.equal(lastPinFromBranch(branch), "claude-haiku-4-5");
});

test("legacy loop prose cannot clear acknowledged pin history", () => {
	const branch = [
		messageEntry("user", "/fm opus"),
		messageEntry("assistant", "force-model applied: claude-opus-4-8 (anthropic)"),
		messageEntry("user", "Keep working"),
		messageEntry(
			"assistant",
			"✦ **Weave Router** → Tool-call loop detected: `read` was called 5 times in the last 10 turns with identical arguments. Stopping this turn and clearing the session pin so the next message re-routes to a fresh model.",
		),
	];
	assert.equal(lastPinFromBranch(branch), "claude-opus-4-8");

	const preserved = [
		messageEntry("user", "/fm opus"),
		messageEntry("assistant", "force-model applied: claude-opus-4-8 (anthropic)"),
		messageEntry("user", "Keep working"),
		messageEntry(
			"assistant",
			"✦ **Weave Router** → No-progress loop detected: 3 consecutive requests under this session routed to `claude-opus-4-8` (`anthropic`) with no observable progress in 2m0s. Stopping this turn and preserving the explicit force-model pin for the next message.",
		),
		messageEntry(
			"assistant",
			"Quoted example: ✦ **Weave Router** → Tool-call loop detected and clearing the session pin.",
		),
	];
	assert.equal(lastPinFromBranch(preserved), "claude-opus-4-8");
});

test("historical pin stays separate from route and savings detail", () => {
	let status: string | undefined;
	const ctx = {
		hasUI: true,
		mode: "rpc",
		ui: {
			setStatus(_key: string, value: string | undefined) {
				status = value;
			},
		},
	} as unknown as ExtensionCommandContext;
	updateRouterStatus(ctx, {
		requestedModel: "claude-sonnet-4-6",
		routedModel: "moonshotai/kimi-k2.7",
		lastPinModel: "claude-haiku-4-5",
		savings: aggregateSavings([]),
	});
	assert.equal(status, "WEAVE ROUTER — moonshotai/kimi-k2.7 ← claude-sonnet-4-6 · saved — · last pin: claude-haiku-4-5");
});

function commandEntry(outcome: RouterCommandOutcome): SessionEntry {
	return { type: "custom", customType: ROUTER_COMMAND_ENTRY_TYPE, data: outcome } as SessionEntry;
}

const APPLIED_OUTCOME: RouterCommandOutcome = {
	version: 1, id: "ce587e4c-058f-49b0-bf7d-2ead4ab65211", command: "force_model", disposition: "accepted",
	pin: { transition: "applied", model: "claude-haiku-4-5", provider: "anthropic", effort: "high" },
};

test("structured history preserves pin through informational and rejected acknowledgments and clears explicitly", () => {
	const branch = [commandEntry(APPLIED_OUTCOME)];
	assert.equal(lastPinFromBranch(branch), "claude-haiku-4-5:high");
	for (const command of ["router_session", "router_models", "router_feedback", "force_model"] as const) {
		branch.push(commandEntry({
			version: 1, id: "87700b90-93f1-48b7-aa89-8504e513832c", command,
			disposition: command === "force_model" ? "rejected" : "accepted", pin: { transition: "unchanged" },
		}));
		assert.equal(lastPinFromBranch(branch), "claude-haiku-4-5:high", command);
		branch.pop();
	}
	branch.push(commandEntry({ ...APPLIED_OUTCOME, id: "55889b13-9736-4523-84fb-6b2fc1ac8745", pin: { transition: "cleared" } }));
	branch.push(commandEntry(APPLIED_OUTCOME));
	assert.equal(lastPinFromBranch(branch), undefined, "replayed old outcome cannot revive a cleared pin");
	assert.equal(lastPinFromBranch(branch.slice(0, 1)), "claude-haiku-4-5:high", "restoring an earlier branch restores only that branch's history");
});

test("rejects malformed command headers and inconsistent state transitions", () => {
	assert.deepEqual(parseRouterCommandHeader(Buffer.from(JSON.stringify(APPLIED_OUTCOME)).toString("base64url")), APPLIED_OUTCOME);
	for (const invalid of [
		{ ...APPLIED_OUTCOME, version: 2 },
		{ ...APPLIED_OUTCOME, id: "not-an-outcome-id" },
		{ ...APPLIED_OUTCOME, disposition: "rejected" },
		{ ...APPLIED_OUTCOME, command: "router_session" },
		{ ...APPLIED_OUTCOME, command: "loop_stop", pin: { transition: "cleared" } },
		{ ...APPLIED_OUTCOME, pin: { ...APPLIED_OUTCOME.pin, effort: "unsafe:effort" } },
		{ ...APPLIED_OUTCOME, pin: { transition: "unchanged", model: "fabricated-model" } },
		{ ...APPLIED_OUTCOME, pin: { transition: "applied", model: "unsafe\nmodel", provider: "anthropic" } },
	]) assert.equal(parseRouterCommandHeader(Buffer.from(JSON.stringify(invalid)).toString("base64url")), undefined);
	for (const invalidHeader of ["", "not-base64?", "====", "a".repeat(4097)]) assert.equal(parseRouterCommandHeader(invalidHeader), undefined);
});

test("force-parent forwards parent scope and restores its acknowledged pin", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx, notifications } = commandContext();
	await commands.get("force-parent")?.handler(" opus:high ", ctx);
	await commands.get("force-parent")?.handler(" ", ctx);
	assert.deepEqual(sent, [{ content: "/force-parent opus:high", options: undefined }]);
	assert.equal(notifications.length, 1);
	assert.equal(lastPinFromBranch([
		messageEntry("user", "/force-parent opus:high"),
		messageEntry("assistant", "force-parent applied: claude-opus-5-5:high (anthropic)"),
	]), "claude-opus-5-5:high");
});
