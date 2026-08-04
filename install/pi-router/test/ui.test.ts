import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionContext } from "@mariozechner/pi-coding-agent";
import { aggregateSavings } from "../src/savings.js";
import { clearLoomUi, installLoomUi, updateRouterStatus } from "../src/ui.js";

function uiHarness(mode: "tui" | "rpc" | "json" | "print" | undefined, hasUI: boolean) {
	let title: string | undefined;
	let header: unknown;
	let widget: unknown;
	let status: string | undefined;
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
		values: () => ({ title, header, widget, status }),
	};
}

test("Pi 0.74 TUI renders and clears Loom without ctx.mode", () => {
	const { ctx, values } = uiHarness(undefined, true);
	installLoomUi(ctx);
	updateRouterStatus(ctx, {
		requestedModel: "claude-sonnet-4-6",
		savings: aggregateSavings([]),
	});

	assert.equal(values().title, "Loom · Weave Router");
	assert.equal(typeof values().header, "function");
	assert.equal(typeof values().widget, "function");
	assert.match(values().status ?? "", /<bold>.*WEAVE ROUTER.*<\/bold>/);

	clearLoomUi(ctx);
	assert.equal(values().header, undefined);
	assert.equal(values().widget, undefined);
	assert.equal(values().status, undefined);
});

test("modern non-TUI modes do not receive terminal components", () => {
	const { ctx, values } = uiHarness("rpc", true);
	installLoomUi(ctx);
	updateRouterStatus(ctx, {
		requestedModel: "claude-sonnet-4-6",
		savings: aggregateSavings([]),
	});

	assert.equal(values().title, undefined);
	assert.equal(values().header, undefined);
	assert.equal(values().widget, undefined);
	assert.equal(values().status, "WEAVE ROUTER — claude-sonnet-4-6 · awaiting route · saved —");
});
