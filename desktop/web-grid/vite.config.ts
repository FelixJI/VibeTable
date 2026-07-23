/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { fileURLToPath, URL } from "node:url";

const projectRoot = fileURLToPath(new URL(".", import.meta.url));

// Vite production build + vitest config for web-grid-v2.
// - build: bundles Vue + Naive UI + Tabulator CSS into dist/ (local-only).
// - test:   jsdom so Vue components + Tabulator can render during unit tests.
// - base: './' so the virtual host https://app.vibetable.local/ serves
//          relative asset paths (matches WebView2 SetVirtualHostNameToFolderMapping).
export default defineConfig({
  // Resolve from the config file instead of process.cwd(). Desktop builds can be
  // launched by MSBuild or a sandboxed host whose logical and real cwd differ.
  root: projectRoot,
  plugins: [vue()],
  base: "./",
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    target: "es2022",
    outDir: "dist",
    sourcemap: true,
    // Split vendor chunks to keep main bundle smaller for first paint.
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("node_modules")) {
            if (id.includes("/pinia/") || id.includes("/vue/")) return "vue";
            if (id.includes("/naive-ui/")) return "naive";
            if (id.includes("/tabulator-tables/")) return "tabulator";
            if (id.includes("/lucide-vue-next/")) return "icons";
            if (id.includes("/echarts/") || id.includes("/zrender/")) return "charts";
            if (id.includes("/gridstack/")) return "dashboard-layout";
          }
          return undefined;
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: false,
    include: ["src/**/*.test.ts"],
    setupFiles: ["src/test/setup.ts"],
    coverage: {
      provider: "v8",
      reportsDirectory: "coverage",
      reporter: ["text", "json-summary", "lcov"],
      include: ["src/**/*.{ts,vue}"],
      exclude: [
        "src/**/*.test.ts",
        "src/**/*.d.ts",
        "src/test/**",
        "src/main.ts",
      ],
      thresholds: {
        // Application logic is already above the repository-wide 85% line
        // target. Vue SFCs are tracked separately so the large, currently
        // presentation-heavy view layer cannot make the logic gate meaningless.
        "src/**/*.ts": {
          lines: 85,
        },
        "src/**/*.vue": {
          lines: 55,
        },
      },
    },
  },
});
