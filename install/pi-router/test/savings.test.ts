import assert from "node:assert/strict";
import test from "node:test";
import {
	aggregateSavings,
	createSavingsEntry,
	formatSavings,
	isSavingsEntryData,
	normalizeModelId,
} from "../src/savings.js";

const usage = { input: 1_000_000, output: 0, cacheRead: 0, cacheWrite: 0 };

test("normalizes Pi, dated, and context-tier model ids", () => {
	assert.equal(normalizeModelId("weave/claude-opus-4-8[1m]"), "claude-opus-4-8");
	assert.equal(normalizeModelId("claude-sonnet-4-6-20260101"), "claude-sonnet-4-6");
});

test("calculates savings against identical usage", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "claude-sonnet-4-6", routedModel: "moonshotai/kimi-k2.7" },
		usage,
	);
	assert.equal(entry.priced, true);
	assert.equal(entry.requestedCostUsd, 3);
	assert.equal(entry.routedCostUsd, 0.95);
	assert.ok(Math.abs((entry.savingsUsd ?? 0) - 2.05) < 1e-12);
});

test("applies cache write and cache read multipliers", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "claude-sonnet-4-6", routedModel: "claude-haiku-4-5" },
		{ input: 100, output: 50, cacheRead: 100, cacheWrite: 100 },
	);
	const effectiveInput = 100 + 1.25 * 100 + 0.1 * 100;
	const expectedRequested = (effectiveInput * 3 + 50 * 15) / 1_000_000;
	const expectedRouted = (effectiveInput * 1 + 50 * 5) / 1_000_000;
	assert.equal(entry.requestedCostUsd, expectedRequested);
	assert.equal(entry.routedCostUsd, expectedRouted);
});

test("same-model routes have an exact zero delta without requiring a catalog price", () => {
	const entry = createSavingsEntry({ requestedModel: "private-model", routedModel: "private-model" }, usage);
	assert.equal(entry.priced, true);
	assert.equal(entry.savingsUsd, 0);
	assert.deepEqual(entry.unpricedModels, []);
});

test("unknown swapped models are explicit instead of fabricated zero savings", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "claude-sonnet-4-6", routedModel: "private-model" },
		usage,
	);
	assert.equal(entry.priced, false);
	assert.equal(entry.savingsUsd, undefined);
	assert.deepEqual(entry.unpricedModels, ["private-model"]);
	assert.equal(formatSavings(aggregateSavings([entry])), "saved — · 1 unpriced");
});

test("negative deltas render as added cost", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "claude-haiku-4-5", routedModel: "claude-opus-4-8" },
		usage,
	);
	assert.equal(formatSavings(aggregateSavings([entry])), "extra $4.00");
});

test("aggregates restored entry data and rejects malformed entries", () => {
	const first = createSavingsEntry(
		{ requestedModel: "claude-sonnet-4-6", routedModel: "moonshotai/kimi-k2.7" },
		usage,
	);
	const second = createSavingsEntry(
		{ requestedModel: "claude-sonnet-4-6", routedModel: "private-model" },
		usage,
	);
	const aggregate = aggregateSavings([first, second]);
	assert.equal(aggregate.pricedResponses, 1);
	assert.equal(aggregate.unpricedResponses, 1);
	assert.equal(aggregate.lastEntry, second);
	assert.equal(formatSavings(aggregate), "saved $2.05 · 1 unpriced");
	assert.equal(isSavingsEntryData(first), true);
	assert.equal(isSavingsEntryData({ ...first, usage: { input: -1 } }), false);
});
