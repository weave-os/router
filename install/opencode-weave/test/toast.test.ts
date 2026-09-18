import { describe, expect, test } from "bun:test"
import type { PluginInput } from "@opencode-ai/plugin"
import type { AssistantMessage, Event, Message } from "@opencode-ai/sdk"
import { WeaveCodex } from "../src/index.ts"

function assistantInfo(overrides: Partial<AssistantMessage> = {}): AssistantMessage {
  return {
    id: "msg_1",
    sessionID: "ses_1",
    role: "assistant",
    time: { created: 1, completed: 2 },
    parentID: "msg_user",
    modelID: "auto",
    providerID: "weave",
    mode: "build",
    path: { cwd: "/", root: "/" },
    cost: 0.012,
    tokens: { input: 1200, output: 340, reasoning: 0, cache: { read: 0, write: 0 } },
    ...overrides,
  }
}

function messageUpdated(info: Message): Event {
  return { type: "message.updated", properties: { info } }
}

async function pluginWithToast(showToast: (opts: unknown) => Promise<unknown>) {
  const input = {
    client: { tui: { showToast } },
  } as unknown as PluginInput
  return WeaveCodex(input)
}

async function captureRoutedModelHeader(
  hooks: Awaited<ReturnType<typeof WeaveCodex>>,
  routedModelID?: string,
  messageID = "msg_user",
  retryBeforeSuccess = false,
): Promise<void> {
  const realFetch = globalThis.fetch
  let attempts = 0
  globalThis.fetch = (async () => {
    attempts += 1
    if (retryBeforeSuccess && attempts === 1) throw new Error("retryable response")
    const headers = routedModelID ? { "x-router-model": routedModelID } : undefined
    return new Response("{}", { headers })
  }) as unknown as typeof fetch
  try {
    const loaded = await hooks.auth!.loader!(async () => ({ type: "api", key: "test" }) as never, {} as never)
    const output: { headers: Record<string, string> } = { headers: {} }
    await hooks["chat.headers"]?.({
      sessionID: "ses_1",
      agent: "build",
      model: { providerID: "weave", modelID: "auto" },
      message: { id: messageID },
    } as never, output)
    const request = () =>
      (loaded.fetch as typeof fetch)("https://router.example.test/v1/responses", {
        headers: output.headers,
      })
    if (retryBeforeSuccess) {
      let firstAttemptFailed = false
      try {
        await request()
      } catch {
        firstAttemptFailed = true
      }
      if (!firstAttemptFailed) throw new Error("expected the first response attempt to fail")
    }
    await request()
  } finally {
    globalThis.fetch = realFetch
  }
}

describe("WeaveCodex routed-model toast", () => {
  test("toasts completed weave assistant message with model and cost/tokens", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    await captureRoutedModelHeader(hooks, "claude-opus-4-8")
    await hooks.event!({ event: messageUpdated(assistantInfo()) })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toEqual({
      body: {
        title: "Weave Router",
        message: "→ claude-opus-4-8 · $0.012 · 1.2k in / 340 out",
        variant: "info",
        duration: 6000,
      },
    })
  })

  test("correlates routed model metadata to each request", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    await captureRoutedModelHeader(hooks, "claude-opus-4-8", "msg_first")
    await captureRoutedModelHeader(hooks, "gpt-5.6-terra", "msg_second")
    await hooks.event!({ event: messageUpdated(assistantInfo({ id: "assistant_first", parentID: "msg_first" })) })
    await hooks.event!({ event: messageUpdated(assistantInfo({ id: "assistant_second", parentID: "msg_second" })) })
    expect(calls.map((call) => (call as { body: { message: string } }).body.message)).toEqual([
      "→ claude-opus-4-8 · $0.012 · 1.2k in / 340 out",
      "→ gpt-5.6-terra · $0.012 · 1.2k in / 340 out",
    ])
  })

  test("retains routed model correlation across a retry", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    await captureRoutedModelHeader(hooks, "claude-opus-4-8", "msg_retry", true)
    await hooks.event!({ event: messageUpdated(assistantInfo({ id: "assistant_retry", parentID: "msg_retry" })) })
    expect(calls[0]).toMatchObject({
      body: {
        message: "→ claude-opus-4-8 · $0.012 · 1.2k in / 340 out",
      },
    })
  })

  test("falls back to requested model when response omits routed model metadata", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    await captureRoutedModelHeader(hooks)
    await hooks.event!({ event: messageUpdated(assistantInfo()) })
    expect(calls[0]).toMatchObject({
      body: {
        message: "→ auto · $0.012 · 1.2k in / 340 out",
      },
    })
  })

  test("preserves positive sub-mill costs", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    await captureRoutedModelHeader(hooks, "claude-opus-4-8")
    await hooks.event!({ event: messageUpdated(assistantInfo({ id: "msg_small_cost", cost: 0.000499 })) })
    expect(calls[0]).toMatchObject({
      body: {
        message: "→ claude-opus-4-8 · <$0.001 · 1.2k in / 340 out",
      },
    })
  })

  test("does not toast user messages", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    await hooks.event!({
      event: messageUpdated({
        id: "msg_user",
        sessionID: "ses_1",
        role: "user",
        time: { created: 1 },
        agent: "build",
        model: { providerID: "weave", modelID: "auto" },
      }),
    })
    expect(calls).toHaveLength(0)
  })

  test("does not toast non-weave providerID", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    await hooks.event!({ event: messageUpdated(assistantInfo({ providerID: "openai" })) })
    expect(calls).toHaveLength(0)
  })

  test("toasts once per assistant message id", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(async (opts) => {
      calls.push(opts)
    })
    const event = messageUpdated(assistantInfo())
    await hooks.event!({ event })
    await hooks.event!({ event })
    expect(calls).toHaveLength(1)
  })

  test("showToast rejection does not reject the hook", async () => {
    const hooks = await pluginWithToast(async () => {
      throw new Error("tui unavailable")
    })
    await expect(hooks.event!({ event: messageUpdated(assistantInfo()) })).resolves.toBeUndefined()
  })
})
