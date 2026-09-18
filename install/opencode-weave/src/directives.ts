/**
 * Rewrite OpenCode user text so Weave Router prompt directives reach the
 * same parser Codex and pi already hit. OpenCode is not a skill runner: `$rf`
 * would otherwise go to the LLM. The router already accepts both `/name` and
 * `$name` on the wire; rewriting `$…` to the leading-space `/…` form matches
 * the Codex emit.sh contract and keeps aliases out of model context.
 */

const FORCE_MODEL = /^(?:\$|\/)(?:force-model|fm)(?:\s+|$)/i
const UNFORCE_MODEL = /^(?:\$|\/)(?:unforce-model|ufm)\s*$/i
const ROUTER_FEEDBACK = /^(?:\$|\/)(?:router-feedback|rf)(?:\s|$)/i
const ROUTER_SESSION = /^(?:\$|\/)router-session\s*$/i

export type RouterDirective =
  | { kind: "force-model"; model: string }
  | { kind: "unforce-model" }
  | { kind: "router-feedback"; payload: string }
  | { kind: "router-session" }

export function parseRouterDirective(text: string): RouterDirective | undefined {
  const line = text.trim()
  if (!line) return undefined

  if (UNFORCE_MODEL.test(line) || line === "$ufm" || line === "/ufm") {
    return { kind: "unforce-model" }
  }
  if (ROUTER_SESSION.test(line)) {
    return { kind: "router-session" }
  }

  const force = line.match(/^(?:\$|\/)(?:force-model|fm)\s+(\S[\s\S]*)$/i)
  if (force) {
    const model = force[1].trim()
    if (model) return { kind: "force-model", model }
    return undefined
  }
  if (FORCE_MODEL.test(line)) return undefined

  const feedback = line.match(/^(?:\$|\/)(?:router-feedback|rf)(?:\s+([\s\S]*))?$/i)
  if (feedback && ROUTER_FEEDBACK.test(line)) {
    return { kind: "router-feedback", payload: (feedback[1] ?? "").trim() }
  }
  return undefined
}

export function rewriteRouterDirective(text: string): string | undefined {
  const directive = parseRouterDirective(text)
  if (!directive) return undefined
  switch (directive.kind) {
    case "force-model":
      return ` /force-model ${directive.model}`
    case "unforce-model":
      return " /unforce-model"
    case "router-feedback":
      return directive.payload ? ` /router-feedback ${directive.payload}` : " /router-feedback"
    case "router-session":
      return " /router-session"
  }
}

export function rewriteDirectiveParts<T extends { type: string; text?: string }>(parts: T[]): boolean {
  let rewritten = false
  for (const part of parts) {
    if (part.type !== "text" || typeof part.text !== "string") continue
    const next = rewriteRouterDirective(part.text)
    if (next === undefined) continue
    part.text = next
    rewritten = true
    break
  }
  return rewritten
}
