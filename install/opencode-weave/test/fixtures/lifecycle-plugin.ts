import type { Plugin } from "@opencode-ai/plugin"

// Observes OpenCode's own agent identity; not a production routing fingerprint.
export const LifecycleObserver: Plugin = async () => ({
  "chat.headers": async (input, output) => {
    output.headers["x-conformance-agent"] = input.agent
    output.headers["x-conformance-scenario"] = process.env.OPENCODE_CONFORMANCE_SCENARIO ?? "text"
  },
})
