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
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { getAgentDir, isSubagent, PROVIDER_NAME, WEAVE_AUTO_MODEL_ID, wireModelId } from "./config.js";

/** Rewrite an `auto` request body's model to its wire id. Returns undefined when unchanged. */
export function rewriteAutoPayload(payload: unknown): Record<string, unknown> | undefined {
	if (payload === null || typeof payload !== "object" || Array.isArray(payload)) return undefined;
	const body = payload as Record<string, unknown>;
	if (typeof body.model !== "string") return undefined;
	const model = wireModelId(body.model);
	return model === body.model ? undefined : { ...body, model };
}

export interface SavedDefault {
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

function settingsFiles(cwd: string): string[] {
	return [path.join(getAgentDir(), "settings.json"), path.join(cwd, ".pi", "settings.json")];
}

/** Pi's saved default model, with project settings overriding global ones as Pi merges them. */
export function readSavedDefault(cwd: string): SavedDefault {
	return settingsFiles(cwd).reduce<SavedDefault>((merged, file) => ({ ...merged, ...savedDefault(readSettings(file)) }), {});
}

/** True when a session or saved default names a weave model this release no longer registers. */
export function isRetiredWeaveModel(provider: string | undefined, model: string | undefined): boolean {
	return provider === PROVIDER_NAME && model !== WEAVE_AUTO_MODEL_ID;
}

/**
 * Point every settings file whose default is a retired weave model at
 * weave/auto. Pi falls back from such a default to whatever else is
 * authenticated, which can silently stop routing, and does so on every start.
 * Only `defaultModel` changes; Pi's own writes merge per field.
 */
export function migrateSavedDefaults(cwd: string): void {
	for (const file of settingsFiles(cwd)) {
		const settings = readSettings(file);
		const saved = savedDefault(settings);
		if (!settings || !isRetiredWeaveModel(saved.provider, saved.model)) continue;
		try {
			fs.writeFileSync(file, `${JSON.stringify({ ...settings, defaultModel: WEAVE_AUTO_MODEL_ID }, null, 2)}\n`);
		} catch {
			/* unwritable settings: the session itself still moves to weave/auto */
		}
	}
}

export function registerAutoModel(pi: ExtensionAPI): void {
	pi.on("before_provider_request", (event) => rewriteAutoPayload(event.payload));

	// Children launch with an explicit model and must not write the user's settings.
	if (isSubagent()) return;
	pi.on("session_start", async (_event, ctx) => {
		migrateSavedDefaults(ctx.cwd);
		if (!isRetiredWeaveModel(ctx.model?.provider, ctx.model?.id)) return;
		const auto = ctx.modelRegistry.find(PROVIDER_NAME, WEAVE_AUTO_MODEL_ID);
		if (auto) await pi.setModel(auto);
	});
}
