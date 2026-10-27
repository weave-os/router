/**
 * Route attribution, session savings, and the interactive Loom presentation.
 *
 * The router's response headers are authoritative for the model that actually
 * served each response. We pair those headers with Pi's finalized turn usage and
 * persist an audit entry. The savings clause is the router's normalized session
 * figure against Pi's comparison model, refreshed after each response.
 */

import type { AssistantMessage } from "@mariozechner/pi-ai";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import {
	isSubagent,
	ROUTED_MODEL_HEADER,
	ROUTED_MODEL_STDERR_PREFIX,
	ROUTED_CONTEXT_WINDOW_HEADER,
	ROUTED_PROVIDER_HEADER,
	ROUTER_DECISION_HEADER,
	PROVIDER_NAME,
} from "./config.js";
import { parseRoutedContextWindow } from "./context-window.js";
import {
	lastPinFromBranch,
	parseRouterCommandHeader,
	ROUTER_COMMAND_ENTRY_TYPE,
	ROUTER_COMMAND_HEADER,
	type RouterCommandOutcome,
} from "./force-model.js";
import { registerWeave } from "./provider.js";
import { fetchSessionSavings, type SessionSavings } from "./session-savings.js";
import {
	aggregateSavings,
	createSavingsEntry,
	isSavingsEntryData,
	normalizeModelId,
	SAVINGS_ENTRY_TYPE,
	type RouteDecision,
	type SavingsAggregate,
	type SavingsEntryData,
} from "./savings.js";
import { clearLoomUi, installLoomUi, updateRouterStatus } from "./ui.js";

interface PendingRoute {
	requestedModel?: string;
	routedModel: string;
	provider?: string;
	decision?: string;
}

interface PendingCommand {
	turnIndex: number;
	sessionId: string;
	outcome?: RouterCommandOutcome;
}

function savingsFromBranch(ctx: ExtensionContext): { entries: SavingsEntryData[]; aggregate: SavingsAggregate } {
	const entries: SavingsEntryData[] = [];
	for (const entry of ctx.sessionManager.getBranch()) {
		if (entry.type !== "custom" || entry.customType !== SAVINGS_ENTRY_TYPE || !isSavingsEntryData(entry.data)) continue;
		entries.push(entry.data);
	}
	return { entries, aggregate: aggregateSavings(entries) };
}

function messageUsage(message: AssistantMessage) {
	return {
		input: message.usage.input,
		output: message.usage.output,
		cacheRead: message.usage.cacheRead,
		cacheWrite: message.usage.cacheWrite,
	};
}

export function registerRoutedModel(pi: ExtensionAPI, request: typeof fetch = fetch): void {
	let pendingRoutes: PendingRoute[] = [];
	let entries: SavingsEntryData[] = [];
	let requestedModel: string | undefined;
	let routedModel: string | undefined;
	let lastPinModel: string | undefined;
	let lastNotifiedModel: string | undefined;
	let activeTurnIndex: number | undefined;
	let pendingCommand: PendingCommand | undefined;
	let sessionSavings: SessionSavings | undefined;
	let savingsGeneration = 0;

	const refresh = (ctx: ExtensionContext) => {
		if (isSubagent()) return;
		updateRouterStatus(ctx, { requestedModel, routedModel, lastPinModel, ...(sessionSavings ? { sessionSavings } : {}) });
	};

	// Best effort: a failed or stale fetch keeps the last figure rather than
	// falling back to a local price comparison.
	const refreshSessionSavings = (ctx: ExtensionContext) => {
		if (isSubagent()) return;
		const generation = ++savingsGeneration;
		void (async () => {
			try {
				const sessionId = ctx.sessionManager.getSessionId();
				const fetched = await fetchSessionSavings(sessionId, request);
				if (generation !== savingsGeneration || !fetched) return;
				sessionSavings = fetched;
				refresh(ctx);
			} catch {
				/* the status line stays without a savings clause */
			}
		})();
	};

	const restore = (ctx: ExtensionContext) => {
		const restored = savingsFromBranch(ctx);
		entries = restored.entries;
		const lastEntry = restored.aggregate.lastEntry;
		requestedModel = ctx.model?.id ?? lastEntry?.requestedModel;
		routedModel =
			lastEntry && requestedModel && normalizeModelId(requestedModel) === lastEntry.requestedModel
				? lastEntry.routedModel
				: undefined;
		lastPinModel = lastPinFromBranch(ctx.sessionManager.getBranch());
		sessionSavings = undefined;
		savingsGeneration++;
		pendingRoutes = [];
		pendingCommand = undefined;
		activeTurnIndex = undefined;
		lastNotifiedModel = undefined;
	};

	pi.on("session_start", (_event, ctx: ExtensionContext) => {
		restore(ctx);
		if (!isSubagent()) installLoomUi(ctx);
		refresh(ctx);
		refreshSessionSavings(ctx);
	});

	pi.on("turn_start", (event) => {
		activeTurnIndex = event.turnIndex;
		pendingCommand = undefined;
	});

	pi.on("model_select", (event, ctx: ExtensionContext) => {
		// A new requested model has not yet received a router-confirmed window.
		// Restore the conservative virtual-model default until its first response.
		registerWeave(pi);
		if (isSubagent()) return;
		requestedModel = event.model.id;
		routedModel = undefined;
		refresh(ctx);
	});

	pi.on("after_provider_response", (event, ctx: ExtensionContext) => {
		pendingCommand = undefined;
		if (event.status < 200 || event.status >= 300) return;
		const commandHeader = event.headers?.[ROUTER_COMMAND_HEADER];
		if (commandHeader !== undefined && activeTurnIndex !== undefined && ctx.model?.provider === PROVIDER_NAME) {
			pendingCommand = {
				turnIndex: activeTurnIndex,
				sessionId: ctx.sessionManager.getSessionId(),
				outcome: parseRouterCommandHeader(commandHeader),
			};
		}
		const model = event.headers?.[ROUTED_MODEL_HEADER];
		if (!model) return;
		const contextWindow = parseRoutedContextWindow(event.headers?.[ROUTED_CONTEXT_WINDOW_HEADER]);
		if (contextWindow && ctx.model?.provider === PROVIDER_NAME) {
			registerWeave(pi, { modelId: ctx.model.id, contextWindow });
		}
		const route: PendingRoute = {
			...(ctx.model?.id ? { requestedModel: ctx.model.id } : {}),
			routedModel: normalizeModelId(model),
			...(event.headers[ROUTED_PROVIDER_HEADER] ? { provider: event.headers[ROUTED_PROVIDER_HEADER] } : {}),
			...(event.headers[ROUTER_DECISION_HEADER] ? { decision: event.headers[ROUTER_DECISION_HEADER] } : {}),
		};
		if (!isSubagent()) pendingRoutes.push(route);

		if (!ctx.hasUI || isSubagent()) {
			if (route.routedModel !== lastNotifiedModel) {
				process.stderr.write(`${ROUTED_MODEL_STDERR_PREFIX} ${route.routedModel}\n`);
				lastNotifiedModel = route.routedModel;
			}
			return;
		}

		requestedModel = route.requestedModel ?? requestedModel;
		routedModel = route.routedModel;
		refresh(ctx);
		if (route.routedModel !== lastNotifiedModel) {
			ctx.ui.notify(`Weave Router routed to ${route.routedModel}`, "info");
			lastNotifiedModel = route.routedModel;
		}
	});

	pi.on("turn_end", (event, ctx: ExtensionContext) => {
		if (isSubagent() || event.message.role !== "assistant") return;
		const command = pendingCommand;
		pendingCommand = undefined;
		if (command && command.turnIndex === event.turnIndex && command.sessionId === ctx.sessionManager.getSessionId() && event.message.stopReason === "stop") {
			const outcome = command.outcome;
			const matchingResponse = outcome && event.message.responseId === `msg_router_cmd_${outcome.id}`;
			pi.appendEntry(ROUTER_COMMAND_ENTRY_TYPE, matchingResponse ? outcome : { responseId: event.message.responseId, outcome: null });
		}
		const restoredLastPin = lastPinFromBranch(ctx.sessionManager.getBranch());
		if (restoredLastPin !== lastPinModel) {
			lastPinModel = restoredLastPin;
			// Force-model directives take effect between responses, before the next
			// served-window header can arrive. Fall back to the conservative value.
			registerWeave(pi);
			refresh(ctx);
		}
		const pending = pendingRoutes.shift();
		if (!pending) return;
		const message = event.message as AssistantMessage;
		const selected = pending.requestedModel || message.model || ctx.model?.id;
		if (!selected) return;
		const decision: RouteDecision = {
			requestedModel: selected,
			routedModel: pending.routedModel,
			...(pending.provider ? { provider: pending.provider } : {}),
			...(pending.decision ? { decision: pending.decision } : {}),
		};
		const entry = createSavingsEntry(decision, messageUsage(message));
		entries.push(entry);
		requestedModel = entry.requestedModel;
		routedModel = entry.routedModel;
		pi.appendEntry(SAVINGS_ENTRY_TYPE, entry);
		refresh(ctx);
		refreshSessionSavings(ctx);
	});

	pi.on("session_tree", (_event, ctx: ExtensionContext) => {
		// A branch can have been served by another pinned model. Do not carry that
		// window into the first request on the newly selected branch.
		registerWeave(pi);
		if (isSubagent()) return;
		restore(ctx);
		refresh(ctx);
	});

	pi.on("session_shutdown", (_event, ctx: ExtensionContext) => {
		pendingRoutes = [];
		pendingCommand = undefined;
		activeTurnIndex = undefined;
		savingsGeneration++;
		if (!isSubagent()) clearLoomUi(ctx);
	});

	pi.on("agent_end", () => {
		pendingCommand = undefined;
		activeTurnIndex = undefined;
	});
}
