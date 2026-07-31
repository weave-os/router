import type { ExtensionContext, Theme } from "@mariozechner/pi-coding-agent";
import type { TUI } from "@mariozechner/pi-tui";
import type { SavingsAggregate } from "./savings.js";
import { formatSavings } from "./savings.js";
import { WoolyComponent } from "./wooly.js";

const STATUS_KEY = "weave-router";
const WOOLY_WIDGET_KEY = "weave-wooly";
const BRAND_OPEN = "\x1b[38;2;255;108;71m";
const BRAND_CLOSE = "\x1b[39m";

const WEAVE_WORDMARK = ["╦ ╦╔═╗╔═╗╦  ╦╔═╗", "║║║║╣ ╠═╣╚╗╔╝║╣ ", "╚╩╝╚═╝╩ ╩ ╚╝ ╚═╝"] as const;
const LOOM_WORDMARK = [
	"██╗    ███████╗ ███████╗ ███╗   ███╗",
	"██║    ██╔═══██╗██╔═══██╗████╗ ████║",
	"██║    ██║   ██║██║   ██║██╔████╔██║",
	"██║    ██║   ██║██║   ██║██║╚██╔╝██║",
	"██████╗╚██████╔╝╚██████╔╝██║ ╚═╝ ██║",
	"╚═════╝ ╚═════╝  ╚═════╝ ╚═╝     ╚═╝",
] as const;

export interface RouterUiSnapshot {
	requestedModel?: string;
	routedModel?: string;
	forcedModel?: string;
	savings: SavingsAggregate;
}

function brand(text: string): string {
	return `${BRAND_OPEN}${text}${BRAND_CLOSE}`;
}

function headerLines(theme: Theme, width: number): string[] {
	const weave = WEAVE_WORDMARK.map((line) => theme.bold(brand(line)));
	if (width < 44) {
		return ["", ...weave, theme.bold("LOOM"), theme.fg("dim", "Weave Router · Loom for Pi"), ""];
	}
	return [
		"",
		...weave,
		...LOOM_WORDMARK.map((line) => theme.bold(line)),
		theme.bold("Weave Router · Loom for Pi"),
		"",
	];
}

export function installLoomUi(ctx: ExtensionContext): void {
	if (ctx.mode !== "tui") return;
	ctx.ui.setTitle("Loom · Weave Router");
	ctx.ui.setHeader((_tui: TUI, theme: Theme) => ({
		invalidate() {},
		render(width: number): string[] {
			return headerLines(theme, width);
		},
	}));
	ctx.ui.setWidget(WOOLY_WIDGET_KEY, (tui: TUI) => new WoolyComponent(tui), { placement: "belowEditor" });
}

export function clearLoomUi(ctx: ExtensionContext): void {
	if (!ctx.hasUI) return;
	ctx.ui.setStatus(STATUS_KEY, undefined);
	if (ctx.mode === "tui") {
		ctx.ui.setWidget(WOOLY_WIDGET_KEY, undefined);
		ctx.ui.setHeader(undefined);
	}
}

export function updateRouterStatus(ctx: ExtensionContext, snapshot: RouterUiSnapshot): void {
	if (!ctx.hasUI) return;
	const { requestedModel, routedModel, forcedModel, savings } = snapshot;
	let route: string;
	if (routedModel && requestedModel) route = `${routedModel} ← ${requestedModel}`;
	else if (routedModel) route = routedModel;
	else if (requestedModel) route = `${requestedModel} · awaiting route`;
	else route = "automatic routing";

	const label = ctx.mode === "tui" ? ctx.ui.theme.bold(brand("WEAVE ROUTER")) : "WEAVE ROUTER";
	const detail = forcedModel ? `${forcedModel} [forced]` : `${route} · ${formatSavings(savings)}`;
	ctx.ui.setStatus(STATUS_KEY, `${label} — ${ctx.mode === "tui" ? ctx.ui.theme.fg("dim", detail) : detail}`);
}
