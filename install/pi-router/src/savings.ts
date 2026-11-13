import { MODEL_PRICING, PRICING_VERSION } from "./pricing.generated.js";

export const SAVINGS_ENTRY_TYPE = "weave-router-savings-v1";

export interface TokenUsage {
	input: number;
	output: number;
	cacheRead: number;
	cacheWrite: number;
}

export interface RouteDecision {
	requestedModel: string;
	routedModel: string;
	provider?: string;
	decision?: string;
}

export interface SavingsEntryData {
	version: 1;
	pricingVersion: string;
	requestedModel: string;
	routedModel: string;
	provider?: string;
	decision?: string;
	usage: TokenUsage;
	requestedCostUsd?: number;
	routedCostUsd?: number;
	savingsUsd?: number;
	priced: boolean;
	unpricedModels: string[];
}

export interface SavingsAggregate {
	totalSavingsUsd: number;
	pricedResponses: number;
	unpricedResponses: number;
	lastEntry?: SavingsEntryData;
}

function finiteNonNegative(value: number): number | undefined {
	return Number.isFinite(value) && value >= 0 ? value : undefined;
}

export function normalizeModelId(model: string): string {
	return model.trim().replace(/^weave\//, "").replace(/\[[^\]]*\]$/, "").replace(/-[0-9]{8}$/, "");
}

function normalizedUsage(usage: TokenUsage): TokenUsage | undefined {
	const input = finiteNonNegative(usage.input);
	const output = finiteNonNegative(usage.output);
	const cacheRead = finiteNonNegative(usage.cacheRead);
	const cacheWrite = finiteNonNegative(usage.cacheWrite);
	if (input === undefined || output === undefined || cacheRead === undefined || cacheWrite === undefined) {
		return undefined;
	}
	return { input, output, cacheRead, cacheWrite };
}

function modelCostUsd(model: string, usage: TokenUsage): number | undefined {
	const price = MODEL_PRICING[normalizeModelId(model)];
	if (!price) return undefined;
	const inputTokens = usage.input + 1.25 * usage.cacheWrite + price.cacheReadMultiplier * usage.cacheRead;
	return (inputTokens * price.inputUsdPerMillion + usage.output * price.outputUsdPerMillion) / 1_000_000;
}

export function createSavingsEntry(decision: RouteDecision, rawUsage: TokenUsage): SavingsEntryData {
	const requestedModel = normalizeModelId(decision.requestedModel);
	const routedModel = normalizeModelId(decision.routedModel);
	const usage = normalizedUsage(rawUsage);
	const base: SavingsEntryData = {
		version: 1,
		pricingVersion: PRICING_VERSION,
		requestedModel,
		routedModel,
		...(decision.provider ? { provider: decision.provider } : {}),
		...(decision.decision ? { decision: decision.decision } : {}),
		usage: usage ?? { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		priced: false,
		unpricedModels: [],
	};

	if (!usage || !requestedModel || !routedModel) return base;

	// An unchanged route has an exact zero delta even if the catalog does not
	// know the model. Avoid calling a no-op response "unpriced" when no price
	// comparison is necessary.
	if (requestedModel === routedModel) {
		const cost = modelCostUsd(requestedModel, usage);
		return {
			...base,
			...(cost === undefined ? {} : { requestedCostUsd: cost, routedCostUsd: cost }),
			savingsUsd: 0,
			priced: true,
		};
	}

	const requestedCostUsd = modelCostUsd(requestedModel, usage);
	const routedCostUsd = modelCostUsd(routedModel, usage);
	if (requestedCostUsd === undefined || routedCostUsd === undefined) {
		const unpricedModels = [
			...(requestedCostUsd === undefined ? [requestedModel] : []),
			...(routedCostUsd === undefined ? [routedModel] : []),
		];
		return { ...base, unpricedModels };
	}

	return {
		...base,
		requestedCostUsd,
		routedCostUsd,
		savingsUsd: requestedCostUsd - routedCostUsd,
		priced: true,
	};
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}

function isTokenUsage(value: unknown): value is TokenUsage {
	if (!isRecord(value)) return false;
	return [value.input, value.output, value.cacheRead, value.cacheWrite].every(
		(token) => typeof token === "number" && Number.isFinite(token) && token >= 0,
	);
}

export function isSavingsEntryData(value: unknown): value is SavingsEntryData {
	if (!isRecord(value)) return false;
	return (
		value.version === 1 &&
		typeof value.pricingVersion === "string" &&
		typeof value.requestedModel === "string" &&
		typeof value.routedModel === "string" &&
		isTokenUsage(value.usage) &&
		typeof value.priced === "boolean" &&
		(value.provider === undefined || typeof value.provider === "string") &&
		(value.decision === undefined || typeof value.decision === "string") &&
		(value.requestedCostUsd === undefined ||
			(typeof value.requestedCostUsd === "number" && Number.isFinite(value.requestedCostUsd))) &&
		(value.routedCostUsd === undefined ||
			(typeof value.routedCostUsd === "number" && Number.isFinite(value.routedCostUsd))) &&
		Array.isArray(value.unpricedModels) &&
		value.unpricedModels.every((model) => typeof model === "string") &&
		(value.savingsUsd === undefined || (typeof value.savingsUsd === "number" && Number.isFinite(value.savingsUsd)))
	);
}

export function aggregateSavings(entries: Iterable<SavingsEntryData>): SavingsAggregate {
	let totalSavingsUsd = 0;
	let pricedResponses = 0;
	let unpricedResponses = 0;
	let lastEntry: SavingsEntryData | undefined;
	for (const entry of entries) {
		lastEntry = entry;
		if (entry.priced && entry.savingsUsd !== undefined) {
			totalSavingsUsd += entry.savingsUsd;
			pricedResponses++;
		} else {
			unpricedResponses++;
		}
	}
	return {
		totalSavingsUsd,
		pricedResponses,
		unpricedResponses,
		...(lastEntry ? { lastEntry } : {}),
	};
}

export function formatMoney(amount: number): string {
	const absolute = Math.abs(amount);
	if (absolute > 0 && absolute < 0.005) return "<$0.01";
	return `$${absolute.toFixed(2)}`;
}

export function formatSavings(aggregate: SavingsAggregate): string {
	let clause: string;
	if (aggregate.pricedResponses === 0) {
		clause = "saved —";
	} else if (aggregate.totalSavingsUsd < 0) {
		clause = `extra ${formatMoney(aggregate.totalSavingsUsd)}`;
	} else {
		clause = `saved ${formatMoney(aggregate.totalSavingsUsd)}`;
	}
	if (aggregate.unpricedResponses > 0) {
		const suffix = aggregate.unpricedResponses === 1 ? "1 unpriced" : `${aggregate.unpricedResponses} unpriced`;
		return `${clause} · ${suffix}`;
	}
	return clause;
}
