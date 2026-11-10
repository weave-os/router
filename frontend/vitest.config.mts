import { fileURLToPath } from "node:url";

import react from "@vitejs/plugin-react";

import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [{ find: "@", replacement: fileURLToPath(new URL("./src", import.meta.url)) }],
  },
  test: {
    clearMocks: true,
    environment: "jsdom",
    globals: true,
    // @weave-os/ui ships bundler-style ESM (extensionless next/* and JSON imports).
    server: { deps: { inline: ["@weave-os/ui"] } },
    setupFiles: ["./vitest.setup.ts"],
  },
});
