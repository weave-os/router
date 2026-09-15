import { describe, expect, test } from "bun:test"
import { createOpenAI } from "@ai-sdk/openai"
import { execFileSync } from "node:child_process"
import { fileURLToPath } from "node:url"

type Scenario = "text" | "tools" | "missing_usage" | "malformed" | "truncated" | "unsupported"

function fixture(scenario: Scenario, streaming: boolean): string {
  return execFileSync("python3", ["-c", `
import json, sys
from responses_fixture import Scenario, responses_fixture
response, events = responses_fixture(Scenario(sys.argv[1]), "build", False)
print("".join(f"event: {name}\\ndata: {json.dumps(payload)}\\n\\n" for name, payload in events)
      if sys.argv[2] == "true" else json.dumps(response))
`, scenario, String(streaming)], {
    cwd: fileURLToPath(new URL("../../pi-router/test", import.meta.url)),
    encoding: "utf8",
    timeout: 5_000,
  })
}

function modelFor(scenario: Scenario, streaming: boolean, status = 200) {
  return createOpenAI({
    baseURL: "https://router.example.test/v1",
    apiKey: "fixture-key",
    fetch: (async (url, init) => {
      expect(String(url)).toBe("https://router.example.test/v1/responses")
      const body = JSON.parse(String(init?.body))
      expect(body.model).toBe("auto")
      expect(Boolean(body.stream)).toBe(streaming)
      return new Response(status === 200 ? fixture(scenario, streaming) : JSON.stringify({
        error: { type: "invalid_request_error", message: "MOCK_REJECTED" },
      }), { status, headers: { "Content-Type": streaming && status === 200 ? "text/event-stream" : "application/json" } })
    }) as typeof fetch,
  }).responses("auto")
}

const prompt = [{ role: "user" as const, content: [{ type: "text" as const, text: "Return the test marker" }] }]

async function streamParts(scenario: Scenario) {
  const response = await modelFor(scenario, true).doStream({ prompt })
  return Array.fromAsync(response.stream)
}

describe("OpenCode 1.18.27 Responses SDK contract (@ai-sdk/openai 3.0.84)", () => {
  test("non-streaming text and usage", async () => {
    const response = await modelFor("text", false).doGenerate({ prompt })
    expect(response.content).toEqual([{
      type: "text", text: "OPENCODE_OK", providerMetadata: { openai: { itemId: "msg_mock" } },
    }])
    expect(response.finishReason.unified).toBe("stop")
    expect(response.usage).toMatchObject({
      inputTokens: { total: 12, noCache: 9, cacheRead: 3, cacheWrite: undefined },
      outputTokens: { total: 8, text: 6, reasoning: 2 },
    })
  })

  test("streaming text ordering and completed usage", async () => {
    const parts = await streamParts("text")
    expect(parts.filter((part) => part.type === "error")).toEqual([])
    expect(parts.filter((part) => part.type === "text-delta").map((part) => part.delta).join("")).toBe("OPENCODE_OK")
    const types = parts.map((part) => part.type)
    expect(types.indexOf("text-start")).toBeLessThan(types.indexOf("text-delta"))
    expect(types.indexOf("text-delta")).toBeLessThan(types.indexOf("text-end"))
    expect(types.at(-1)).toBe("finish")
    const finish = parts.find((part) => part.type === "finish")!
    expect(finish.finishReason.unified).toBe("stop")
    expect(finish.usage.inputTokens.total).toBe(12)
    expect(finish.usage.inputTokens.cacheRead).toBe(3)
    expect(finish.usage.outputTokens.total).toBe(8)
    expect(finish.usage.outputTokens.reasoning).toBe(2)
  })

  test.each([false, true])("missing usage is unknown, not fabricated (stream=%s)", async (streaming) => {
    if (!streaming) {
      const response = await modelFor("missing_usage", false).doGenerate({ prompt })
      expect(response.usage.inputTokens.total).toBeUndefined()
      expect(response.usage.outputTokens.total).toBeUndefined()
      return
    }
    const parts = await streamParts("missing_usage")
    const finish = parts.find((part) => part.type === "finish")!
    // The pinned streaming schema requires usage on response.completed;
    // without it OpenCode sees an unknown finish and may continue the turn.
    expect(finish.finishReason.unified).toBe("other")
    expect(finish.usage.inputTokens.total).toBeUndefined()
    expect(finish.usage.outputTokens.total).toBeUndefined()
  })

  test.each([false, true])("upstream error rejects (stream=%s)", async (streaming) => {
    const model = modelFor("text", streaming, 400)
    await expect(streaming ? model.doStream({ prompt }) : model.doGenerate({ prompt })).rejects.toThrow("MOCK_REJECTED")
  })

  test("multiple calls retain IDs and assembled arguments", async () => {
    const parts = await streamParts("tools")
    const calls = parts.filter((part) => part.type === "tool-call")
    expect(calls.map((part) => part.toolCallId)).toEqual(["call_mock_0", "call_mock_1"])
    for (const [index, call] of calls.entries()) {
      expect(call.toolName).toBe("bash")
      expect(JSON.parse(call.input)).toEqual({ command: `printf TOOL_${index}_OK`, description: "Print test marker" })
    }
    expect(parts.find((part) => part.type === "finish")!.finishReason.unified).toBe("tool-calls")
  })

  test("non-streaming malformed output is rejected", async () => {
    await expect(modelFor("malformed", false).doGenerate({ prompt })).rejects.toThrow("Invalid JSON response")
  })

  test("malformed known event cannot produce text or successful completion", async () => {
    const parts = await streamParts("malformed")
    expect(parts.some((part) => part.type === "text-delta")).toBe(false)
    expect(parts.find((part) => part.type === "finish")?.finishReason.unified).toBe("other")
  })

  test("truncated stream cannot claim successful completion or usage", async () => {
    const parts = await streamParts("truncated")
    const finish = parts.find((part) => part.type === "finish")!
    expect(finish.finishReason.unified).toBe("other")
    expect(finish.usage.outputTokens.total).toBeUndefined()
    expect(parts.some((part) => part.type === "text-delta")).toBe(false)
  })

  test("unknown event is ignored without losing subsequent completion", async () => {
    const parts = await streamParts("unsupported")
    expect(parts.filter((part) => part.type === "text-delta").map((part) => part.delta).join("")).toBe("OPENCODE_OK")
    expect(parts.find((part) => part.type === "finish")!.finishReason.unified).toBe("stop")
  })
})
