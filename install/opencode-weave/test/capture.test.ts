/**
 * Capture tests for the Weave opencode subscription plugin (run under bun, the
 * runtime opencode itself uses): bun test install/opencode-weave/test
 *
 * These exercise the plugin's runtime contract without a live opencode:
 *   1. the `weave` loader attaches BOTH subscriptions to a request via the
 *      router's dedicated X-Weave-*-Subscription headers, preserves the
 *      configured router-key/identity headers, and leaves Authorization alone;
 *   2. an expired Claude token (read from the on-disk weave-claude slot) is
 *      refreshed and the rotated token persisted + injected;
 *   3. the `weave-claude` login hook builds the canonical Claude OAuth flow and
 *      exchanges a pasted code.
 *
 * Type-only imports from @opencode-ai/plugin are erased at runtime, so no
 * opencode package is needed to load the module.
 */
import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { PluginInput } from "@opencode-ai/plugin"

const ANTHROPIC_TOKEN_URL = "https://token.example.test/anthropic/oauth/token"
const CHATGPT_ISSUER = "https://issuer.example.test"
const originalAnthropicTokenURL = process.env.WEAVE_ANTHROPIC_OAUTH_TOKEN
const originalChatGPTIssuer = process.env.WEAVE_CODEX_OAUTH_ISSUER

// Set the env overrides BEFORE importing the module (constants are read at load).
process.env.WEAVE_ANTHROPIC_OAUTH_TOKEN = ANTHROPIC_TOKEN_URL
process.env.WEAVE_CODEX_OAUTH_ISSUER = CHATGPT_ISSUER

const ROUTER_KEY = "rk_capture_test"
const CHATGPT_ACCESS = "eyJhbGciOiJChatGPTaccessJWT"
const CHATGPT_ACCOUNT = "acct-1234-5678"
const CLAUDE_ACCESS = "sk-ant-oat01-claude-access"
const CLAUDE_REFRESH = "claude-refresh-token"
const PLACEHOLDER_AUTHORIZATION = "Bearer weave-router-oauth"
const SESSION_ID = "ses_capture_test"
const ORIGINATOR = "codex_cli_ts"
const OPENCODE_AGENT_HEADER = "x-weave-opencode-agent"
const OPENCODE_AGENTS = ["build", "title", "explore", "compaction"] as const
const SECRET_VALUES = [
  ROUTER_KEY,
  CHATGPT_ACCESS,
  CHATGPT_ACCOUNT,
  CLAUDE_ACCESS,
  CLAUDE_REFRESH,
  "cg-refresh",
  "cg-refresh-2",
  "expired-chatgpt",
  "stale-chatgpt",
  "expired-claude",
  "sk-ant-oat01-stale",
  "sk-ant-oat01-rotated",
  "claude-refresh-2",
  "nearly-expired",
  "rotatedChatGPTaccessJWT",
] as const

const realFetch = globalThis.fetch
const realConsole = {
  log: console.log,
  info: console.info,
  warn: console.warn,
  error: console.error,
  debug: console.debug,
}
const originalAuthFile = process.env.WEAVE_OPENCODE_AUTH_FILE
type LogMethod = (...args: unknown[]) => void
let authDir: string
let authFile: string
let setCalls: Array<{ id: string; body: Record<string, unknown> }>
let appLogCalls: string[]
let consoleCalls: string[]

interface CapturedRequest {
  url: string
  headers: Record<string, string>
}

function stringifyLogArg(value: unknown): string {
  if (typeof value === "string") return value
  if (value instanceof Error) return value.stack ?? value.message
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function recordConsole(...args: unknown[]): void {
  consoleCalls.push(args.map(stringifyLogArg).join(" "))
}

function fakeInput(): PluginInput {
  return {
    client: {
      auth: {
        async set(opts: { path: { id: string }; body: Record<string, unknown> }) {
          setCalls.push({ id: opts.path.id, body: opts.body })
          const raw = JSON.parse(await readFile(authFile, "utf8")) as Record<string, unknown>
          raw[opts.path.id] = opts.body
          await writeFile(authFile, JSON.stringify(raw))
        },
      },
      app: {
        log(...args: unknown[]) {
          appLogCalls.push(args.map(stringifyLogArg).join(" "))
        },
      },
    },
  } as unknown as PluginInput
}

function installConsoleSpies(): void {
  const consoleWithMethods = console as unknown as Record<string, LogMethod>
  for (const method of Object.keys(realConsole)) consoleWithMethods[method] = recordConsole
}

function restoreConsoleSpies(): void {
  const consoleWithMethods = console as unknown as Record<string, LogMethod>
  for (const [method, original] of Object.entries(realConsole)) consoleWithMethods[method] = original
}

function assertNoSecretLeak(): void {
  for (const line of [...consoleCalls, ...appLogCalls]) {
    for (const secret of SECRET_VALUES) expect(line).not.toContain(secret)
  }
}

function assertRetainedIdentityHeaders(headers: Record<string, string>): void {
  expect(headers["x-weave-router-key"]).toBe(ROUTER_KEY)
  expect(headers["x-app"]).toBe("opencode")
  expect(headers["authorization"]).toBe(PLACEHOLDER_AUTHORIZATION)
  expect(headers["session-id"]).toBe(SESSION_ID)
  expect(headers.originator).toBe(ORIGINATOR)
}

beforeEach(async () => {
  process.env.WEAVE_ANTHROPIC_OAUTH_TOKEN = ANTHROPIC_TOKEN_URL
  process.env.WEAVE_CODEX_OAUTH_ISSUER = CHATGPT_ISSUER
  authDir = await mkdtemp(join(tmpdir(), "weave-auth-"))
  authFile = join(authDir, "auth.json")
  process.env.WEAVE_OPENCODE_AUTH_FILE = authFile
  setCalls = []
  appLogCalls = []
  consoleCalls = []
  installConsoleSpies()
})

afterEach(async () => {
  try {
    assertNoSecretLeak()
  } finally {
    globalThis.fetch = realFetch
    restoreConsoleSpies()
    if (originalAuthFile === undefined) delete process.env.WEAVE_OPENCODE_AUTH_FILE
    else process.env.WEAVE_OPENCODE_AUTH_FILE = originalAuthFile
    if (originalAnthropicTokenURL === undefined) delete process.env.WEAVE_ANTHROPIC_OAUTH_TOKEN
    else process.env.WEAVE_ANTHROPIC_OAUTH_TOKEN = originalAnthropicTokenURL
    if (originalChatGPTIssuer === undefined) delete process.env.WEAVE_CODEX_OAUTH_ISSUER
    else process.env.WEAVE_CODEX_OAUTH_ISSUER = originalChatGPTIssuer
    await rm(authDir, { recursive: true, force: true })
  }
})

// Drive the loader's fetch once and capture the upstream request. tokenResponder
// answers any OAuth /oauth/token call (ChatGPT or Anthropic) so refreshes work.
// Run the production chat.headers hook the way OpenCode does for the weave provider.
async function chatHeaders(
  hooks: Awaited<ReturnType<typeof import("../src/index.ts").WeaveCodex>>,
  agent: string | undefined,
): Promise<Record<string, string>> {
  const output: { headers: Record<string, string> } = { headers: {} }
  await hooks["chat.headers"]?.({ sessionID: SESSION_ID, agent, model: { providerID: "weave" } } as never, output)
  expect(output.headers.originator).toBe(ORIGINATOR)
  expect(output.headers["session-id"]).toBe(SESSION_ID)
  return output.headers
}

async function runLoaderFetch(
  getAuth: () => Promise<Record<string, unknown>>,
  tokenResponder?: (url: string) => unknown,
  agent = "build",
): Promise<CapturedRequest> {
  const { WeaveCodex } = await import("../src/index.ts")
  const hooks = await WeaveCodex(fakeInput())
  const sessionHeaders = { headers: await chatHeaders(hooks, agent) }
  const loaded = await hooks.auth!.loader!(getAuth as never, {} as never)

  let captured: CapturedRequest | undefined
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString()
    if (new URL(url).pathname.endsWith("/oauth/token")) {
      const body = tokenResponder ? tokenResponder(url) : {}
      // A responder returning null/undefined for a token URL simulates a failed
      // refresh (non-2xx), so tests can exercise the resilience paths.
      if (body === null || body === undefined) {
        return new Response("refresh failed", { status: 500 })
      }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }
    const headers: Record<string, string> = {}
    new Headers(init?.headers as HeadersInit).forEach((v, k) => (headers[k] = v))
    captured = { url, headers }
    return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })
  }) as typeof fetch

  // opencode's @ai-sdk provider sends the configured headers + an Authorization
  // bearer built from the (placeholder) apiKey.
  await (loaded.fetch as typeof fetch)("https://router.example.test/v1/responses", {
    method: "POST",
    headers: {
      ...sessionHeaders.headers,
      "X-Weave-Router-Key": ROUTER_KEY,
      "X-App": "opencode",
      Authorization: PLACEHOLDER_AUTHORIZATION,
    },
  })
  if (!captured) throw new Error("upstream request was not captured")
  assertRetainedIdentityHeaders(captured.headers)
  expect(captured.headers[OPENCODE_AGENT_HEADER]).toBe(agent)
  assertNoSecretLeak()
  return captured
}

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
    expect(headers).toEqual({ originator: ORIGINATOR, "session-id": SESSION_ID })
  })

  test("omits the header when no agent is provided", async () => {
    const { WeaveCodex } = await import("../src/index.ts")
    const headers = await chatHeaders(await WeaveCodex(fakeInput()), undefined)
    expect(headers).toEqual({ originator: ORIGINATOR, "session-id": SESSION_ID })
  })

  test("leaves other providers' requests untouched", async () => {
    const { WeaveCodex } = await import("../src/index.ts")
    const hooks = await WeaveCodex(fakeInput())
    const output: { headers: Record<string, string> } = { headers: { "x-existing": "kept" } }
    await hooks["chat.headers"]?.({ sessionID: SESSION_ID, agent: "title", model: { providerID: "anthropic" } } as never, output)
    expect(output.headers).toEqual({ "x-existing": "kept" })
  })

  test("the loader carries the lifecycle header alongside the subscriptions", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": { type: "oauth", access: CLAUDE_ACCESS, refresh: CLAUDE_REFRESH, expires: Date.now() + 3_600_000 },
      }),
    )
    const getAuth = async () => ({
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    })
    const req = await runLoaderFetch(getAuth, undefined, "compaction")
    expect(req.headers[OPENCODE_AGENT_HEADER]).toBe("compaction")
    expect(req.headers["x-weave-openai-subscription"]).toBe(CHATGPT_ACCESS)
    expect(req.headers["x-weave-anthropic-subscription"]).toBe(CLAUDE_ACCESS)
  })

  test("rewrites $rf into a leading-space router-feedback prompt", async () => {
    const { WeaveCodex } = await import("../src/index.ts")
    const hooks = await WeaveCodex(fakeInput())
    const parts = [{ type: "text" as const, text: "$rf - too slow" }]
    await hooks["chat.message"]?.(
      { sessionID: SESSION_ID },
      { message: { role: "user" }, parts } as never,
    )
    expect(parts[0].text).toBe(" /router-feedback - too slow")
  })
})

describe("weave loader — dual subscription injection", () => {
  test("attaches both subs via dedicated headers, preserves router key, leaves Authorization", async () => {
    // Claude slot present + unexpired on disk.
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": { type: "oauth", access: CLAUDE_ACCESS, refresh: CLAUDE_REFRESH, expires: Date.now() + 3_600_000 },
      }),
    )
    // ChatGPT (own provider) auth: unexpired.
    const getAuth = async () => ({
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    })

    const req = await runLoaderFetch(getAuth)

    expect(req.headers["x-weave-openai-subscription"]).toBe(CHATGPT_ACCESS)
    expect(req.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
    expect(req.headers["x-weave-anthropic-subscription"]).toBe(CLAUDE_ACCESS)
    // Neither subscription leaks into Authorization (router resolves per model).
    expect(req.headers["authorization"]).not.toContain(CHATGPT_ACCESS)
    expect(req.headers["authorization"]).not.toContain(CLAUDE_ACCESS)
    // No refresh happened (tokens were fresh).
    expect(setCalls).toHaveLength(0)
  })

  test("ChatGPT-only (no Claude connected) attaches only the OpenAI sub", async () => {
    await writeFile(authFile, JSON.stringify({})) // no weave-claude slot
    const getAuth = async () => ({
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    })

    const req = await runLoaderFetch(getAuth)

    expect(req.headers["x-weave-openai-subscription"]).toBe(CHATGPT_ACCESS)
    expect(req.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
    expect(req.headers["x-weave-anthropic-subscription"]).toBeUndefined()
  })

  test("Claude-only attaches the Anthropic sub without a ChatGPT login", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": { type: "oauth", access: CLAUDE_ACCESS, refresh: CLAUDE_REFRESH, expires: Date.now() + 3_600_000 },
      }),
    )
    const req = await runLoaderFetch(async () => ({ type: "api", key: "router-config-key" }))

    expect(req.headers["x-weave-anthropic-subscription"]).toBe(CLAUDE_ACCESS)
    expect(req.headers["x-weave-openai-subscription"]).toBeUndefined()
    expect(req.headers["x-weave-openai-account-id"]).toBeUndefined()
  })

  test("refreshes a ChatGPT token before its remaining lifetime is too short for a turn", async () => {
    await writeFile(authFile, JSON.stringify({}))
    const getAuth = async () => ({
      type: "oauth",
      access: "nearly-expired",
      refresh: "cg-refresh",
      expires: Date.now() + 30_000,
      accountId: CHATGPT_ACCOUNT,
    })
    const rotated = "eyJhbGciOiJrotatedChatGPTaccessJWT"

    const req = await runLoaderFetch(getAuth, (url) => {
      if (url === `${CHATGPT_ISSUER}/oauth/token`) {
        return {
          id_token: "",
          access_token: rotated,
          refresh_token: "cg-refresh-2",
          expires_in: 3600,
        }
      }
      return {}
    })

    expect(req.headers["x-weave-openai-subscription"]).toBe(rotated)
    expect(req.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
    const chatgptSet = setCalls.find((call) => call.id === "weave")
    expect(chatgptSet?.body.access).toBe(rotated)
    expect(chatgptSet?.body.refresh).toBe("cg-refresh-2")
  })

  test("refreshes an already-expired ChatGPT token, not only a near-expiry token", async () => {
    await writeFile(authFile, JSON.stringify({}))
    const getAuth = async () => ({
      type: "oauth",
      access: "expired-chatgpt",
      refresh: "cg-refresh",
      expires: Date.now() - 5_000,
      accountId: CHATGPT_ACCOUNT,
    })
    const rotated = "eyJhbGciOiJexpiredRotatedChatGPT"

    const req = await runLoaderFetch(getAuth, (url) => {
      if (url === `${CHATGPT_ISSUER}/oauth/token`) {
        return {
          id_token: "",
          access_token: rotated,
          refresh_token: "cg-refresh-expired",
          expires_in: 3600,
        }
      }
      return {}
    })

    expect(req.headers["x-weave-openai-subscription"]).toBe(rotated)
    expect(req.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
    const chatgptSet = setCalls.find((call) => call.id === "weave")
    expect(chatgptSet?.body.access).toBe(rotated)
    expect(chatgptSet?.body.refresh).toBe("cg-refresh-expired")
  })

  test("fresh credentials do not invoke either refresh endpoint", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": {
          type: "oauth",
          access: CLAUDE_ACCESS,
          refresh: CLAUDE_REFRESH,
          expires: Date.now() + 3_600_000,
        },
      }),
    )
    const getAuth = async () => ({
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    })
    let refreshCalls = 0

    const req = await runLoaderFetch(getAuth, () => {
      refreshCalls += 1
      return {}
    })

    expect(refreshCalls).toBe(0)
    expect(setCalls).toHaveLength(0)
    expect(req.headers["x-weave-openai-subscription"]).toBe(CHATGPT_ACCESS)
    expect(req.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
    expect(req.headers["x-weave-anthropic-subscription"]).toBe(CLAUDE_ACCESS)
  })

  test("a failed ChatGPT refresh still attaches the Claude sub (and doesn't fail the turn)", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": { type: "oauth", access: CLAUDE_ACCESS, refresh: CLAUDE_REFRESH, expires: Date.now() + 3_600_000 },
      }),
    )
    // ChatGPT token is expired → triggers a refresh; the issuer returns 500.
    const getAuth = async () => ({
      type: "oauth",
      access: "stale",
      refresh: "cg-refresh",
      expires: Date.now() - 1000,
      accountId: CHATGPT_ACCOUNT,
    })

    const req = await runLoaderFetch(getAuth, (url) => (url === `${CHATGPT_ISSUER}/oauth/token` ? undefined : {}))

    // ChatGPT refresh failed → no OpenAI headers, but the turn still went out…
    expect(req.headers["x-weave-openai-subscription"]).toBeUndefined()
    expect(req.headers["x-weave-openai-account-id"]).toBeUndefined()
    // …with the (unaffected) Claude sub attached.
    expect(req.headers["x-weave-anthropic-subscription"]).toBe(CLAUDE_ACCESS)
  })

  test("does not inject an expired Claude token that has no refresh token", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": { type: "oauth", access: "expired-claude", expires: Date.now() - 1000 },
      }),
    )
    const getAuth = async () => ({
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    })

    const req = await runLoaderFetch(getAuth)

    // Dead Claude sub (expired, unrefreshable) is dropped so the router bills the
    // Weave key rather than treating it as present.
    expect(req.headers["x-weave-anthropic-subscription"]).toBeUndefined()
    expect(req.headers["x-weave-openai-subscription"]).toBe(CHATGPT_ACCESS)
    expect(req.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
  })

  test("drops an expired Claude token when refresh fails", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": { type: "oauth", access: "sk-ant-oat01-stale", refresh: CLAUDE_REFRESH, expires: Date.now() - 1000 },
      }),
    )
    const getAuth = async () => ({
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    })

    const req = await runLoaderFetch(getAuth, (url) => (url === ANTHROPIC_TOKEN_URL ? undefined : {}))

    expect(req.headers["x-weave-anthropic-subscription"]).toBeUndefined()
    expect(req.headers["x-weave-openai-subscription"]).toBe(CHATGPT_ACCESS)
    expect(req.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
  })

  test("failed ChatGPT and Claude refreshes omit stale credentials and account id", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": {
          type: "oauth",
          access: "sk-ant-oat01-stale",
          refresh: CLAUDE_REFRESH,
          expires: Date.now() - 1_000,
        },
      }),
    )
    const getAuth = async () => ({
      type: "oauth",
      access: "stale-chatgpt",
      refresh: "cg-refresh",
      expires: Date.now() - 1_000,
      accountId: CHATGPT_ACCOUNT,
    })

    const req = await runLoaderFetch(getAuth, () => undefined)

    expect(req.headers["x-weave-openai-subscription"]).toBeUndefined()
    expect(req.headers["x-weave-openai-account-id"]).toBeUndefined()
    expect(req.headers["x-weave-anthropic-subscription"]).toBeUndefined()
    expect(setCalls).toHaveLength(0)
  })

  test("refreshes an expired Claude token, persists it to weave-claude, and injects the rotated token", async () => {
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": { type: "oauth", access: "stale", refresh: CLAUDE_REFRESH, expires: Date.now() - 1000 },
      }),
    )
    const getAuth = async () => ({
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    })

    const rotated = "sk-ant-oat01-rotated"
    const req = await runLoaderFetch(getAuth, (url) => {
      if (url === ANTHROPIC_TOKEN_URL) {
        return { access_token: rotated, refresh_token: "claude-refresh-2", expires_in: 3600 }
      }
      return {}
    })

    // Rotated token injected on the wire.
    expect(req.headers["x-weave-anthropic-subscription"]).toBe(rotated)
    // Persisted back to the weave-claude slot.
    const claudeSet = setCalls.find((c) => c.id === "weave-claude")
    expect(claudeSet).toBeDefined()
    expect(claudeSet!.body.access).toBe(rotated)
    expect(claudeSet!.body.refresh).toBe("claude-refresh-2")
  })

  test("loader remains active when the provider has no oauth (no subs attached)", async () => {
    await writeFile(authFile, JSON.stringify({}))
    const req = await runLoaderFetch(async () => ({ type: "api", key: "x" }))

    expect(req.headers["x-weave-openai-subscription"]).toBeUndefined()
    expect(req.headers["x-weave-anthropic-subscription"]).toBeUndefined()
  })

  test("loader initialized without auth resolves credentials added after login", async () => {
    await writeFile(authFile, JSON.stringify({}))
    const { WeaveCodex, WeaveClaude } = await import("../src/index.ts")
    const input = fakeInput()
    const weaveHooks = await WeaveCodex(input)
    const claudeHooks = await WeaveClaude(input)

    expect(weaveHooks.auth?.provider).toBe("weave")
    expect(weaveHooks.auth?.methods.length).toBeGreaterThan(0)
    expect(claudeHooks.auth?.provider).toBe("weave-claude")
    expect(claudeHooks.auth?.methods.length).toBeGreaterThan(0)
    const sessionOutput = { headers: await chatHeaders(weaveHooks, "build") }

    let chatgptAuth: Record<string, unknown> = { type: "api", key: "x" }
    const loaded = await weaveHooks.auth!.loader!(async () => chatgptAuth as never, {} as never)
    const captures: CapturedRequest[] = []
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString()
      const headers: Record<string, string> = {}
      new Headers(init?.headers as HeadersInit).forEach((v, k) => (headers[k] = v))
      captures.push({ url, headers })
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })
    }) as typeof fetch
    const request = {
      method: "POST",
      headers: {
        ...sessionOutput.headers,
        "X-Weave-Router-Key": ROUTER_KEY,
        "X-App": "opencode",
        Authorization: PLACEHOLDER_AUTHORIZATION,
      },
    }

    await (loaded.fetch as typeof fetch)("https://router.example.test/v1/responses", request)
    expect(captures[0]?.headers["x-weave-openai-subscription"]).toBeUndefined()
    expect(captures[0]?.headers["x-weave-anthropic-subscription"]).toBeUndefined()
    assertRetainedIdentityHeaders(captures[0]!.headers)
    expect(captures[0]?.headers[OPENCODE_AGENT_HEADER]).toBe("build")

    chatgptAuth = {
      type: "oauth",
      access: CHATGPT_ACCESS,
      refresh: "cg-refresh",
      expires: Date.now() + 3_600_000,
      accountId: CHATGPT_ACCOUNT,
    }
    await writeFile(
      authFile,
      JSON.stringify({
        "weave-claude": {
          type: "oauth",
          access: CLAUDE_ACCESS,
          refresh: CLAUDE_REFRESH,
          expires: Date.now() + 3_600_000,
        },
      }),
    )

    await (loaded.fetch as typeof fetch)("https://router.example.test/v1/responses", request)
    expect(captures[1]?.headers["x-weave-openai-subscription"]).toBe(CHATGPT_ACCESS)
    expect(captures[1]?.headers["x-weave-openai-account-id"]).toBe(CHATGPT_ACCOUNT)
    expect(captures[1]?.headers["x-weave-anthropic-subscription"]).toBe(CLAUDE_ACCESS)
    assertRetainedIdentityHeaders(captures[1]!.headers)
    expect(captures[1]?.headers[OPENCODE_AGENT_HEADER]).toBe("build")
    assertNoSecretLeak()
  })
})

describe("weave-claude login hook", () => {
  test("exposes the Claude login on its own provider with the canonical OAuth flow", async () => {
    const { WeaveClaude } = await import("../src/index.ts")
    const hooks = await WeaveClaude(fakeInput())
    expect(hooks.auth!.provider).toBe("weave-claude")
    const method = hooks.auth!.methods[0]
    expect(method.type).toBe("oauth")
    expect(method.label).toContain("Claude")

    const result = await (method as { authorize: () => Promise<{ url: string; method: string }> }).authorize()
    const url = new URL(result.url)
    expect(url.origin).toBe("https://claude.ai")
    expect(url.pathname).toBe("/oauth/authorize")
    expect(url.searchParams.get("client_id")).toBe("9d1c250a-e61b-44d9-88ed-5944d1962f5e")
    expect(url.searchParams.get("redirect_uri")).toBe("https://console.anthropic.com/oauth/code/callback")
    expect(url.searchParams.get("code_challenge_method")).toBe("S256")
    expect(result.method).toBe("code")
  })

  test("rejects a pasted code missing the #state separator with a clear error", async () => {
    const { WeaveClaude } = await import("../src/index.ts")
    const hooks = await WeaveClaude(fakeInput())
    const method = hooks.auth!.methods[0] as {
      authorize: () => Promise<{ callback: (code: string) => Promise<Record<string, unknown>> }>
    }
    const flow = await method.authorize()
    await expect(flow.callback("JUSTACODE")).rejects.toThrow(/code#state/)
  })

  test("exchanges a pasted code#state for tokens", async () => {
    const { WeaveClaude } = await import("../src/index.ts")
    const hooks = await WeaveClaude(fakeInput())
    const method = hooks.auth!.methods[0] as {
      authorize: () => Promise<{ callback: (code: string) => Promise<Record<string, unknown>> }>
    }
    const flow = await method.authorize()

    let exchangeBody: Record<string, unknown> | undefined
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      exchangeBody = JSON.parse(String(init?.body))
      return new Response(
        JSON.stringify({ access_token: CLAUDE_ACCESS, refresh_token: CLAUDE_REFRESH, expires_in: 3600 }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )
    }) as typeof fetch

    const out = await flow.callback("THECODE#THESTATE")
    expect(out.type).toBe("success")
    expect(out.access).toBe(CLAUDE_ACCESS)
    expect(out.refresh).toBe(CLAUDE_REFRESH)
    // Code/state split on the "#" the manual flow returns.
    expect(exchangeBody!.code).toBe("THECODE")
    expect(exchangeBody!.state).toBe("THESTATE")
    expect(exchangeBody!.grant_type).toBe("authorization_code")
  })
})

describe("opencode plugin-loading contract", () => {
  // opencode's loader (applyPlugin → readV1Plugin(mod, _, "server", "detect"))
  // treats a module whose `default` is a plain OBJECT with id/server/tui as a V1
  // module and loads ONLY `default.server`. Only when `default` is NOT such an
  // object does it fall back to getLegacyPlugins → Object.values(mod), loading
  // EVERY exported Plugin function. We rely on that fallback so BOTH WeaveCodex
  // (provider `weave`) and WeaveClaude (provider `weave-claude`) register from one
  // file. These assertions fail if someone converts the module to a `{ server }`
  // default export (which would silently drop the named Claude-login plugin).
  test("default export is a bare function (takes the all-exports legacy path)", async () => {
    const mod = await import("../src/index.ts")
    expect(typeof mod.default).toBe("function")
    // A function is not a record, so readV1Plugin returns undefined in detect mode.
    expect(mod.default).not.toBeNull()
    expect(["id", "server", "tui"].some((k) => k in (mod.default as object))).toBe(false)
  })

  test("exports exactly the legacy plugin functions", async () => {
    const mod = (await import("../src/index.ts")) as unknown as Record<string, unknown>
    expect(Object.keys(mod).sort()).toEqual(["WeaveClaude", "WeaveCodex", "default"].sort())
    for (const value of Object.values(mod)) expect(typeof value).toBe("function")
    expect(mod.default).toBe(mod.WeaveCodex)
  })

  test("both Plugins are exported functions that opencode's Object.values loop will load", async () => {
    const mod = (await import("../src/index.ts")) as unknown as Record<string, unknown>
    const plugins = Object.values(mod).filter((v) => typeof v === "function")
    // Dedup by reference (default === WeaveCodex), as opencode's loader does.
    const unique = new Set(plugins)
    const providers = new Set<string>()
    for (const p of unique) {
      const hooks = await (p as (i: unknown) => Promise<{ auth?: { provider: string; methods: unknown[] } }>)(fakeInput())
      expect(hooks.auth).toBeDefined()
      expect(hooks.auth!.methods.length).toBeGreaterThan(0)
      if (hooks.auth) providers.add(hooks.auth.provider)
    }
    expect(providers).toEqual(new Set(["weave", "weave-claude"]))
    assertNoSecretLeak()
  })
})
