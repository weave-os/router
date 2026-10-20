/**
 * Pi consumes slash commands locally, so expose the router's force-model
 * directives as extension commands and forward one canonical user turn.
 *
 * The router remains authoritative for aliases, validation, canonical model
 * ids, and pin persistence. UI history uses structured outcomes and legacy
 * command/response pairs on the reachable Pi branch because Pi records the
 * selected model handle on assistant messages rather than the response model.
 */

import type {
	ExtensionAPI,
	ExtensionCommandContext,
	SessionEntry,
} from "@mariozechner/pi-coding-agent";

export const ROUTER_COMMAND_HEADER = "x-weave-router-command";
export const ROUTER_COMMAND_ENTRY_TYPE = "weave-router-command";

const COMMAND_ID = /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i;
const ROUTER_COMMANDS = ["force_model", "router_session", "router_models", "router_feedback", "loop_stop"] as const;
type RouterCommand = (typeof ROUTER_COMMANDS)[number];
type CommandDisposition = "accepted" | "rejected";

export type RouterPinTransition =
	| { transition: "applied"; model: string; provider: string; effort?: string }
	| { transition: "cleared" | "unchanged" };

export interface RouterCommandOutcome {
	version: 1;
	id: string;
	command: RouterCommand;
	disposition: CommandDisposition;
	pin: RouterPinTransition;
}

export type ForceModelTransition =
	| { kind: "applied"; model: string }
	| { kind: "cleared" }
	| { kind: "noop" };

type ForceModelDirective = "force" | "clear";

function record(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function displayToken(value: unknown): value is string {
	return typeof value === "string" && /^[A-Za-z0-9._:/-]{1,128}$/.test(value);
}

function isRouterCommandOutcome(value: unknown): value is RouterCommandOutcome {
	if (!record(value) || value.version !== 1 || typeof value.id !== "string" ||
		!COMMAND_ID.test(value.id) ||
		!ROUTER_COMMANDS.some((command) => command === value.command) ||
		(value.disposition !== "accepted" && value.disposition !== "rejected") || !record(value.pin)) return false;
	const pin = value.pin;
	if (value.disposition === "rejected" && pin.transition !== "unchanged") return false;
	if (pin.transition === "applied") {
		return value.command === "force_model" && displayToken(pin.model) && displayToken(pin.provider) &&
			(pin.effort === undefined || (typeof pin.effort === "string" && /^[A-Za-z0-9_-]{1,32}$/.test(pin.effort)));
	}
	return (pin.transition === "unchanged" ||
		(pin.transition === "cleared" && value.command === "force_model")) &&
		pin.model === undefined && pin.provider === undefined && pin.effort === undefined;
}

export function parseRouterCommandHeader(header: string): RouterCommandOutcome | undefined {
	if (header.length === 0 || header.length > 4096 || !/^[A-Za-z0-9_-]+$/.test(header)) return undefined;
	try {
		const decoded = Buffer.from(header, "base64url");
		if (decoded.toString("base64url") !== header) return undefined;
		const outcome: unknown = JSON.parse(decoded.toString("utf8"));
		return isRouterCommandOutcome(outcome) ? outcome : undefined;
	} catch {
		return undefined;
	}
}

function messageText(message: unknown): string | undefined {
	if (!record(message)) return undefined;
	const content = message.content;
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return undefined;
	return content
		.filter((block): block is { type: "text"; text: string } =>
			record(block) && block.type === "text" && typeof block.text === "string")
		.map((block) => block.text)
		.join("\n");
}

export function parseForceModelDirective(text: string): ForceModelDirective | undefined {
	const command = text.trim();
	if (/^\/(?:force-model|fm)\s+\S+/i.test(command)) return "force";
	if (/^\/(?:unforce-model|ufm)$/i.test(command)) return "clear";
	return undefined;
}

export function parseForceModelAcknowledgement(text: string): ForceModelTransition | undefined {
	const applied = /^(?:(?:✦ \*\*Weave Router\*\* →|Weave Router:)\s+)?force-model applied:\s+([^\s()]+)/i.exec(text.trim());
	if (applied) return { kind: "applied", model: applied[1] };
	if (/^(?:(?:✦ \*\*Weave Router\*\* →|Weave Router:)\s+)?force-model cleared\b/i.test(text.trim())) return { kind: "cleared" };
	if (/is(?:n't| not) a recognized model/i.test(text)) return { kind: "noop" };
	return undefined;
}

/** Reconstruct acknowledged pin history on the reachable branch, never current server state. */
export function lastPinFromBranch(entries: readonly SessionEntry[]): string | undefined {
	let lastPin: string | undefined;
	let pendingDirective: ForceModelDirective | undefined;
	const appliedOutcomes = new Set<string>();
	const structuredResponses = new Set<string>();
	for (const entry of entries) {
		if (entry.type !== "custom" || entry.customType !== ROUTER_COMMAND_ENTRY_TYPE) continue;
		if (isRouterCommandOutcome(entry.data)) structuredResponses.add(`msg_router_cmd_${entry.data.id}`);
		else if (record(entry.data) && typeof entry.data.responseId === "string") structuredResponses.add(entry.data.responseId);
	}

	for (const [index, entry] of entries.entries()) {
		if (entry.type === "custom" && entry.customType === ROUTER_COMMAND_ENTRY_TYPE) {
			pendingDirective = undefined;
			if (!isRouterCommandOutcome(entry.data) || appliedOutcomes.has(entry.data.id)) continue;
			appliedOutcomes.add(entry.data.id);
			if (entry.data.pin.transition === "applied") {
				lastPin = entry.data.pin.model + (entry.data.pin.effort ? `:${entry.data.pin.effort}` : "");
			} else if (entry.data.pin.transition === "cleared") lastPin = undefined;
			continue;
		}
		if (entry.type !== "message") continue;
		if (entry.message.role === "user") {
			pendingDirective = parseForceModelDirective(messageText(entry.message) ?? "");
			continue;
		}
		if (entry.message.role !== "assistant") continue;
		const annotation = entries[index + 1];
		const responseId = entry.message.responseId;
		const structuredResponse = responseId?.startsWith("msg_router_cmd_") &&
			COMMAND_ID.test(responseId.slice("msg_router_cmd_".length));
		if (structuredResponse || (responseId && structuredResponses.has(responseId)) ||
			(annotation?.type === "custom" && annotation.customType === ROUTER_COMMAND_ENTRY_TYPE)) {
			// Structured responses never fall back to acknowledgment prose.
			pendingDirective = undefined;
			continue;
		}
		if (entry.message.stopReason !== "stop") {
			pendingDirective = undefined;
			continue;
		}
		if (!pendingDirective) continue;
		const transition = parseForceModelAcknowledgement(messageText(entry.message) ?? "");
		if (transition?.kind === "applied" && pendingDirective === "force") lastPin = transition.model;
		else if (transition?.kind === "cleared" && pendingDirective === "clear") lastPin = undefined;
		pendingDirective = undefined;
	}
	return lastPin;
}

function sendRouterCommand(pi: ExtensionAPI, command: string, ctx: ExtensionCommandContext): void {
	pi.sendUserMessage(command, ctx.isIdle() ? undefined : { deliverAs: "followUp" });
}

export function registerForceModelCommands(pi: ExtensionAPI): void {
	const forceModel = async (args: string, ctx: ExtensionCommandContext): Promise<void> => {
		const modelAndPrompt = args.trim();
		if (!modelAndPrompt) {
			ctx.ui.notify("Usage: /fm <model-id>", "warning");
			return;
		}
		sendRouterCommand(pi, `/force-model ${modelAndPrompt}`, ctx);
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
