/**
 * Capture tests for the legacy OpenCode routing hooks (run with Bun).
 *
 * Subscription enrollment is managed by the router. These tests guard that
 * the unbundled source only preserves ordinary router/lifecycle headers and
 * never adds subscription credentials to a request.
 */
import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import type { PluginInput } from "@opencode-ai/plugin"

const ROUTER_KEY = "rk_capture_test"
const PLACEHOLDER_AUTHORIZATION = "Bearer weave-router-oauth"
const SESSION_ID = "ses_capture_test"
const OPENCODE_AGENT_HEADER = "x-weave-opencode-agent"
const OPENCODE_AGENTS = ["build", "title", "explore", "compaction"] as const

const realFetch = globalThis.fetch

interface CapturedRequest {
  headers: Record<string, string>
}

function fakeInput(): PluginInput {
  return { client: {} } as unknown as PluginInput
}

async function chatHeaders(
  hooks: Awaited<ReturnType<typeof import("../src/index.ts").WeaveCodex>>,
  agent: string | undefined,
): Promise<Record<string, string>> {
  const output: { headers: Record<string, string> } = { headers: {} }
  await hooks["chat.headers"]?.({ sessionID: SESSION_ID, agent, model: { providerID: "weave" } } as never, output)
  expect(output.headers.originator).toBe("codex_cli_ts")
  expect(output.headers["session-id"]).toBe(SESSION_ID)
  return output.headers
}

async function runLoaderFetch(agent = "build"): Promise<CapturedRequest> {
  const { WeaveCodex } = await import("../src/index.ts")
  const hooks = await WeaveCodex(fakeInput())
  const loaded = await hooks.auth!.loader!(async () => ({ type: "api", key: "router-key" }) as never, {} as never)
  const captured: CapturedRequest[] = []
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    const headers: Record<string, string> = {}
    new Headers(init?.headers as HeadersInit).forEach((value, key) => { headers[key] = value })
    captured.push({ headers })
    return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })
  }) as typeof fetch

  await (loaded.fetch as typeof fetch)("https://router.example.test/v1/responses", {
    method: "POST",
    headers: {
      ...(await chatHeaders(hooks, agent)),
      "X-Weave-Router-Key": ROUTER_KEY,
      "X-App": "opencode",
      Authorization: PLACEHOLDER_AUTHORIZATION,
    },
  })
  if (!captured[0]) throw new Error("upstream request was not captured")
  return captured[0]
}

beforeEach(() => {
  globalThis.fetch = realFetch
})

afterEach(() => {
  globalThis.fetch = realFetch
})

describe("weave chat.headers — OpenCode lifecycle metadata", () => {
  for (const agent of OPENCODE_AGENTS) {
    test(`forwards the ${agent} agent`, async () => {
      const { WeaveCodex } = await import("../src/index.ts")
      const headers = await chatHeaders(await WeaveCodex(fakeInput()), agent)
      expect(headers["X-Weave-OpenCode-Agent"]).toBe(agent)
    })
  }

  test("omits a user-defined agent name", async () => {
    const { WeaveCodex } = await import("../src/index.ts")
    const headers = await chatHeaders(await WeaveCodex(fakeInput()), "my-custom-reviewer")
    expect(Object.keys(headers).map((key) => key.toLowerCase())).not.toContain(OPENCODE_AGENT_HEADER)
    expect(headers).toEqual({ originator: "codex_cli_ts", "session-id": SESSION_ID })
  })

  test("leaves other providers' requests untouched", async () => {
    const { WeaveCodex } = await import("../src/index.ts")
    const hooks = await WeaveCodex(fakeInput())
    const output: { headers: Record<string, string> } = { headers: { "x-existing": "kept" } }
    await hooks["chat.headers"]?.({ sessionID: SESSION_ID, agent: "title", model: { providerID: "anthropic" } } as never, output)
    expect(output.headers).toEqual({ "x-existing": "kept" })
  })

  test("rewrites router feedback directives", async () => {
    const { WeaveCodex } = await import("../src/index.ts")
    const hooks = await WeaveCodex(fakeInput())
    const parts = [{ type: "text" as const, text: "$rf - too slow" }]
    await hooks["chat.message"]?.({ sessionID: SESSION_ID }, { message: { role: "user" }, parts } as never)
    expect(parts[0].text).toBe(" /router-feedback - too slow")
  })
})

describe("weave auth loader", () => {
  test("preserves configured identity and lifecycle headers", async () => {
    const request = await runLoaderFetch("compaction")
    expect(request.headers["x-weave-router-key"]).toBe(ROUTER_KEY)
    expect(request.headers["x-app"]).toBe("opencode")
    expect(request.headers.authorization).toBe(PLACEHOLDER_AUTHORIZATION)
    expect(request.headers[OPENCODE_AGENT_HEADER]).toBe("compaction")
    expect(request.headers["session-id"]).toBe(SESSION_ID)
    expect(request.headers.originator).toBe("codex_cli_ts")
  })

  test("does not add subscription or account headers", async () => {
    const request = await runLoaderFetch()
    for (const name of Object.keys(request.headers)) {
      expect(name).not.toMatch(/^x-weave-.*(?:subscription|account-id)$/i)
    }
  })
})

describe("opencode plugin-loading contract", () => {
  test("exports only the router plugin", async () => {
    const mod = (await import("../src/index.ts")) as unknown as Record<string, unknown>
    expect(Object.keys(mod).sort()).toEqual(["WeaveCodex", "default"].sort())
    expect(typeof mod.WeaveCodex).toBe("function")
    expect(mod.default).toBe(mod.WeaveCodex)
  })

  test("the router plugin has no subscription login methods", async () => {
    const { WeaveCodex } = await import("../src/index.ts")
    const hooks = await WeaveCodex(fakeInput())
    expect(hooks.auth?.provider).toBe("weave")
    expect(hooks.auth?.methods).toEqual([])
  })
})
