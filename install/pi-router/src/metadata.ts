import { randomUUID } from "node:crypto";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { isSubagent, PROVIDER_NAME } from "./config.js";

// One id per child process so all of a subagent's requests share a single pin.
// The parent passes WEAVE_PI_SUBAGENT_ID; a standalone child falls back to a uuid.
const SUBAGENT_USER_ID = `subagent:${process.env.WEAVE_PI_SUBAGENT_ID?.trim() || randomUUID()}`;

export function registerMetadata(pi: ExtensionAPI): void {
	pi.on("before_provider_request", (event, ctx: ExtensionContext) => {
		const body = event.payload as { metadata?: { user_id?: string }; weave_routing?: RoutingThread } | undefined;
		if (!body || typeof body !== "object") return undefined;

		const userId = isSubagent() ? SUBAGENT_USER_ID : `pi:${ctx.sessionManager.getSessionId()}`;
		body.metadata = { ...(body.metadata ?? {}), user_id: userId };
		if (ctx.model?.provider === PROVIDER_NAME) body.weave_routing = routingThread(ctx);
		return body;
	});
}

interface RoutingThread {
	conversation_id: string;
	thread_id: string;
	parent_thread_id?: string;
}

export function routingThread(ctx: ExtensionContext): RoutingThread {
	const sessionId = ctx.sessionManager.getSessionId();
	return {
		conversation_id: isSubagent() ? process.env.WEAVE_PI_CONVERSATION_ID || `pi:${sessionId}` : `pi:${sessionId}`,
		thread_id: isSubagent() ? `${SUBAGENT_USER_ID}:${sessionId}` : sessionId,
		parent_thread_id: isSubagent() ? process.env.WEAVE_PI_PARENT_THREAD_ID : undefined,
	};
}
