import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionCommandContext, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { aggregateSavings } from "../src/savings.js";
import { clearLoomUi, installLoomUi, registerWooly, updateRouterStatus } from "../src/ui.js";

function uiHarness(mode: "tui" | "rpc" | "json" | "print" | undefined, hasUI: boolean) {
	let title: string | undefined;
	let header: unknown;
	let widget: unknown;
	let status: string | undefined;
	const commands = new Map<string, (args: string, ctx: ExtensionCommandContext) => Promise<void>>();
	let shutdown: (() => void) | undefined;
	const pi = {
		registerCommand(name: string, options: { handler: (args: string, ctx: ExtensionCommandContext) => Promise<void> }) {
			commands.set(name, options.handler);
		},
		on(_event: string, callback: () => void) { shutdown = callback; },
	} as unknown as ExtensionAPI;
	registerWooly(pi);
	const ctx = {
		...(mode === undefined ? {} : { mode }),
		hasUI,
		ui: {
			theme: {
				bold(text: string) {
					return `<bold>${text}</bold>`;
				},
				fg(color: string, text: string) {
					return `<${color}>${text}</${color}>`;
				},
			},
			setTitle(value: string) {
				title = value;
			},
			setHeader(value: unknown) {
				header = value;
			},
			setWidget(_key: string, value: unknown) {
				widget = value;
			},
			setStatus(_key: string, value: string | undefined) {
				status = value;
			},
		},
	} as unknown as ExtensionContext;
	return {
		ctx,
		toggleWooly: () => commands.get("wooly")!("", ctx as ExtensionCommandContext),
		shutdown: () => { clearLoomUi(ctx); shutdown?.(); },
		values: () => ({ title, header, widget, status }),
	};
}

test("Pi 0.74 TUI starts without Wooly and toggles it for the session", async () => {
	const { ctx, values, toggleWooly, shutdown } = uiHarness(undefined, true);
	installLoomUi(ctx);
	updateRouterStatus(ctx, {
		requestedModel: "claude-sonnet-4-6",
		savings: aggregateSavings([]),
	});

	assert.equal(values().title, "Loom · Weave Router");
	assert.equal(typeof values().header, "function");
	assert.equal(values().widget, undefined);
	assert.match(values().status ?? "", /<bold>.*WEAVE ROUTER.*<\/bold>/);

	await toggleWooly();
	assert.equal(typeof values().widget, "function");
	await toggleWooly();
	assert.equal(values().widget, undefined);
	await toggleWooly();
	shutdown();
	assert.equal(values().header, undefined);
	assert.equal(values().widget, undefined);
	assert.equal(values().status, undefined);
	installLoomUi(ctx);
	assert.equal(values().widget, undefined);
	await toggleWooly();
	assert.equal(typeof values().widget, "function");
});

test("modern non-TUI modes do not receive terminal components", async () => {
	const { ctx, values, toggleWooly } = uiHarness("rpc", true);
	installLoomUi(ctx);
	await toggleWooly();
	updateRouterStatus(ctx, {
		requestedModel: "claude-sonnet-4-6",
		savings: aggregateSavings([]),
	});

	assert.equal(values().title, undefined);
	assert.equal(values().header, undefined);
	assert.equal(values().widget, undefined);
	assert.equal(values().status, "WEAVE ROUTER — claude-sonnet-4-6 · awaiting route · saved —");
});
