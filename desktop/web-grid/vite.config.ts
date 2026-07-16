/// <reference types="vitest/config" />
import { defineConfig } from "vite";

// Vite production build config + vitest test environment.
// - build: bundles src/main.ts + Tabulator CSS into dist/ (local-only, no CDN).
// - test:   jsdom so Tabulator can render against a DOM during unit tests.
export default defineConfig({
  build: {
    target: "es2022",
    outDir: "dist",
    sourcemap: true,
  },
  test: {
    environment: "jsdom",
    globals: false,
    include: ["src/**/*.test.ts"],
  },
});
