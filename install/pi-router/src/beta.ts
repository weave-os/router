/**
 * Forward Pi's local /beta command to the router as one canonical user turn.
 *
 * The router owns the session strategy state and the enabled/disabled reply;
 * the client only validates the command shape and preserves busy-turn ordering.
 */

import type { ExtensionAPI, ExtensionCommandContext } from "@mariozechner/pi-coding-agent";

export function registerBetaCommand(pi: ExtensionAPI): void {
	pi.registerCommand("beta", {
		description: "Toggle beta HMM routing for this Weave Router session",
		handler: async (args: string, ctx: ExtensionCommandContext): Promise<void> => {
			if (args.trim()) {
				ctx.ui.notify("Usage: /beta", "warning");
				return;
			}
			pi.sendUserMessage("/beta", ctx.isIdle() ? undefined : { deliverAs: "followUp" });
		},
	});
}
