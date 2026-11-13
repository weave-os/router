/**
 * The `weave/auto` picker entry: what goes on the wire for it, and moving
 * installs off the per-model entries earlier releases registered.
 *
 * Pi derives a request's `model` from the selected entry's id, so an `auto`
 * request is rewritten to the model installs defaulted to before `auto`. The
 * router, escalation handoff tokens and session state therefore see the same
 * requested model as before.
 */

import * as fs from "node:fs";
import * as path from "node:path";
import type { ExtensionAPI, ExtensionContext, SessionEntry } from "@mariozechner/pi-coding-agent";
import { getAgentDir, isSubagent, PROVIDER_NAME, WEAVE_AUTO_MODEL_ID, wireModelId } from "./config.js";

/** Rewrite an `auto` request body's model to its wire id. Returns undefined when unchanged. */
export function rewriteAutoPayload(payload: unknown): Record<string, unknown> | undefined {
	if (payload === null || typeof payload !== "object" || Array.isArray(payload)) return undefined;
	const body = payload as Record<string, unknown>;
	if (typeof body.model !== "string") return undefined;
	const model = wireModelId(body.model);
	return model === body.model ? undefined : { ...body, model };
}

interface SavedDefault {
	provider?: string;
	model?: string;
}

function readSettings(file: string): Record<string, unknown> | undefined {
	try {
		const parsed: unknown = JSON.parse(fs.readFileSync(file, "utf-8"));
		return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as Record<string, unknown>) : undefined;
	} catch {
		return undefined;
	}
}

function savedDefault(settings: Record<string, unknown> | undefined): SavedDefault {
	const { defaultProvider, defaultModel } = settings ?? {};
	return {
		...(typeof defaultProvider === "string" ? { provider: defaultProvider } : {}),
		...(typeof defaultModel === "string" ? { model: defaultModel } : {}),
	};
}

function settingsFiles(cwd: string) {
	return [path.join(getAgentDir(), "settings.json"), path.join(cwd, ".pi", "settings.json")];
}

/** True when a session or saved default names a weave model this release no longer registers. */
export function isRetiredWeaveModel(provider: string | undefined, model: string | undefined): boolean {
	return provider === PROVIDER_NAME && model !== WEAVE_AUTO_MODEL_ID;
}

/**
 * Point a saved default that resolves to a retired weave model at weave/auto,
 * and return the default as it stood. Pi merges project settings over global
 * ones per field and falls back from such a default to whatever else is
 * authenticated, which can silently stop routing on every start. Only the
 * file supplying `defaultModel` changes; Pi's own writes merge per field.
 */
export function migrateSavedDefaults(cwd: string): SavedDefault {
	const files = settingsFiles(cwd).map((file) => ({ file, settings: readSettings(file) }));
	const merged = files.reduce<SavedDefault>((acc, { settings }) => ({ ...acc, ...savedDefault(settings) }), {});
	if (!isRetiredWeaveModel(merged.provider, merged.model)) return merged;
	const source = [...files].reverse().find(({ settings }) => typeof settings?.defaultModel === "string");
	if (!source) return merged;
	try {
		fs.writeFileSync(source.file, `${JSON.stringify({ ...source.settings, defaultModel: WEAVE_AUTO_MODEL_ID }, null, 2)}\n`);
	} catch {
		/* unwritable settings: the session itself still moves to weave/auto */
	}
	return merged;
}

/** The model a resumed conversation last used, read the way Pi restores it. */
function conversationModel(entries: SessionEntry[]): SavedDefault | undefined {
	if (!entries.some((entry) => entry.type === "message")) return undefined;
	let model: SavedDefault = {};
	for (const entry of entries) {
		if (entry.type === "model_change") model = { provider: entry.provider, model: entry.modelId };
		else if (entry.type === "message" && entry.message.role === "assistant") model = { provider: entry.message.provider, model: entry.message.model };
	}
	return model;
}

const LAUNCH_MODEL_FLAGS = new Set(["--model", "--provider", "--models"]);

/**
 * Whether to select weave/auto at start: the active model is a retired weave
 * entry, or Pi fell back to another provider only because the resumed
 * conversation or saved default named one. Launch flags and a model scope
 * without weave/auto are explicit choices and stay.
 */
function shouldSelectAuto(ctx: ExtensionContext, saved: SavedDefault, argv: readonly string[]): boolean {
	if (ctx.model?.provider === PROVIDER_NAME) return isRetiredWeaveModel(ctx.model.provider, ctx.model.id);
	if (argv.some((arg) => LAUNCH_MODEL_FLAGS.has(arg))) return false;
	const scoped = ctx.scopedModels ?? [];
	if (scoped.length > 0 && !scoped.some(({ model }) => model.provider === PROVIDER_NAME && model.id === WEAVE_AUTO_MODEL_ID)) return false;
	return (conversationModel(ctx.sessionManager.getBranch()) ?? saved).provider === PROVIDER_NAME;
}

export function registerAutoModel(pi: ExtensionAPI, argv: readonly string[] = process.argv): void {
	pi.on("before_provider_request", (event, ctx) => {
		if (ctx.model?.provider !== PROVIDER_NAME) return;
		return rewriteAutoPayload(event.payload);
	});

	// Children launch with an explicit model and must not write the user's settings.
	if (isSubagent()) return;
	pi.on("session_start", async (_event, ctx) => {
		const saved = migrateSavedDefaults(ctx.cwd);
		if (!shouldSelectAuto(ctx, saved, argv)) return;
		const auto = ctx.modelRegistry.find(PROVIDER_NAME, WEAVE_AUTO_MODEL_ID);
		if (auto) await pi.setModel(auto);
	});
}
