import { defineConfig } from "vitest/config";
import { resolve }      from "path";

export default defineConfig({
  resolve: {
    alias: { "@": resolve(__dirname, "src") },
  },
  test: {
    // edge-runtime gives us real Web Crypto, Headers, Response, Request globals
    // matching the Cloudflare Workers runtime exactly
    environment: "edge-runtime",
    globals:      true,
    coverage: {
      provider:   "v8",
      reporter:   ["text", "lcov", "html"],
      include:    ["src/**/*.ts"],
      exclude:    ["src/**/*.d.ts", "src/index.ts"],
      thresholds: {
        branches:   85,
        functions:  85,
        lines:      85,
        statements: 85,
      },
    },
  },
});
