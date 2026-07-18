/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { fileURLToPath, URL } from "node:url";

// Vite production build + vitest config for web-grid-v2.
// - build: bundles Vue + Naive UI + Tabulator CSS into dist/ (local-only).
// - test:   jsdom so Vue components + Tabulator can render during unit tests.
// - base: './' so the virtual host https://app.vibetable.local/ serves
//          relative asset paths (matches WebView2 SetVirtualHostNameToFolderMapping).
export default defineConfig({
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
  },
});
