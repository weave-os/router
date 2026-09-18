import { describe, expect, test } from "bun:test"
import { parseRouterDirective, rewriteDirectiveParts, rewriteRouterDirective } from "../src/directives.ts"

describe("OpenCode router directive rewrite", () => {
  test.each([
    ["$rf - too slow", " /router-feedback - too slow"],
    ["$router-feedback +", " /router-feedback +"],
    ["/rf - too slow", " /router-feedback - too slow"],
    ["$fm gpt-5.6-sol", " /force-model gpt-5.6-sol"],
    ["$force-model claude-opus-5", " /force-model claude-opus-5"],
    ["/fm opus", " /force-model opus"],
    ["$ufm", " /unforce-model"],
    ["$unforce-model", " /unforce-model"],
    ["/ufm", " /unforce-model"],
    ["$router-session", " /router-session"],
    ["/router-session", " /router-session"],
  ])("rewrites %s", (input, want) => {
    expect(rewriteRouterDirective(input)).toBe(want)
  })

  test("does not rewrite ordinary prompts", () => {
    expect(rewriteRouterDirective("please use $rf as an example")).toBeUndefined()
    expect(rewriteRouterDirective("/not-a-router-command")).toBeUndefined()
    expect(parseRouterDirective("$fm")).toBeUndefined()
  })

  test("rewrites the first text part only", () => {
    const parts = [
      { type: "text" as const, text: "$rf - too slow" },
      { type: "text" as const, text: "$fm opus" },
    ]
    expect(rewriteDirectiveParts(parts)).toBe(true)
    expect(parts[0].text).toBe(" /router-feedback - too slow")
    expect(parts[1].text).toBe("$fm opus")
  })
})
