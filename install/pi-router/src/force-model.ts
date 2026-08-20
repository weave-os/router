/**
 * Pi consumes slash commands locally, so expose the router's force-model
 * directives as extension commands and forward one canonical user turn.
 *
 * The router remains authoritative for aliases, validation, canonical model
 * ids, and pin persistence. UI state is reconstructed from command/response
 * pairs on the reachable Pi branch; this is necessary because Pi records the
 * selected model handle on assistant messages rather than the response model.
 */

import type {
	ExtensionAPI,
	ExtensionCommandContext,
	SessionEntry,
} from "@mariozechner/pi-coding-agent";

export type ForceModelTransition =
	| { kind: "applied"; model: string }
	| { kind: "cleared" }
	| { kind: "noop" };

type ForceModelDirective = "force" | "clear";

function messageText(message: unknown): string | undefined {
	if (!message || typeof message !== "object" || !("content" in message)) return undefined;
	const content = message.content;
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return undefined;
	return content
		.filter(
			(block): block is { type: "text"; text: string } =>
				Boolean(
					block &&
						typeof block === "object" &&
						"type" in block &&
						block.type === "text" &&
						"text" in block &&
						typeof block.text === "string",
				),
		)
		.map((block) => block.text)
		.join("\n");
}

export function parseForceModelDirective(text: string): ForceModelDirective | undefined {
	const command = text.trim();
	// The argument is optional: a bare /fm asks the router for the listing of
	// pinnable models. The (\s|$) boundary keeps /fmt from matching.
	if (/^\/(?:force-model|fm)(?:\s|$)/i.test(command)) return "force";
	if (/^\/(?:unforce-model|ufm)$/i.test(command)) return "clear";
	return undefined;
}

export function parseForceModelAcknowledgement(text: string): ForceModelTransition | undefined {
	const applied = /force-model applied:\s+([^\s()]+)/i.exec(text);
	if (applied) return { kind: "applied", model: applied[1] };
	if (/force-model cleared/i.test(text)) return { kind: "cleared" };
	if (/is(?:n't| not) a recognized model/i.test(text)) return { kind: "noop" };
	// The bare-command listing changes no pin state, so it must read as a
	// no-op — treating it as a failed force would drop a live pin from the UI.
	if (/force-model: pick a model by id|no models are available to pin/i.test(text)) return { kind: "noop" };
	return undefined;
}

function isSyntheticPinClear(text: string): boolean {
	return /^(?:✦ \*\*Weave Router\*\* →|Weave Router:)\s+(?:Tool-call|Repetition|No-progress) loop detected\b[\s\S]*\bclearing the session pin\b/i.test(
		text.trim(),
	);
}

/** Reconstruct the effective router pin on the currently reachable branch. */
export function forcedModelFromBranch(entries: readonly SessionEntry[]): string | undefined {
	let forcedModel: string | undefined;
	let pendingDirective: ForceModelDirective | undefined;

	for (const entry of entries) {
		if (entry.type !== "message") continue;
		if (entry.message.role === "user") {
			pendingDirective = parseForceModelDirective(messageText(entry.message) ?? "");
			continue;
		}
		if (entry.message.role !== "assistant") continue;

		const text = messageText(entry.message) ?? "";
		if (isSyntheticPinClear(text)) {
			forcedModel = undefined;
			pendingDirective = undefined;
			continue;
		}
		if (!pendingDirective) continue;

		const transition = parseForceModelAcknowledgement(text);
		if (transition?.kind === "applied" && pendingDirective === "force") forcedModel = transition.model;
		else if (transition?.kind === "cleared" && pendingDirective === "clear") forcedModel = undefined;
		// Rejected force-model commands intentionally retain the previous pin.
		pendingDirective = undefined;
	}

	return forcedModel;
}

function sendRouterCommand(pi: ExtensionAPI, command: string, ctx: ExtensionCommandContext): void {
	pi.sendUserMessage(command, ctx.isIdle() ? undefined : { deliverAs: "followUp" });
}

export function registerForceModelCommands(pi: ExtensionAPI): void {
	const forceModel = async (args: string, ctx: ExtensionCommandContext): Promise<void> => {
		// A bare /fm is forwarded, not refused: the router answers it with the
		// listing of pinnable models, which is what someone who doesn't know
		// the exact slug is asking for.
		sendRouterCommand(pi, `/force-model ${args.trim()}`.trimEnd(), ctx);
	};
	const clearForceModel = async (_args: string, ctx: ExtensionCommandContext): Promise<void> => {
		sendRouterCommand(pi, "/unforce-model", ctx);
	};

	for (const name of ["fm", "force-model"]) {
		pi.registerCommand(name, {
			description: "Pin this session to a specific model via the Weave Router",
			handler: forceModel,
		});
	}
	for (const name of ["ufm", "unforce-model"]) {
		pi.registerCommand(name, {
			description: "Clear this session's forced Weave Router model",
			handler: clearForceModel,
		});
	}
}
