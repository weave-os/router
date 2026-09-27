/**
 * Legacy opencode integration. Subscription enrollment is handled by the
 * router (`npx @weave-os/router login claude`); this module is no longer
 * bundled by the installer.
 *
 * opencode talks to one Responses-format `weave` provider:
 *   - POST {router}/v1/responses                          (Responses wire format)
 *   - X-Weave-Router-Key: rk_...                          (from config options.headers)
 *
 * The router authenticates off X-Weave-Router-Key and managed enrollment.
 */

import type { Hooks, Plugin, PluginInput } from "@opencode-ai/plugin"
import type { AssistantMessage, Message } from "@opencode-ai/sdk"
import { CLASSIFIER_THREAD_HEADER, CLASSIFIER_UNAVAILABLE_TICKET, ClassifierThreads } from "./classifier-thread.ts"
import { rewriteDirectiveParts } from "./directives.ts"

// Provider id this legacy plugin owns. Deliberately NOT "openai"/"anthropic" —
// those ids are claimed by opencode's bundled provider plugins.
const PROVIDER_ID = "weave"
const HEADER_ROUTER_MODEL = "x-router-model"
const TOAST_TITLE = "Weave Router"
const TOAST_DURATION_MS = 6000

// Lifecycle metadata for turn classification only (title/subagent/compaction
// pin safety). Must match internal/requestcontext.OpenCodeAgentHeader; the
// router ignores it for auth, billing, and provider eligibility.
const HEADER_OPENCODE_AGENT = "X-Weave-OpenCode-Agent"
const HEADER_OPENCODE_REQUEST_ID = "X-Weave-OpenCode-Request-ID"
type OpenCodeAgent = "build" | "title" | "explore" | "compaction"
const OPENCODE_AGENTS: ReadonlySet<string> = new Set<OpenCodeAgent>(["build", "title", "explore", "compaction"])

function knownOpenCodeAgent(agent: string | undefined): OpenCodeAgent | undefined {
  return agent !== undefined && OPENCODE_AGENTS.has(agent) ? (agent as OpenCodeAgent) : undefined
}

// Placeholder so the @ai-sdk/openai provider considers auth configured. The
// router key is supplied by opencode.json and is preserved by the fetch wrapper.
const DUMMY_KEY = "weave-router-oauth"
// ---- Request provider: `weave` (Responses) ---------------------------------

function compactTokenCount(n: number): string {
  if (n >= 1000) {
    const k = n / 1000
    return `${k >= 10 ? k.toFixed(0) : k.toFixed(1).replace(/\.0$/, "")}k`
  }
  return String(n)
}

function formatCost(cost: number): string {
  if (!Number.isFinite(cost) || cost <= 0) return ""
  if (cost < 0.001) return "<$0.001"
  return `$${cost.toFixed(3).replace(/0+$/, "").replace(/\.$/, "")}`
}

function formatRoutedToast(routedModelID: string, cost: number, tokens: { input: number; output: number }): string {
  const parts = [`→ ${routedModelID}`]
  const costLabel = formatCost(cost)
  if (costLabel) parts.push(costLabel)
  const usage: string[] = []
  if (tokens.input > 0) usage.push(`${compactTokenCount(tokens.input)} in`)
  if (tokens.output > 0) usage.push(`${compactTokenCount(tokens.output)} out`)
  if (usage.length > 0) parts.push(usage.join(" / "))
  return parts.join(" · ")
}

function isCompletedWeaveAssistant(info: Message): info is AssistantMessage {
  if (info.role !== "assistant") return false
  if (info.providerID !== PROVIDER_ID) return false
  if (!info.modelID) return false
  return info.time.completed !== undefined
}

export const WeaveCodex: Plugin = async (input: PluginInput): Promise<Hooks> => {
  const classifierThreads = new ClassifierThreads()
  const pendingRequestMessageIDs = new Map<string, string>()
  const routedModelIDsByMessage = new Map<string, string>()
  const toastedMessageIDs = new Set<string>()
  return {
    "chat.message": async (_hookInput, output) => {
      rewriteDirectiveParts(output.parts)
    },
    event: async ({ event }) => {
      if (event.type === "session.created") {
        await classifierThreads.created(event.properties.info)
        return
      }
      if (event.type === "session.compacted") {
        await classifierThreads.compacted(event.properties.sessionID)
        return
      }
      if (event.type !== "message.updated") return
      const info = event.properties.info
      if (!isCompletedWeaveAssistant(info)) return
      if (toastedMessageIDs.has(info.id)) return
      toastedMessageIDs.add(info.id)
      const routedModelID = routedModelIDsByMessage.get(info.parentID) ?? info.modelID
      routedModelIDsByMessage.delete(info.parentID)
      for (const [requestID, messageID] of pendingRequestMessageIDs) {
        if (messageID === info.parentID) pendingRequestMessageIDs.delete(requestID)
      }
      try {
        await input.client.tui.showToast({
          body: {
            title: TOAST_TITLE,
            message: formatRoutedToast(routedModelID, info.cost, info.tokens),
            variant: "info",
            duration: TOAST_DURATION_MS,
          },
        })
      } catch {
        // Toast is best-effort; a TUI miss must not fail the turn.
      }
    },
    auth: {
      provider: PROVIDER_ID,
      async loader() {
        return {
          apiKey: DUMMY_KEY,
          async fetch(requestInput: RequestInfo | URL, init?: RequestInit) {
            // Preserve the configured router and lifecycle headers.
            const headers = new Headers(init?.headers as HeadersInit | undefined)

            const requestID = headers.get(HEADER_OPENCODE_REQUEST_ID)
            let routedModelCaptured = false
            try {
              const response = await fetch(requestInput, { ...init, headers })
              const messageID = requestID ? pendingRequestMessageIDs.get(requestID) : undefined
              const routedModelID = response.headers.get(HEADER_ROUTER_MODEL)
              if (messageID && routedModelID) {
                routedModelIDsByMessage.set(messageID, routedModelID)
                routedModelCaptured = true
              }
              return response
            } finally {
              if (requestID && routedModelCaptured) pendingRequestMessageIDs.delete(requestID)
            }
          },
        }
      },
      methods: [],
    },
    // The Codex backend (which the router forwards GPT turns to) keys session
    // continuity off these headers; mirror opencode's bundled codex plugin.
    // Scoped to our provider so other providers are untouched.
    "chat.headers": async (hookInput, output) => {
      if (hookInput.model.providerID !== PROVIDER_ID) return
      if (hookInput.agent !== "title") {
        // OpenCode swallows plugin hook exceptions. A failure must reach the
        // router as an invalid ticket, never as an unticketed HMM request.
        output.headers[CLASSIFIER_THREAD_HEADER] = CLASSIFIER_UNAVAILABLE_TICKET
        try {
          const enrolled = await classifierThreads.hasThread(hookInput.sessionID)
          if (!enrolled && process.env.WEAVE_OPENCODE_LLM_CLASSIFIER !== "1") {
            delete output.headers[CLASSIFIER_THREAD_HEADER]
          } else {
            if (!hookInput.provider?.options?.baseURL) throw new Error("Weave router origin unavailable")
            const routerOrigin = new URL(hookInput.provider.options.baseURL).origin
            const configuredHeaders = new Headers(hookInput.provider.options.headers as HeadersInit | undefined)
            Object.assign(output.headers, await classifierThreads.header(hookInput.sessionID, routerOrigin,
              configuredHeaders.get("X-Weave-Router-Key") ?? "", hookInput.agent))
          }
        } catch (error) {
          console.error("Classifier enrollment blocked this OpenCode request", hookInput.sessionID,
            error instanceof Error ? error.message : "unknown failure")
        }
      }
      output.headers["originator"] = "codex_cli_ts"
      output.headers["session-id"] = hookInput.sessionID
      const messageID = hookInput.message?.id
      if (messageID) {
        const requestID = crypto.randomUUID()
        pendingRequestMessageIDs.set(requestID, messageID)
        output.headers[HEADER_OPENCODE_REQUEST_ID] = requestID
      }
      // Custom agents are user-named; only OpenCode's own lifecycle agents are forwarded.
      const agent = knownOpenCodeAgent(hookInput.agent)
      if (agent) output.headers[HEADER_OPENCODE_AGENT] = agent
    },
    "chat.params": async (hookInput, output) => {
      if (hookInput.model.providerID !== PROVIDER_ID) return
      // Match codex cli: the Codex backend rejects an explicit max output cap.
      output.maxOutputTokens = undefined
    },
  }
}

export default WeaveCodex
