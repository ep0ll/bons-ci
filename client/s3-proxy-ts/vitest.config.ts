import { defineConfig } from "vitest/config";
import { resolve }      from "path";

export default defineConfig({
  resolve: {
    alias: { "@": resolve(__dirname, "src") },
  },
  test: {
    /**
     * "edge-runtime" provides the Web Crypto API (`globalThis.crypto`),
     * the `Headers`, `Request`, `Response` globals, and `KVNamespace`-compatible
     * APIs that Cloudflare Workers expose.
     *
     * This means crypto tests run against the real Web Crypto implementation
     * (not a polyfill), matching the production Cloudflare Workers behaviour.
     */
    environment:  "edge-runtime",
    globals:       true,
    coverage: {
      provider:   "v8",
      reporter:   ["text", "lcov", "html"],
      include:    ["src/**/*.ts"],
      exclude:    ["src/**/*.d.ts"],
      thresholds: {
        branches:   80,
        functions:  80,
        lines:      80,
        statements: 80,
      },
    },
  },
});
