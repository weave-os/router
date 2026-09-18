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
    modelID: "anthropic/claude-sonnet-4-5",
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

async function pluginWithToast(showToast: (opts: unknown) => Promise<unknown>, assistantText = "") {
  const input = {
    client: {
      tui: { showToast },
      session: {
        async message() {
          return {
            data: {
              parts: assistantText ? [{ type: "text", text: assistantText }] : [],
            },
          }
        },
      },
    },
  } as unknown as PluginInput
  return WeaveCodex(input)
}

describe("WeaveCodex routed-model toast", () => {
  test("toasts completed weave assistant message with model and cost/tokens", async () => {
    const calls: unknown[] = []
    const hooks = await pluginWithToast(
      async (opts) => {
        calls.push(opts)
      },
      "✦ **Weave Router** → claude-opus-4-8 · best pick for this turn\n\nanswer",
    )
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
