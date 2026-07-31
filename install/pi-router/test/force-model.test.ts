import assert from "node:assert/strict";
import test from "node:test";
import type {
	ExtensionAPI,
	ExtensionCommandContext,
	SessionEntry,
} from "@mariozechner/pi-coding-agent";
import {
	forcedModelFromBranch,
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
function messageEntry(role: "user" | "assistant", content: string | Array<{ type: "text"; text: string }>): SessionEntry {
	nextEntryId += 1;
	return {
		type: "message",
		id: `entry-${nextEntryId}`,
		parentId: null,
		timestamp: "2026-07-31T00:00:00.000Z",
		message: { role, content },
	} as unknown as SessionEntry;
}

test("registers the Claude-parity command aliases", () => {
	const { commands } = extensionHarness();
	assert.deepEqual([...commands.keys()], ["fm", "force-model", "ufm", "unforce-model"]);
});

test("forwards one canonical force-model turn and preserves trailing prompt text", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx } = commandContext();
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
	assert.equal(forcedModelFromBranch(branch), "claude-opus-4-8");

	branch.push(messageEntry("user", "/ufm"));
	branch.push(messageEntry("assistant", "✦ **Weave Router** → force-model cleared · resuming automatic model selection"));
	assert.equal(forcedModelFromBranch(branch), undefined);
});

test("server loop breaks clear stale forced status only when they evict the pin", () => {
	const branch = [
		messageEntry("user", "/fm opus"),
		messageEntry("assistant", "force-model applied: claude-opus-4-8 (anthropic)"),
		messageEntry("user", "Keep working"),
		messageEntry(
			"assistant",
			"✦ **Weave Router** → Tool-call loop detected: `read` was called 5 times in the last 10 turns with identical arguments. Stopping this turn and clearing the session pin so the next message re-routes to a fresh model.",
		),
	];
	assert.equal(forcedModelFromBranch(branch), undefined);

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
	assert.equal(forcedModelFromBranch(preserved), "claude-opus-4-8");
});

test("forced status replaces automatic routing and savings detail", () => {
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
		forcedModel: "claude-haiku-4-5",
		savings: aggregateSavings([]),
	});
	assert.equal(status, "WEAVE ROUTER — claude-haiku-4-5 [forced]");
});
