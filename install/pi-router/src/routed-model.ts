/**
 * Route attribution, session savings, and the interactive Loom presentation.
 *
 * The selected Pi model is only the comparison baseline. The router's response
 * headers are authoritative for the model that actually served each response.
 * We pair those headers with Pi's finalized turn usage, persist an audit entry,
 * and rebuild the reachable total whenever a session resumes or changes branch.
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
import { forcedModelFromBranch } from "./force-model.js";
import { registerWeave } from "./provider.js";
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

export function registerRoutedModel(pi: ExtensionAPI): void {
	let pendingRoutes: PendingRoute[] = [];
	let entries: SavingsEntryData[] = [];
	let savings = aggregateSavings(entries);
	let requestedModel: string | undefined;
	let routedModel: string | undefined;
	let forcedModel: string | undefined;
	let lastNotifiedModel: string | undefined;

	const refresh = (ctx: ExtensionContext) => {
		if (isSubagent()) return;
		updateRouterStatus(ctx, { requestedModel, routedModel, forcedModel, savings });
	};

	const restore = (ctx: ExtensionContext) => {
		const restored = savingsFromBranch(ctx);
		entries = restored.entries;
		savings = restored.aggregate;
		const lastEntry = restored.aggregate.lastEntry;
		requestedModel = ctx.model?.id ?? lastEntry?.requestedModel;
		routedModel =
			lastEntry && requestedModel && normalizeModelId(requestedModel) === lastEntry.requestedModel
				? lastEntry.routedModel
				: undefined;
		forcedModel = forcedModelFromBranch(ctx.sessionManager.getBranch());
		pendingRoutes = [];
		lastNotifiedModel = undefined;
	};

	pi.on("session_start", (_event, ctx: ExtensionContext) => {
		restore(ctx);
		if (!isSubagent()) installLoomUi(ctx);
		refresh(ctx);
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
		if (event.status < 200 || event.status >= 300) return;
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
		const restoredForcedModel = forcedModelFromBranch(ctx.sessionManager.getBranch());
		if (restoredForcedModel !== forcedModel) {
			forcedModel = restoredForcedModel;
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
		savings = aggregateSavings(entries);
		requestedModel = entry.requestedModel;
		routedModel = entry.routedModel;
		pi.appendEntry(SAVINGS_ENTRY_TYPE, entry);
		refresh(ctx);
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
		if (!isSubagent()) clearLoomUi(ctx);
	});
}
