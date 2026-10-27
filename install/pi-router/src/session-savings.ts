/**
 * The router's normalized session savings: what this session would have cost
 * on the client's measured comparison model, minus what it did cost.
 */

import { getRole, getRouterBaseUrl, providerHeaders, resolveRouterKey } from "./config.js";
import { formatMoney } from "./savings.js";

export interface SessionSavings {
	savingsUsd: number;
	baselineLabel: string;
}

const BASELINE_LABEL = /^[A-Za-z0-9 ._-]{1,32}$/;
const MAX_BODY_BYTES = 8192;

function isRecord(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** A cost body without normalized savings (no measurements, older router) has none to show. */
export function parseSessionSavings(body: unknown, sessionId: string): SessionSavings | undefined {
	if (!isRecord(body) || body.session_id !== sessionId) return undefined;
	const savingsUsd = body.normalized_savings_usd;
	const baselineLabel = body.savings_baseline_label;
	if (typeof savingsUsd !== "number" || !Number.isFinite(savingsUsd)) return undefined;
	if (typeof baselineLabel !== "string" || !BASELINE_LABEL.test(baselineLabel)) return undefined;
	return { savingsUsd, baselineLabel };
}

export async function fetchSessionSavings(
	sessionId: string,
	request: typeof fetch = fetch,
	signal: AbortSignal = AbortSignal.timeout(5_000),
): Promise<SessionSavings | undefined> {
	const routerKey = resolveRouterKey();
	if (!routerKey || !sessionId) return undefined;
	const response = await request(`${getRouterBaseUrl()}/v1/sessions/${encodeURIComponent(sessionId)}/cost`, {
		redirect: "error",
		headers: providerHeaders(getRole(), routerKey),
		signal,
	});
	if (!response.ok) return undefined;
	const text = await response.text();
	if (text.length > MAX_BODY_BYTES) return undefined;
	return parseSessionSavings(JSON.parse(text), sessionId);
}

export function formatSessionSavings({ savingsUsd, baselineLabel }: SessionSavings): string {
	if (savingsUsd === 0) return `no savings yet vs ${baselineLabel}`;
	if (savingsUsd < 0) return `cost ${formatMoney(savingsUsd)} more than ${baselineLabel}`;
	return `saved ${formatMoney(savingsUsd)} vs ${baselineLabel}`;
}
