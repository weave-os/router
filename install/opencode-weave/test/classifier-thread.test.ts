import assert from "node:assert/strict"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import test from "node:test"
import type { PluginInput } from "@opencode-ai/plugin"

const ROUTER = "https://router.example.test"
const realFetch = globalThis.fetch

test("only explicit new parent and child sessions enroll; resumes retain separate tickets", async t => {
  const directory = await mkdtemp(join(tmpdir(), "weave-classifier-test-"))
  const previousOptIn = process.env.WEAVE_OPENCODE_LLM_CLASSIFIER
  const previousAuthFile = process.env.WEAVE_OPENCODE_AUTH_FILE
  process.env.WEAVE_OPENCODE_LLM_CLASSIFIER = "1"
  process.env.WEAVE_OPENCODE_AUTH_FILE = join(directory, "auth.json")
  t.after(async () => {
    globalThis.fetch = realFetch
    if (previousOptIn === undefined) delete process.env.WEAVE_OPENCODE_LLM_CLASSIFIER
    else process.env.WEAVE_OPENCODE_LLM_CLASSIFIER = previousOptIn
    if (previousAuthFile === undefined) delete process.env.WEAVE_OPENCODE_AUTH_FILE
    else process.env.WEAVE_OPENCODE_AUTH_FILE = previousAuthFile
    await rm(directory, { recursive: true, force: true })
  })

  const handshakes: Array<{ new_chat_id: string }> = []
  const inference: Array<{ session: string | null; ticket: string | null }> = []
  let handshakeStatus = 200
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url === `${ROUTER}/v1/router/threads`) {
      const request = JSON.parse(String(init?.body)) as { new_chat_id: string }
      handshakes.push(request)
      assert.equal(new Headers(init?.headers).get("X-Weave-Router-Key"), "rk_test")
      if (handshakeStatus !== 200) return new Response("unavailable", { status: handshakeStatus })
      return Response.json({ thread_token: `ticket-${request.new_chat_id}` })
    }
    assert.equal(url, `${ROUTER}/v1/responses`)
    const headers = new Headers(init?.headers)
    if (headers.get("X-Weave-Classifier-Thread") === "weave-classifier-unavailable") {
      return new Response("classifier_client_unavailable", { status: 400 })
    }
    inference.push({ session: headers.get("session-id"), ticket: headers.get("X-Weave-Classifier-Thread") })
    return Response.json({})
  }) as typeof fetch

  const input = { client: { tui: { showToast: async () => {} } } } as unknown as PluginInput
  const { WeaveCodex } = await import("../src/index.ts?classifier-test" as string) as typeof import("../src/index.ts")
  const plugin = await WeaveCodex(input)
  const loaded = await plugin.auth!.loader!(async () => ({ type: "api", key: "" }) as never, {} as never)
  const send = async (sessionID: string, agent = "build", hooks = plugin, provider = loaded) => {
    const output = { headers: {} as Record<string, string> }
    await hooks["chat.headers"]?.({ sessionID, agent, model: { providerID: "weave" },
      provider: { options: { baseURL: `${ROUTER}/v1`, headers: { "X-Weave-Router-Key": "rk_test" } } } } as never, output)
    return (provider.fetch as typeof fetch)(`${ROUTER}/v1/responses`, {
      method: "POST", headers: { ...output.headers, "X-Weave-Router-Key": "rk_test" }, body: "{}",
    })
  }

  process.env.WEAVE_OPENCODE_LLM_CLASSIFIER = "0"
  await send("ses_old")
  assert.deepEqual(inference, [{ session: "ses_old", ticket: null }])
  process.env.WEAVE_OPENCODE_LLM_CLASSIFIER = "1"
  assert.equal((await send("ses_old")).status, 400, "opted-in process cannot silently route an unknown session")

  await plugin.event?.({ event: { type: "session.created", properties: { info: { id: "ses_parent" } } } as never })
  await plugin.event?.({ event: { type: "session.created", properties: { info: { id: "ses_child", parentID: "ses_parent" } } } as never })
  handshakeStatus = 503
  assert.equal((await send("ses_parent")).status, 400)
  assert.equal(inference.length, 1, "failed enrollment cannot dispatch unticketed")
  handshakeStatus = 200
  await send("ses_parent")
  await send("ses_child", "explore")
  assert.equal(handshakes[0].new_chat_id, handshakes[1].new_chat_id)
  assert.notEqual(handshakes[1].new_chat_id, handshakes[2].new_chat_id)
  assert.notEqual(inference[1].ticket, inference[2].ticket)

  const restarted = await WeaveCodex(input)
  const restartedProvider = await restarted.auth!.loader!(async () => ({ type: "api", key: "" }) as never, {} as never)
  await send("ses_parent", "build", restarted, restartedProvider)
  assert.equal(inference[3].ticket, inference[1].ticket)
  assert.equal(handshakes.length, 3)
  await send("ses_parent", "title", restarted, restartedProvider)
  assert.equal(inference[4].ticket, null, "title requests do not become classifier turns")
  assert.equal((await send("ses_parent", "compaction", restarted, restartedProvider)).status, 400)
  assert.equal(inference.length, 5)
  await restarted.event?.({ event: { type: "session.compacted", properties: { sessionID: "ses_parent" } } as never })
  assert.equal((await send("ses_parent", "build", restarted, restartedProvider)).status, 400)
  assert.equal(inference.length, 5)

  const persisted = await readFile(join(directory, "weave-classifier-threads", "ses_parent.json"), "utf8")
  assert.equal(JSON.parse(persisted).compacted, true)
})
