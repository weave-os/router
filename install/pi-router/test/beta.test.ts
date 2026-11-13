import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionCommandContext } from "@mariozechner/pi-coding-agent";
import { registerBetaCommand } from "../src/beta.js";

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
	registerBetaCommand(pi);
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

test("registers the beta command", () => {
	const { commands } = extensionHarness();
	assert.deepEqual([...commands.keys()], ["beta"]);
});

test("forwards one canonical beta turn while Pi is idle", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx } = commandContext();
	await commands.get("beta")?.handler("", ctx);
	assert.deepEqual(sent, [{ content: "/beta", options: undefined }]);
});

test("queues beta as a follow-up while Pi is busy", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx } = commandContext(false);
	await commands.get("beta")?.handler("", ctx);
	assert.deepEqual(sent, [{ content: "/beta", options: { deliverAs: "followUp" } }]);
});

test("rejects beta arguments without starting a turn", async () => {
	const { commands, sent } = extensionHarness();
	const { ctx, notifications } = commandContext();
	await commands.get("beta")?.handler(" off ", ctx);
	assert.deepEqual(sent, []);
	assert.deepEqual(notifications, [{ message: "Usage: /beta", level: "warning" }]);
});
