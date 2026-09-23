import { mkdir, readFile, rename, unlink, writeFile } from "node:fs/promises"
import { homedir } from "node:os"
import { dirname, join } from "node:path"
import type { Session } from "@opencode-ai/sdk"

const SESSION_ID = /^[a-zA-Z0-9_-]{1,128}$/
export const CLASSIFIER_THREAD_HEADER = "X-Weave-Classifier-Thread"
export const CLASSIFIER_UNAVAILABLE_TICKET = "weave-classifier-unavailable"

interface SavedThread {
  sessionID: string
  newChatID: string
  routerOrigin?: string
  threadToken?: string
  compacted?: boolean
}

function threadDirectory(): string {
  const authFile = process.env.WEAVE_OPENCODE_AUTH_FILE ??
    join(process.env.XDG_DATA_HOME ?? join(homedir(), ".local", "share"), "opencode", "auth.json")
  return join(dirname(authFile), "weave-classifier-threads")
}

function threadPath(sessionID: string): string {
  if (!SESSION_ID.test(sessionID)) throw new Error("Invalid OpenCode session identity")
  return join(threadDirectory(), `${sessionID}.json`)
}

async function readThread(sessionID: string): Promise<SavedThread | undefined> {
  let saved: string
  try {
    saved = await readFile(threadPath(sessionID), "utf8")
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return undefined
    throw error
  }
  const parsed: unknown = JSON.parse(saved)
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Invalid classifier enrollment state")
  const thread = parsed as Partial<SavedThread>
  if (thread.sessionID !== sessionID || typeof thread.newChatID !== "string" ||
    !/^[0-9a-f-]{36}$/.test(thread.newChatID) ||
    (thread.routerOrigin !== undefined && typeof thread.routerOrigin !== "string") ||
    (thread.threadToken !== undefined && (typeof thread.threadToken !== "string" || !thread.threadToken || thread.threadToken.length > 4096)) ||
    (thread.compacted !== undefined && typeof thread.compacted !== "boolean")) {
    throw new Error("Invalid classifier enrollment state")
  }
  return thread as SavedThread
}

async function replaceThread(thread: SavedThread): Promise<void> {
  const destination = threadPath(thread.sessionID)
  const temporary = `${destination}.${crypto.randomUUID()}.tmp`
  await writeFile(temporary, JSON.stringify(thread), { flag: "wx", mode: 0o600 })
  try {
    await rename(temporary, destination)
  } catch (error) {
    await unlink(temporary)
    throw error
  }
}

/** The session-created event is the only enrollment authority; a short request is not. */
export class ClassifierThreads {
  private creations = new Map<string, Promise<void>>()
  private handshakes = new Map<string, Promise<string | undefined>>()

  async hasThread(sessionID: string): Promise<boolean> {
    await this.creations.get(sessionID)
    return (await readThread(sessionID)) !== undefined
  }

  created(session: Session): Promise<void> {
    if (process.env.WEAVE_OPENCODE_LLM_CLASSIFIER !== "1") return Promise.resolve()
    const sessionID = session.id
    if (this.creations.has(sessionID)) return this.creations.get(sessionID)!
    const creation = (async () => {
      const path = threadPath(sessionID)
      await mkdir(threadDirectory(), { recursive: true, mode: 0o700 })
      try {
        await writeFile(path, JSON.stringify({ sessionID, newChatID: crypto.randomUUID() }), { flag: "wx", mode: 0o600 })
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error
        await readThread(sessionID)
      }
    })()
    this.creations.set(sessionID, creation)
    return creation
  }

  compacted(sessionID: string): Promise<void> {
    return this.updateCompacted(sessionID)
  }

  private async updateCompacted(sessionID: string): Promise<void> {
    await this.creations.get(sessionID)
    await this.handshakes.get(sessionID)?.catch(() => undefined)
    const thread = await readThread(sessionID)
    if (thread) await replaceThread({ ...thread, compacted: true })
  }

  async header(sessionID: string, routerOrigin: string, routerKey: string, agent?: string): Promise<Record<string, string>> {
    await this.creations.get(sessionID)
    if (agent === "title" || agent === "compaction") {
      const ticket = await this.resolveTicket(sessionID, routerOrigin, routerKey, agent)
      return ticket ? { [CLASSIFIER_THREAD_HEADER]: ticket } : {}
    }
    if (this.handshakes.has(sessionID)) {
      const ticket = await this.handshakes.get(sessionID)!
      return ticket ? { [CLASSIFIER_THREAD_HEADER]: ticket } : {}
    }
    const handshake = this.resolveTicket(sessionID, routerOrigin, routerKey, agent)
    this.handshakes.set(sessionID, handshake)
    try {
      const ticket = await handshake
      return ticket ? { [CLASSIFIER_THREAD_HEADER]: ticket } : {}
    } finally {
      if (this.handshakes.get(sessionID) === handshake) this.handshakes.delete(sessionID)
    }
  }

  private async resolveTicket(sessionID: string, routerOrigin: string, routerKey: string, agent?: string): Promise<string | undefined> {
    const thread = await readThread(sessionID)
    if (!thread) return undefined
    if (thread.compacted || (thread.routerOrigin && thread.routerOrigin !== routerOrigin)) {
      throw new Error("Classifier session cannot continue after compaction or router change")
    }
    if (agent === "title") return undefined
    if (agent === "compaction") throw new Error("Classifier sessions require complete history; compaction is unavailable")
    if (thread.threadToken) return thread.threadToken
    if (!routerKey) throw new Error("Classifier enrollment requires a router key")
    const response = await fetch(`${routerOrigin}/v1/router/threads`, {
      method: "POST", redirect: "error", signal: AbortSignal.timeout(10_000),
      headers: { "X-Weave-Router-Key": routerKey, "X-App": "opencode", "Content-Type": "application/json" },
      body: JSON.stringify({ new_chat_id: thread.newChatID }),
    })
    if (!response.ok) throw new Error(`Classifier enrollment returned HTTP ${response.status}`)
    const ticket: unknown = await response.json()
    if (ticket === null || typeof ticket !== "object" || !("thread_token" in ticket) ||
      typeof ticket.thread_token !== "string" || !ticket.thread_token || ticket.thread_token.length > 4096) {
      throw new Error("Invalid classifier enrollment response")
    }
    await replaceThread({ ...thread, routerOrigin, threadToken: ticket.thread_token })
    return ticket.thread_token
  }
}
