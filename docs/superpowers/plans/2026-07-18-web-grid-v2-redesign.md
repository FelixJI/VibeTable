# web-grid v2 前端重做实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `desktop/web-grid/`（vanilla TS + Tabulator，单体 main.ts）绿地重写为 `desktop/web-grid-v2/`（Vue 3 + Pinia + Naive UI + Lucide + 严格分层 + 飞书风视觉），新增暗色模式（跟随系统）、复制/粘贴/撤销快捷键 + 说明页、键盘导航，并修正现有架构债。

**Architecture:** 严格单向分层：contracts（复用）→ bridge（复用）→ services（唯一 bridge 调用方）→ stores（Pinia 唯一状态源）→ components（纯展示）→ views。host bridge 协议、契约、.NET 宿主、Python 后端、Directus 零改动。

**Tech Stack:** Vue 3.5 + Vite 8 + TypeScript 6 + Pinia 2.3 + Naive UI 2 + lucide-vue-next 0.300 + tabulator-tables 6.5.2（保留）+ Vitest 4 + jsdom 29。Node 24.18.0。

**Reference spec:** `docs/superpowers/specs/2026-07-18-web-grid-v2-redesign.md`

## Global Constraints

- **Node 版本**：24.18.0（`.nvmrc`），`engines.node: ">=24 <25"`。所有 npm 命令在此版本下运行。
- **CSP 不变**：`default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'`。所有依赖经 Vite 打包，禁 CDN、禁 inline script、禁运行时网络请求。
- **契约不变**：`contracts.ts`（862 行）一字不改，与 .NET `VibeTable.Contracts` + Python pydantic 三方镜像。
- **Bridge 不变**：`hostBridge.ts`（479 行）原样拷贝。16 个出站白名单 + 13 个入站白名单 + `{type, requestId?, payload?}` 信封 + 4 MiB 上限不变。
- **严格分层规则**：components 不得 import bridge/services；stores 不得 import bridge（只能经 service）；services 是 bridge 的唯一调用方。
- **TypeScript 严格**：`strict: true`，`verbatimModuleSyntax: true`（类型导入必须用 `import type`），`noUnusedLocals/Parameters: true`。
- **Git 策略**：在 `feature/web-grid-v2` 分支上工作，阶段 4 验收前不合入 main。每步结束提交。
- **旧目录保留**：`desktop/web-grid/` 在阶段 4 验收通过后才删除。
- **复用模块**：拷贝时原样复制，连同单测。复用模块的现有单测必须 100% 通过。

**关键现有 API（复用，勿改）**：
- `createHostBridge(options?: HostBridgeOptions): HostBridge` — 返回 `{start, stop, request, notify, on}`
- `HostBridge.on<K>(type, handler): () => void` — 返回 unsubscribe 函数
- `HostBridge.notify<K>(type, payload): void`
- `HostBridge.request<K>(type, payload): Promise<unknown>`
- 契约类型见 `desktop/web-grid/src/contracts.ts`（`TablePage`/`ColumnSchema`/`PastePlan`/`ApplyPasteResult`/`UpdateCellResult`/`InsertRowResult`/`DeleteRowsResult` 等）

---

## File Structure

```
desktop/web-grid-v2/
├── index.html                              # Task 1
├── package.json                            # Task 1
├── vite.config.ts                          # Task 1
├── tsconfig.json                           # Task 1
├── .gitignore                              # Task 1
└── src/
    ├── main.ts                             # Task 1 (skeleton), Task 19 (final)
    ├── App.vue                             # Task 19
    ├── contracts/index.ts                  # Task 2 (copy)
    ├── bridge/hostBridge.ts                # Task 2 (copy)
    ├── grid/                               # Task 2 (copy all)
    │   ├── createGrid.ts
    │   ├── clipboardParser.ts
    │   ├── pasteContext.ts
    │   ├── editorFactory.ts
    │   ├── pendingEdits.ts
    │   ├── queryAdapter.ts
    │   └── gridState.ts
    ├── services/tableAdminValidation.ts    # Task 2 (copy)
    ├── design-tokens/
    │   ├── tokens.css                      # Task 3
    │   └── theme.ts                        # Task 3
    ├── i18n/
    │   ├── index.ts                        # Task 4
    │   └── locales/{zh-CN,en-US}.ts        # Task 4
    ├── stores/
    │   ├── workspaceStore.ts               # Task 6
    │   ├── tableStore.ts                   # Task 7
    │   ├── pasteStore.ts                   # Task 8
    │   ├── tableAdminStore.ts              # Task 9
    │   ├── uiStore.ts                      # Task 10
    │   ├── historyStore.ts                 # Task 11
    │   └── keyboardStore.ts                # Task 12
    ├── services/
    │   ├── bridgeContext.ts                # Task 5 (provides singleton HostBridge to services)
    │   ├── workspaceService.ts             # Task 6
    │   ├── tableService.ts                 # Task 7
    │   ├── pasteService.ts                 # Task 8
    │   ├── tableAdminService.ts            # Task 9
    │   └── errorRouter.ts                  # Task 13
    ├── composables/
    │   ├── useTheme.ts                     # Task 14
    │   ├── useTabulator.ts                 # Task 16
    │   └── useKeyboard.ts                  # Task 15
    ├── keyboard/
    │   ├── shortcuts.ts                    # Task 12
    │   └── gridNavigation.ts               # Task 15
    ├── views/
    │   ├── WorkspaceView.vue               # Task 18
    │   └── ShortcutsView.vue               # Task 17
    └── components/
        ├── layout/{AppSidebar,AppToolbar}.vue          # Task 18
        ├── grid/GridHost.vue                            # Task 16
        ├── panels/{PastePanel,CreateTableModal,DeleteConfirmModal}.vue  # Task 18
        └── feedback/{StatusBar,LoadingOverlay,ErrorOverlay}.vue         # Task 18
```

---

## 阶段 0: 脚手架

### Task 1: 初始化 web-grid-v2 项目骨架

**Files:**
- Create: `desktop/web-grid-v2/package.json`
- Create: `desktop/web-grid-v2/vite.config.ts`
- Create: `desktop/web-grid-v2/tsconfig.json`
- Create: `desktop/web-grid-v2/.gitignore`
- Create: `desktop/web-grid-v2/index.html`
- Create: `desktop/web-grid-v2/src/main.ts`
- Create: `desktop/web-grid-v2/src/App.vue`
- Modify: `.gitignore` (root) — add `desktop/web-grid-v2/node_modules/` and `/dist` exclusions if not covered

**Interfaces:**
- Produces: 一个能 `npm install && npm run build` 产出 `dist/index.html` 的空 Vue 应用，能在 WebView2 加载空白页。

- [ ] **Step 1: 创建 package.json**

`desktop/web-grid-v2/package.json`:
```json
{
  "name": "vibetable-web-grid-v2",
  "private": true,
  "version": "1.0.0",
  "type": "module",
  "engines": { "node": ">=24 <25" },
  "scripts": {
    "build": "vue-tsc --noEmit && vite build",
    "test": "vitest run",
    "test:watch": "vitest",
    "typecheck": "vue-tsc --noEmit"
  },
  "dependencies": {
    "lucide-vue-next": "0.300.0",
    "naive-ui": "2.38.2",
    "pinia": "2.3.1",
    "tabulator-tables": "6.5.2",
    "vue": "3.5.13"
  },
  "devDependencies": {
    "@vitejs/plugin-vue": "5.2.3",
    "@vue/test-utils": "2.4.6",
    "jsdom": "29.1.1",
    "typescript": "6.0.3",
    "vite": "8.1.3",
    "vitest": "4.1.9",
    "vue-tsc": "2.2.0"
  }
}
```

- [ ] **Step 2: 创建 tsconfig.json**

`desktop/web-grid-v2/tsconfig.json`（基于旧 web-grid/tsconfig.json，加 Vue 支持）:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "types": ["vite/client"],
    "jsx": "preserve",
    "jsxImportSource": "vue",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noImplicitOverride": true,
    "noFallthroughCasesInSwitch": true,
    "exactOptionalPropertyTypes": false,
    "forceConsistentCasingInFileNames": true,
    "isolatedModules": true,
    "verbatimModuleSyntax": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "resolveJsonModule": true,
    "allowSyntheticDefaultImports": true,
    "noEmit": true,
    "baseUrl": ".",
    "paths": { "@/*": ["src/*"] }
  },
  "include": ["src", "vite.config.ts", "env.d.ts"]
}
```

- [ ] **Step 3: 创建 vite.config.ts**

`desktop/web-grid-v2/vite.config.ts`:
```ts
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
        manualChunks: {
          vue: ["vue", "pinia"],
          naive: ["naive-ui"],
          tabulator: ["tabulator-tables"],
          icons: ["lucide-vue-next"],
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
```

- [ ] **Step 4: 创建 .gitignore**

`desktop/web-grid-v2/.gitignore`:
```
node_modules/
dist/
*.local
.vitest-cache/
```

- [ ] **Step 5: 创建 index.html（含原 CSP）**

`desktop/web-grid-v2/index.html`:
```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <meta
      http-equiv="Content-Security-Policy"
      content="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
    />
    <title>VibeTable</title>
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

- [ ] **Step 6: 创建 env.d.ts（Tabulator 类型 + Vue SFC 声明）**

`desktop/web-grid-v2/env.d.ts`:
```ts
/// <reference types="vite/client" />

declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent<Record<string, never>, Record<string, never>, unknown>;
  export default component;
}

// Copied verbatim from web-grid/src/env.d.ts (Tabulator 6.5.2 ships no types).
declare module "tabulator-tables" {
  export interface TabulatorRowComponent {
    getData(): Record<string, unknown>;
  }
  export interface TabulatorColumnComponent {
    getField(): string;
  }
  export interface TabulatorRangeComponent {
    getRows(): TabulatorRowComponent[];
    getColumns(): TabulatorColumnComponent[];
  }
  export type TabulatorOptions = Record<string, unknown> & {
    columns?: unknown[];
    data?: unknown[];
    selectableRange?: boolean | unknown;
    selectableRangeCellBDash?: unknown;
    clipboard?: boolean | "copy" | "paste" | "copy paste";
    clipboardPasteAction?: unknown;
    [key: string]: unknown;
  };
  export class Tabulator {
    constructor(element: string | HTMLElement, options: TabulatorOptions);
    getColumns(): unknown[];
    getRanges(): TabulatorRangeComponent[];
    setData(data: unknown[]): Promise<void>;
    setColumns(columns: unknown[]): void;
    destroy?(): void;
  }
  export class TabulatorFull extends Tabulator {}
}

declare module "*.css";
```

- [ ] **Step 7: 创建 src/App.vue 和 src/main.ts（最小骨架）**

`desktop/web-grid-v2/src/App.vue`:
```vue
<script setup lang="ts">
// Minimal skeleton — replaced in Task 19.
</script>

<template>
  <div class="app-root">VibeTable web-grid v2 (skeleton)</div>
</template>
```

`desktop/web-grid-v2/src/main.ts`:
```ts
import { createApp } from "vue";
import App from "./App.vue";

createApp(App).mount("#app");
```

- [ ] **Step 8: 安装依赖**

Run:
```bash
cd desktop/web-grid-v2
npm install
```
Expected: 安装成功，无依赖冲突。若 naive-ui 或 lucide-vue-next 的精确版本不存在，调整到最接近的可用版本（记录在提交信息里）。

- [ ] **Step 9: 验证 build 产出 dist/index.html**

Run:
```bash
cd desktop/web-grid-v2
npm run build
```
Expected: `vue-tsc --noEmit` 通过，`vite build` 产出 `dist/index.html` + `dist/assets/*.js`。

- [ ] **Step 10: 验证 CSP 合规（静态检查 dist/index.html 无 CDN 引用）**

Run:
```bash
cd desktop/web-grid-v2
grep -E "https?://|//cdn" dist/index.html dist/assets/*.js | grep -v "//# sourceMappingURL" || echo "OK: no external URLs"
```
Expected: 输出 `OK: no external URLs`。

- [ ] **Step 11: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): scaffold Vue 3 + Vite + Pinia + Naive UI project"
```

---

## 阶段 1: 基础设施层

### Task 2: 拷贝复用模块（contracts / bridge / grid / validation）及其单测

**Files:**
- Copy: `desktop/web-grid/src/contracts.ts` → `desktop/web-grid-v2/src/contracts/index.ts`
- Copy: `desktop/web-grid/src/hostBridge.ts` → `desktop/web-grid-v2/src/bridge/hostBridge.ts`
- Copy: `desktop/web-grid/src/grid/{createGrid,clipboardParser,pasteContext,editorFactory,pendingEdits}.ts` → `desktop/web-grid-v2/src/grid/`
- Copy: `desktop/web-grid/src/query/queryAdapter.ts` → `desktop/web-grid-v2/src/grid/queryAdapter.ts`
- Copy: `desktop/web-grid/src/state/gridState.ts` → `desktop/web-grid-v2/src/grid/gridState.ts`
- Copy: `desktop/web-grid/src/tableAdminValidation.ts` → `desktop/web-grid-v2/src/services/tableAdminValidation.ts`
- Copy: 所有对应的 `.test.ts` 文件到相同相对位置

**Interfaces:**
- Produces: 复用模块在 v2 下可被 import，且所有现有单测通过（证明逻辑等价）。

- [ ] **Step 1: 拷贝所有复用模块**

Run:
```bash
cd desktop/web-grid-v2/src
mkdir -p contracts bridge grid services

cp ../../web-grid/src/contracts.ts contracts/index.ts
cp ../../web-grid/src/hostBridge.ts bridge/hostBridge.ts
cp ../../web-grid/src/grid/createGrid.ts grid/createGrid.ts
cp ../../web-grid/src/grid/clipboardParser.ts grid/clipboardParser.ts
cp ../../web-grid/src/grid/pasteContext.ts grid/pasteContext.ts
cp ../../web-grid/src/grid/editorFactory.ts grid/editorFactory.ts
cp ../../web-grid/src/grid/pendingEdits.ts grid/pendingEdits.ts
cp ../../web-grid/src/query/queryAdapter.ts grid/queryAdapter.ts
cp ../../web-grid/src/state/gridState.ts grid/gridState.ts
cp ../../web-grid/src/tableAdminValidation.ts services/tableAdminValidation.ts
```

- [ ] **Step 2: 拷贝对应的单测文件**

Run:
```bash
cd desktop/web-grid-v2/src
cp ../../web-grid/src/grid/pasteContext.test.ts grid/ 2>/dev/null || true
cp ../../web-grid/src/hostBridge.test.ts bridge/
cp ../../web-grid/src/pasteFlow.test.ts ./
cp ../../web-grid/src/fieldHistoryFlow.test.ts ./
cp ../../web-grid/src/tableAdminValidation.test.ts services/ 2>/dev/null || true
cp ../../web-grid/src/tableAdminFlow.test.ts ./
cp ../../web-grid/src/tableFlow.test.ts ./
# (some tests import from sibling paths; we will fix imports next)
```
注：只拷贝与复用模块直接相关的测试。`pasteFlow.test.ts`/`tableFlow.test.ts`/`tableAdminFlow.test.ts` 测的是将要重写的 flow 模块——本次先拷过来作为"行为基线"，等重写后用新 store 测试替换。

- [ ] **Step 3: 修正拷贝文件里的相对 import 路径**

复用模块内部互相引用的路径在新目录结构下需要调整。逐文件检查：

Run:
```bash
cd desktop/web-grid-v2/src
grep -rn "from \"\./contracts\"\|from \"\./hostBridge\"\|from \"\./grid/\|from \"\./query/\|from \"\./state/" contracts bridge grid services 2>/dev/null
```
对每个匹配，按新结构调整：
- `grid/*.ts` 里 `from "./contracts"` → `from "../contracts"`
- `grid/*.ts` 里 `from "./query/..."` 或 `from "./state/..."` → `from "./..."` （已平铺到 grid/）
- `services/tableAdminValidation.ts` 里 `from "./contracts"` → `from "../contracts"`
- `bridge/hostBridge.ts` 里 `from "./contracts"` → `from "../contracts"`

手动编辑每个文件修正（用 Edit 工具，逐个确认）。

- [ ] **Step 4: 运行 typecheck 验证复用模块编译通过**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck
```
Expected: 零错误。若有错误，是 import 路径问题，继续修正直到通过。

- [ ] **Step 5: 运行复用模块的现有单测**

Run:
```bash
cd desktop/web-grid-v2
npm test
```
Expected: 与复用模块直接相关的测试（`hostBridge.test.ts`、`pasteContext.test.ts`、`tableAdminValidation.test.ts` 等）全部 PASS。

注：`pasteFlow.test.ts`/`tableFlow.test.ts`/`tableAdminFlow.test.ts` 此时可能因 import 旧 flow 模块而失败——把这些测试文件**暂时重命名为 `.test.ts.skip`**，等阶段 2 重写 store 后用新测试替换。在提交信息里记录这一情况。

- [ ] **Step 6: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): copy reusable modules (contracts/bridge/grid/validation) + tests"
```

---

### Task 3: 飞书风 design tokens（亮/暗双套）

**Files:**
- Create: `desktop/web-grid-v2/src/design-tokens/tokens.css`
- Create: `desktop/web-grid-v2/src/design-tokens/theme.ts`
- Modify: `desktop/web-grid-v2/src/main.ts`（import tokens.css）

**Interfaces:**
- Produces: CSS 变量 `--vt-color-primary-500` 等（亮色在 `:root`，暗色在 `:root.dark`）+ Naive UI `GlobalThemeOverrides` 导出（`lightThemeOverrides` / `darkThemeOverrides`）。

- [ ] **Step 1: 创建 tokens.css**

`desktop/web-grid-v2/src/design-tokens/tokens.css`（完整飞书风变量，亮 + 暗）:
```css
/* VibeTable design tokens — Feishu-style. Light default, dark under :root.dark. */
:root {
  /* Brand primary — Feishu blue-violet #3370FF + lightness scale */
  --vt-color-primary-50:  #e8f0ff;
  --vt-color-primary-100: #d4e3ff;
  --vt-color-primary-200: #b3ccff;
  --vt-color-primary-500: #3370ff;
  --vt-color-primary-600: #2b5fe0;
  --vt-color-primary-700: #1f47b3;

  /* Semantic colors (Feishu) */
  --vt-color-success: #00b88a;
  --vt-color-warning: #ffa600;
  --vt-color-danger:  #f54a45;
  --vt-color-info:    #3370ff;

  /* Neutral grayscale — 7 stops */
  --vt-gray-0:   #ffffff;
  --vt-gray-50:  #f7f8fa;
  --vt-gray-100: #f2f3f5;
  --vt-gray-200: #e5e6eb;
  --vt-gray-300: #c9cdd4;
  --vt-gray-400: #86909c;
  --vt-gray-500: #4e5969;
  --vt-gray-600: #272e3b;

  /* Surfaces */
  --vt-bg:        var(--vt-gray-0);
  --vt-bg-subtle: var(--vt-gray-50);
  --vt-bg-sunken: var(--vt-gray-100);
  --vt-fg:        var(--vt-gray-600);
  --vt-fg-muted:  var(--vt-gray-400);
  --vt-border:    var(--vt-gray-200);

  /* Shadows — restrained */
  --vt-shadow-1: 0 1px 2px rgba(0, 0, 0, 0.04);
  --vt-shadow-2: 0 2px 8px rgba(0, 0, 0, 0.08);
  --vt-shadow-3: 0 8px 24px rgba(0, 0, 0, 0.12);

  /* Radius */
  --vt-radius-sm: 4px;
  --vt-radius-md: 6px;
  --vt-radius-lg: 8px;

  /* Spacing — 4px base */
  --vt-space-1: 4px;
  --vt-space-2: 8px;
  --vt-space-3: 12px;
  --vt-space-4: 16px;
  --vt-space-5: 24px;

  /* Type scale (Feishu) */
  --vt-font-caption: 12px;
  --vt-font-body:    13px;
  --vt-font-label:   14px;
  --vt-font-title:   16px;
  --vt-font-heading: 18px;
  --vt-line-base:    1.5;

  /* Motion — restrained */
  --vt-ease: cubic-bezier(0.34, 0.6, 0.2, 1);
  --vt-duration-fast: 120ms;
  --vt-duration-base: 200ms;

  /* Font stack — system fonts only (CSP bans CDN web fonts) */
  --vt-font-family: "PingFang SC", "Microsoft YaHei", system-ui, -apple-system,
    "Segoe UI", Roboto, sans-serif;
}

:root.dark {
  --vt-color-primary-500: #5b8bff;  /* lifted for contrast on dark */
  --vt-color-primary-600: #4a7dff;
  --vt-color-primary-700: #3a6bff;

  --vt-gray-0:   #17191f;
  --vt-gray-50:  #1e2128;
  --vt-gray-100: #23262e;
  --vt-gray-200: #2a2e37;
  --vt-gray-300: #3a3f4a;
  --vt-gray-400: #6b7280;
  --vt-gray-500: #9ca3af;
  --vt-gray-600: #c9cdd4;

  --vt-shadow-1: 0 1px 2px rgba(0, 0, 0, 0.3);
  --vt-shadow-2: 0 2px 8px rgba(0, 0, 0, 0.4);
  --vt-shadow-3: 0 8px 24px rgba(0, 0, 0, 0.5);
}

html, body {
  margin: 0;
  padding: 0;
  height: 100%;
  background: var(--vt-bg);
  color: var(--vt-fg);
  font-family: var(--vt-font-family);
  font-size: var(--vt-font-body);
  line-height: var(--vt-line-base);
  -webkit-font-smoothing: antialiased;
}

#app {
  height: 100%;
}
```

- [ ] **Step 2: 创建 theme.ts（Naive UI themeOverrides）**

`desktop/web-grid-v2/src/design-tokens/theme.ts`:
```ts
import type { GlobalThemeOverrides } from "naive-ui";

// Naive UI overrides so the component library eats the same Feishu tokens.
// Light + dark variants. Primary color differs (dark lifts for contrast).

const fontFamily =
  '"PingFang SC", "Microsoft YaHei", system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

const base: GlobalThemeOverrides = {
  common: {
    borderRadius: "6px",
    borderRadiusSmall: "4px",
    fontFamily,
    fontWeightStrong: "500",
  },
  Button: { fontWeight: "500" },
  Card: { borderRadius: "8px" },
};

export const lightThemeOverrides: GlobalThemeOverrides = {
  ...base,
  common: {
    ...base.common,
    primaryColor: "#3370ff",
    primaryColorHover: "#2b5fe0",
    primaryColorPressed: "#1f47b3",
    primaryColorSuppl: "#3370ff",
    successColor: "#00b88a",
    successColorHover: "#00a67e",
    successColorPressed: "#008f6c",
    warningColor: "#ffa600",
    warningColorHover: "#e59500",
    warningColorPressed: "#cc8400",
    errorColor: "#f54a45",
    errorColorHover: "#db3f3a",
    errorColorPressed: "#c23530",
    infoColor: "#3370ff",
  },
};

export const darkThemeOverrides: GlobalThemeOverrides = {
  ...base,
  common: {
    ...base.common,
    primaryColor: "#5b8bff",
    primaryColorHover: "#4a7dff",
    primaryColorPressed: "#3a6bff",
    primaryColorSuppl: "#5b8bff",
    successColor: "#1ecda0",
    warningColor: "#ffb840",
    errorColor: "#f56a66",
    infoColor: "#5b8bff",
  },
};
```

- [ ] **Step 3: 在 main.ts import tokens.css**

`desktop/web-grid-v2/src/main.ts`（更新）:
```ts
import { createApp } from "vue";
import "./design-tokens/tokens.css";
import App from "./App.vue";

createApp(App).mount("#app");
```

- [ ] **Step 4: 验证 typecheck + build**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck && npm run build
```
Expected: 通过，`dist/` 产出。

- [ ] **Step 5: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add Feishu-style design tokens (light + dark)"
```

---

### Task 4: i18n 轻量层（zh-CN 默认 + en-US）

**Files:**
- Create: `desktop/web-grid-v2/src/i18n/index.ts`
- Create: `desktop/web-grid-v2/src/i18n/locales/zh-CN.ts`
- Create: `desktop/web-grid-v2/src/i18n/locales/en-US.ts`
- Create: `desktop/web-grid-v2/src/i18n/i18n.test.ts`

**Interfaces:**
- Produces: `t(key, params?)` / `setLocale(l)` / `getLocale()`，约 50 条 key。

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/i18n/i18n.test.ts`:
```ts
import { describe, it, expect, beforeEach } from "vitest";
import { t, setLocale, getLocale } from "./index";

describe("i18n", () => {
  beforeEach(() => {
    setLocale("zh-CN");
  });

  it("returns the zh-CN message for a known key", () => {
    expect(t("app.title")).toBe("VibeTable");
  });

  it("falls back to key when message is missing", () => {
    expect(t("nonexistent.key")).toBe("nonexistent.key");
  });

  it("interpolates params", () => {
    expect(t("toolbar.rowCount", { count: 42 })).toBe("42 行");
  });

  it("switches locale to en-US", () => {
    setLocale("en-US");
    expect(getLocale()).toBe("en-US");
    expect(t("toolbar.rowCount", { count: 42 })).toBe("42 rows");
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- i18n
```
Expected: FAIL（`t` 未定义）。

- [ ] **Step 3: 创建 zh-CN locale**

`desktop/web-grid-v2/src/i18n/locales/zh-CN.ts`:
```ts
export const messages: Record<string, string> = {
  "app.title": "VibeTable",
  "app.loading": "加载中…",
  "app.ready": "就绪",

  "sidebar.tables": "表",
  "sidebar.newTable": "新建表",
  "sidebar.admin": "管理后台",
  "sidebar.delete": "删除",
  "sidebar.delete.confirm": "确定要删除表「{name}」吗？此操作不可撤销。",

  "toolbar.connectDirectus": "连接 Directus",
  "toolbar.refresh": "刷新",
  "toolbar.rowCount": "{count} 行",
  "toolbar.help": "快捷键",
  "toolbar.theme": "主题",

  "status.databaseOpening": "正在连接数据库…",
  "status.databaseOpened": "已连接，共 {count} 个集合",
  "status.databaseFailed": "连接失败：{message}",
  "status.tableLoading": "正在加载表「{name}」…",
  "status.tableLoaded": "已加载 {count} 行",
  "status.tableLoadFailed": "加载失败：{message}",

  "paste.title.preview": "粘贴预览",
  "paste.title.result": "粘贴结果",
  "paste.title.error": "粘贴出错",
  "paste.summary": "将写入 {rows} 行 × {cols} 列",
  "paste.overflow": "剪贴板超过 10,000 单元格上限。请改用「文件导入」。",
  "paste.ack": "我已确认上述警告",
  "paste.confirm": "提交",
  "paste.cancel": "取消",
  "paste.empty": "剪贴板内容为空",
  "paste.noTable": "请先选择一个表再粘贴",
  "paste.noFields": "当前表没有可编辑字段",

  "createTable.title": "新建表",
  "createTable.name": "表名",
  "createTable.fieldName": "字段名",
  "createTable.fieldType": "类型",
  "createTable.addField": "添加字段",
  "createTable.removeField": "删除",
  "createTable.submit": "创建",
  "createTable.cancel": "取消",

  "delete.title": "确认删除",
  "delete.confirm": "删除",
  "delete.cancel": "取消",

  "error.title": "出错了",
  "error.unknown": "未知错误",

  "undo.succeeded": "已撤销：{label}",
  "undo.failed.conflict": "撤销失败：数据已变化",
  "undo.failed.partial": "部分操作无法完美恢复",
  "redo.succeeded": "已重做：{label}",

  "shortcuts.title": "快捷键",
  "shortcuts.category.general": "通用",
  "shortcuts.category.navigation": "网格导航",
  "shortcuts.category.notes": "说明",
  "shortcuts.close": "关闭",
};
```

- [ ] **Step 4: 创建 en-US locale**

`desktop/web-grid-v2/src/i18n/locales/en-US.ts`:
```ts
export const messages: Record<string, string> = {
  "app.title": "VibeTable",
  "app.loading": "Loading…",
  "app.ready": "Ready",

  "sidebar.tables": "Tables",
  "sidebar.newTable": "New table",
  "sidebar.admin": "Admin",
  "sidebar.delete": "Delete",
  "sidebar.delete.confirm": "Delete table \"{name}\"? This cannot be undone.",

  "toolbar.connectDirectus": "Connect Directus",
  "toolbar.refresh": "Refresh",
  "toolbar.rowCount": "{count} rows",
  "toolbar.help": "Shortcuts",
  "toolbar.theme": "Theme",

  "status.databaseOpening": "Connecting to database…",
  "status.databaseOpened": "Connected, {count} collections",
  "status.databaseFailed": "Connection failed: {message}",
  "status.tableLoading": "Loading table \"{name}\"…",
  "status.tableLoaded": "Loaded {count} rows",
  "status.tableLoadFailed": "Load failed: {message}",

  "paste.title.preview": "Paste preview",
  "paste.title.result": "Paste result",
  "paste.title.error": "Paste error",
  "paste.summary": "Will write {rows} rows × {cols} columns",
  "paste.overflow": "Clipboard exceeds the 10,000 cell limit. Use \"File import\" instead.",
  "paste.ack": "I acknowledge the warnings above",
  "paste.confirm": "Apply",
  "paste.cancel": "Cancel",
  "paste.empty": "Clipboard is empty",
  "paste.noTable": "Select a table before pasting",
  "paste.noFields": "Current table has no editable fields",

  "createTable.title": "New table",
  "createTable.name": "Table name",
  "createTable.fieldName": "Field name",
  "createTable.fieldType": "Type",
  "createTable.addField": "Add field",
  "createTable.removeField": "Remove",
  "createTable.submit": "Create",
  "createTable.cancel": "Cancel",

  "delete.title": "Confirm delete",
  "delete.confirm": "Delete",
  "delete.cancel": "Cancel",

  "error.title": "Error",
  "error.unknown": "Unknown error",

  "undo.succeeded": "Undone: {label}",
  "undo.failed.conflict": "Undo failed: data changed",
  "undo.failed.partial": "Some operations cannot be fully restored",
  "redo.succeeded": "Redone: {label}",

  "shortcuts.title": "Keyboard shortcuts",
  "shortcuts.category.general": "General",
  "shortcuts.category.navigation": "Grid navigation",
  "shortcuts.category.notes": "Notes",
  "shortcuts.close": "Close",
};
```

- [ ] **Step 5: 创建 i18n/index.ts**

`desktop/web-grid-v2/src/i18n/index.ts`:
```ts
import { messages as zhCN } from "./locales/zh-CN";
import { messages as enUS } from "./locales/en-US";

export type Locale = "zh-CN" | "en-US";

const locales: Record<Locale, Record<string, string>> = {
  "zh-CN": zhCN,
  "en-US": enUS,
};

let current: Locale = "zh-CN";

const STORAGE_KEY = "vt:locale";

/** Initialize locale from localStorage at module load. Call once in main.ts. */
export function initLocale(): void {
  const stored = localStorage.getItem(STORAGE_KEY) as Locale | null;
  if (stored && stored in locales) current = stored;
}

export function getLocale(): Locale {
  return current;
}

export function setLocale(locale: Locale): void {
  if (!(locale in locales)) return;
  current = locale;
  try {
    localStorage.setItem(STORAGE_KEY, locale);
  } catch {
    // localStorage may be unavailable (private mode); ignore.
  }
}

function interpolate(msg: string, params?: Record<string, string | number>): string {
  if (!params) return msg;
  return msg.replace(/\{(\w+)\}/g, (_, key: string) =>
    key in params ? String(params[key]) : `{${key}}`,
  );
}

export function t(key: string, params?: Record<string, string | number>): string {
  const msg = locales[current][key] ?? locales["zh-CN"][key] ?? key;
  return interpolate(msg, params);
}
```

- [ ] **Step 6: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- i18n
```
Expected: 4 个测试全 PASS。

- [ ] **Step 7: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add lightweight i18n (zh-CN default + en-US)"
```

---

## 阶段 2: 状态与服务层

### Task 5: bridgeContext — 提供给 services 用的 HostBridge 单例

**Files:**
- Create: `desktop/web-grid-v2/src/services/bridgeContext.ts`

**Interfaces:**
- Consumes: `createHostBridge` from `@/bridge/hostBridge`
- Produces: `useHostBridge(): HostBridge` — 返回单例 HostBridge 实例，所有 services 共用。

**Why:** Vue 的 `provide/inject` 适合组件树，但 services 是普通函数模块。用一个模块级单例 + 可注入（测试时 `setHostBridgeForTesting` 覆盖）最简单。

- [ ] **Step 1: 创建 bridgeContext.ts**

`desktop/web-grid-v2/src/services/bridgeContext.ts`:
```ts
import { createHostBridge } from "@/bridge/hostBridge";
import type { HostBridge } from "@/bridge/hostBridge";

// Module-level singleton. In production there is exactly one WebView2 host,
// so one bridge is correct. Tests call setHostBridgeForTesting() to inject a
// fake built via createHostBridge({ webview: shim }).
let singleton: HostBridge | null = null;

export function useHostBridge(): HostBridge {
  if (!singleton) {
    singleton = createHostBridge();
  }
  return singleton;
}

/** Test-only: inject a pre-built bridge (real or shim). */
export function setHostBridgeForTesting(bridge: HostBridge | null): void {
  singleton = bridge;
}
```

- [ ] **Step 2: 验证 typecheck**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck
```
Expected: 通过。

- [ ] **Step 3: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add bridgeContext singleton for services"
```

---

### Task 6: workspaceStore + workspaceService

**Files:**
- Create: `desktop/web-grid-v2/src/stores/workspaceStore.ts`
- Create: `desktop/web-grid-v2/src/stores/workspaceStore.test.ts`
- Create: `desktop/web-grid-v2/src/services/workspaceService.ts`

**Interfaces:**
- Consumes: `HostBridge`（via `useHostBridge`），contracts 的 `CollectionSummary` / `DatabaseOpenedPayload` / `DatabaseCollectionsChangedPayload`
- Produces: `useWorkspaceStore()` (Pinia)，`useWorkspaceService()`（init/selectTable/openDatabase）

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/stores/workspaceStore.test.ts`:
```ts
import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useWorkspaceStore } from "./workspaceStore";

describe("workspaceStore", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("starts idle with empty collections", () => {
    const s = useWorkspaceStore();
    expect(s.phase).toBe("idle");
    expect(s.collections).toEqual([]);
    expect(s.currentTable).toBeNull();
  });

  it("beginOpen moves to opening phase", () => {
    const s = useWorkspaceStore();
    s.beginOpen();
    expect(s.phase).toBe("opening");
  });

  it("setOpened stores collections and moves to opened", () => {
    const s = useWorkspaceStore();
    s.beginOpen();
    s.setOpened([{ collection: "users", metadata: {} }]);
    expect(s.phase).toBe("opened");
    expect(s.collections).toHaveLength(1);
  });

  it("selectTable sets currentTable", () => {
    const s = useWorkspaceStore();
    s.selectTable("orders");
    expect(s.currentTable).toBe("orders");
  });

  it("setFailed records error and moves to failed", () => {
    const s = useWorkspaceStore();
    s.beginOpen();
    s.setFailed("boom");
    expect(s.phase).toBe("failed");
    expect(s.lastError).toBe("boom");
  });

  it("clear resets to idle", () => {
    const s = useWorkspaceStore();
    s.setOpened([{ collection: "x", metadata: {} }]);
    s.selectTable("x");
    s.clear();
    expect(s.phase).toBe("idle");
    expect(s.collections).toEqual([]);
    expect(s.currentTable).toBeNull();
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- workspaceStore
```
Expected: FAIL（store 未定义）。

- [ ] **Step 3: 实现 workspaceStore**

`desktop/web-grid-v2/src/stores/workspaceStore.ts`:
```ts
import { defineStore } from "pinia";
import { ref } from "vue";
import type { CollectionSummary } from "@/contracts";

export type WorkspacePhase = "idle" | "opening" | "opened" | "failed";

export const useWorkspaceStore = defineStore("workspace", () => {
  const phase = ref<WorkspacePhase>("idle");
  const collections = ref<readonly CollectionSummary[]>([]);
  const currentTable = ref<string | null>(null);
  const lastError = ref<string | null>(null);

  function beginOpen(): void {
    phase.value = "opening";
    lastError.value = null;
  }

  function setOpened(cols: readonly CollectionSummary[]): void {
    collections.value = cols;
    phase.value = "opened";
    lastError.value = null;
  }

  function setCollections(cols: readonly CollectionSummary[]): void {
    collections.value = cols;
  }

  function selectTable(name: string): void {
    currentTable.value = name;
  }

  function setFailed(message: string): void {
    phase.value = "failed";
    lastError.value = message;
  }

  function clear(): void {
    phase.value = "idle";
    collections.value = [];
    currentTable.value = null;
    lastError.value = null;
  }

  return {
    phase,
    collections,
    currentTable,
    lastError,
    beginOpen,
    setOpened,
    setCollections,
    selectTable,
    setFailed,
    clear,
  };
});
```

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- workspaceStore
```
Expected: 6 个测试全 PASS。

- [ ] **Step 5: 实现 workspaceService**

`desktop/web-grid-v2/src/services/workspaceService.ts`:
```ts
import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import type { DatabaseOpenedPayload } from "@/contracts";

/** Subscribe to inbound host events for the workspace. Call once at app boot. */
export function useWorkspaceService(): {
  init: () => void;
  openDatabase: () => void;
} {
  const bridge = useHostBridge();
  const store = useWorkspaceStore();

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      store.setOpened(payload.collections);
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(payload.collections);
    });
  }

  function openDatabase(): void {
    store.beginOpen();
    bridge.notify("database.openRequested", {});
  }

  return { init, openDatabase };
}
```

- [ ] **Step 6: 验证 typecheck**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck
```
Expected: 通过。若 `DatabaseCollectionsChangedPayload` 类型名不符，查 contracts/index.ts 调整。

- [ ] **Step 7: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add workspaceStore + workspaceService"
```

---

### Task 7: tableStore + tableService（分页累积 + 增量更新基础）

**Files:**
- Create: `desktop/web-grid-v2/src/stores/tableStore.ts`
- Create: `desktop/web-grid-v2/src/stores/tableStore.test.ts`
- Create: `desktop/web-grid-v2/src/services/tableService.ts`

**Interfaces:**
- Consumes: `HostBridge`（via `useHostBridge`），contracts 的 `TablePage` / `ColumnSchema` / `TablePageLoadedPayload` / `DatasetReadyPayload`
- Produces: `useTableStore()`，含 `allRows` getter（pages 扁平化）；`useTableService()`（init/selectTable/refresh）

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/stores/tableStore.test.ts`:
```ts
import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTableStore } from "./tableStore";
import type { TablePage } from "@/contracts";

function makePage(rows: Record<string, unknown>[]): TablePage {
  return {
    tableName: "users",
    schemaRevision: "rev1",
    columns: [],
    rows,
    rowCount: rows.length,
    isLast: false,
    pageIndex: 0,
    pageCount: 1,
  } as unknown as TablePage;
}

describe("tableStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts idle with no rows", () => {
    const s = useTableStore();
    expect(s.loading).toBe(false);
    expect(s.allRows).toEqual([]);
    expect(s.schema).toBeNull();
  });

  it("beginLoad sets loading and clears previous data", () => {
    const s = useTableStore();
    s.appendPage(makePage([{ id: 1 }]));
    s.beginLoad();
    expect(s.loading).toBe(true);
    expect(s.allRows).toEqual([]);
  });

  it("appendPage accumulates rows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ id: 1 }, { id: 2 }]));
    s.appendPage(makePage([{ id: 3 }]));
    expect(s.allRows).toHaveLength(3);
    expect(s.rowCount).toBe(3);
  });

  it("setDatasetReady stores schema and ends loading", () => {
    const s = useTableStore();
    s.beginLoad();
    s.setDatasetReady([{ name: "id", type: "integer" } as never], 3);
    expect(s.loading).toBe(false);
    expect(s.datasetReady).toBe(true);
    expect(s.schema).toHaveLength(1);
    expect(s.rowCount).toBe(3);
  });

  it("setError ends loading and records error", () => {
    const s = useTableStore();
    s.beginLoad();
    s.setError("boom");
    expect(s.loading).toBe(false);
    expect(s.error).toBe("boom");
  });

  it("reset clears everything", () => {
    const s = useTableStore();
    s.appendPage(makePage([{ id: 1 }]));
    s.reset();
    expect(s.allRows).toEqual([]);
    expect(s.rowCount).toBe(0);
    expect(s.datasetReady).toBe(false);
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- tableStore
```
Expected: FAIL。

- [ ] **Step 3: 实现 tableStore**

`desktop/web-grid-v2/src/stores/tableStore.ts`:
```ts
import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type { ColumnSchema, TablePage } from "@/contracts";

export const useTableStore = defineStore("table", () => {
  const loading = ref(false);
  const datasetReady = ref(false);
  const pages = ref<TablePage[]>([]);
  const schema = ref<ColumnSchema[] | null>(null);
  const rowCount = ref(0);
  const error = ref<string | null>(null);

  const allRows = computed<Record<string, unknown>[]>(() =>
    pages.value.flatMap((p) => p.rows as Record<string, unknown>[]),
  );

  function beginLoad(): void {
    loading.value = true;
    datasetReady.value = false;
    pages.value = [];
    schema.value = null;
    rowCount.value = 0;
    error.value = null;
  }

  function appendPage(page: TablePage): void {
    pages.value.push(page);
  }

  function setDatasetReady(cols: ColumnSchema[], count: number): void {
    schema.value = cols;
    rowCount.value = count;
    datasetReady.value = true;
    loading.value = false;
    error.value = null;
  }

  function setError(message: string): void {
    error.value = message;
    loading.value = false;
  }

  function reset(): void {
    loading.value = false;
    datasetReady.value = false;
    pages.value = [];
    schema.value = null;
    rowCount.value = 0;
    error.value = null;
  }

  return {
    loading,
    datasetReady,
    pages,
    schema,
    rowCount,
    error,
    allRows,
    beginLoad,
    appendPage,
    setDatasetReady,
    setError,
    reset,
  };
});
```

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- tableStore
```
Expected: 6 个测试全 PASS。

- [ ] **Step 5: 实现 tableService**

`desktop/web-grid-v2/src/services/tableService.ts`:
```ts
import { useHostBridge } from "./bridgeContext";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import type {
  DatasetReadyPayload,
  TablePageLoadedPayload,
} from "@/contracts";

export function useTableService(): {
  init: () => void;
  selectTable: (name: string) => void;
  refresh: () => void;
} {
  const bridge = useHostBridge();
  const tableStore = useTableStore();
  const workspaceStore = useWorkspaceStore();

  function init(): void {
    bridge.on("table.pageLoaded", (payload: TablePageLoadedPayload) => {
      tableStore.appendPage(payload.page);
    });
    bridge.on("table.datasetReady", (payload: DatasetReadyPayload) => {
      tableStore.setDatasetReady(payload.columns, payload.rowCount);
    });
  }

  function selectTable(name: string): void {
    workspaceStore.selectTable(name);
    tableStore.reset();
    tableStore.beginLoad();
    bridge.notify("table.selected", { table: name });
  }

  function refresh(): void {
    const current = workspaceStore.currentTable;
    if (!current) return;
    tableStore.reset();
    tableStore.beginLoad();
    bridge.notify("table.selected", { table: current });
  }

  return { init, selectTable, refresh };
}
```

- [ ] **Step 6: 验证 typecheck（修正 contracts 类型名）**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck
```
若 `TablePageLoadedPayload` / `DatasetReadyPayload` 名字不符，查 `src/contracts/index.ts` 用准确名（可能是 `TablePageLoadedPayload` 对应 `table.pageLoaded` 的 payload 类型）。

- [ ] **Step 7: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add tableStore (incremental paging) + tableService"
```

---

### Task 8: pasteStore + pasteService

**Files:**
- Create: `desktop/web-grid-v2/src/stores/pasteStore.ts`
- Create: `desktop/web-grid-v2/src/stores/pasteStore.test.ts`
- Create: `desktop/web-grid-v2/src/services/pasteService.ts`

**Interfaces:**
- Consumes: `HostBridge`，contracts 的 `PastePlan` / `ApplyPasteResult` / `PastePreviewReadyPayload` / `PasteAppliedPayload`，旧 `pasteFlow.ts` 的纯函数（`summaryLine`/`outcomeLine`/`errorsByRow`）
- Produces: `usePasteStore()`，含 phase 状态机 + `summaryText` getter；`usePasteService()`（preview/apply/init）

- [ ] **Step 1: 拷贝 pasteFlow 纯函数**

把旧 `desktop/web-grid/src/pasteFlow.ts` 里的纯函数（`summaryLine`/`outcomeLine`/`errorsByRow` 及其依赖的类型）提取到新文件。

`desktop/web-grid-v2/src/stores/pasteFlowHelpers.ts`:
```ts
// Pure helpers extracted from the old pasteFlow.ts. State lives in pasteStore;
// these functions only compute derived text from a PastePlan / ApplyPasteResult.
import type { ApplyPasteResult, PastePlan, PasteCellDiagnostic } from "@/contracts";

/** Group diagnostics by row index for the preview panel. */
export function errorsByRow(
  plan: PastePlan | null,
): Array<{ rowIndex: number; diagnostics: PasteCellDiagnostic[] }> {
  if (!plan?.cells) return [];
  const byRow = new Map<number, PasteCellDiagnostic[]>();
  for (const cell of plan.cells) {
    const d = (cell as { diagnostics?: PasteCellDiagnostic[] }).diagnostics ?? [];
    for (const diag of d) {
      const row = diag.rowIndex ?? 0;
      if (!byRow.has(row)) byRow.set(row, []);
      byRow.get(row)!.push(diag);
    }
  }
  return Array.from(byRow.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([rowIndex, diagnostics]) => ({ rowIndex, diagnostics }));
}

/** One-line summary like "将写入 12 行 × 3 列". */
export function summaryLine(plan: PastePlan | null): string {
  if (!plan) return "";
  return `将写入 ${plan.rows ?? 0} 行 × ${plan.columns ?? 0} 列`;
}

/** Outcome line after apply, e.g. "已创建 5 行，更新 7 行". */
export function outcomeLine(result: ApplyPasteResult | null): string {
  if (!result) return "";
  const parts: string[] = [];
  if (result.createdRowKeys.length) parts.push(`创建 ${result.createdRowKeys.length} 行`);
  if (result.updatedRowKeys.length) parts.push(`更新 ${result.updatedRowKeys.length} 行`);
  if (result.skippedRowKeys.length) parts.push(`跳过 ${result.skippedRowKeys.length} 行`);
  return parts.length ? `已${parts.join("，")}` : "无变更";
}
```
注：`PastePlan` 的真实字段名（`rows`/`columns`/`cells`）需对照 `contracts/index.ts` 修正。如果字段名不同，调整为准确名。

- [ ] **Step 2: 写失败测试**

`desktop/web-grid-v2/src/stores/pasteStore.test.ts`:
```ts
import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { usePasteStore } from "./pasteStore";

describe("pasteStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts idle", () => {
    const s = usePasteStore();
    expect(s.phase).toBe("idle");
    expect(s.plan).toBeNull();
    expect(s.acked).toBe(false);
  });

  it("setPlan moves to previewing and resets ack", () => {
    const s = usePasteStore();
    s.setPlan({ rows: 2, columns: 3, cells: [] } as never);
    expect(s.phase).toBe("previewing");
    expect(s.acked).toBe(false);
  });

  it("toggleAck flips acknowledgement", () => {
    const s = usePasteStore();
    s.toggleAck();
    expect(s.acked).toBe(true);
    s.toggleAck();
    expect(s.acked).toBe(false);
  });

  it("beginApply moves to applying", () => {
    const s = usePasteStore();
    s.beginApply();
    expect(s.phase).toBe("applying");
  });

  it("setResult moves to applied", () => {
    const s = usePasteStore();
    s.setResult({ createdRowKeys: [], updatedRowKeys: [], skippedRowKeys: [], conflicts: [], collection: "", outcome: "committed", requestId: "" } as never);
    expect(s.phase).toBe("applied");
  });

  it("setError moves to error", () => {
    const s = usePasteStore();
    s.setError("bad");
    expect(s.phase).toBe("error");
    expect(s.error).toBe("bad");
  });

  it("reset returns to idle", () => {
    const s = usePasteStore();
    s.setPlan({ rows: 1, columns: 1, cells: [] } as never);
    s.reset();
    expect(s.phase).toBe("idle");
    expect(s.plan).toBeNull();
  });
});
```

- [ ] **Step 3: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- pasteStore
```
Expected: FAIL。

- [ ] **Step 4: 实现 pasteStore**

`desktop/web-grid-v2/src/stores/pasteStore.ts`:
```ts
import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type { ApplyPasteResult, PastePlan } from "@/contracts";

export type PastePhase =
  | "idle"
  | "previewing"
  | "applying"
  | "applied"
  | "error"
  | "overflow";

export const usePasteStore = defineStore("paste", () => {
  const phase = ref<PastePhase>("idle");
  const plan = ref<PastePlan | null>(null);
  const result = ref<ApplyPasteResult | null>(null);
  const acked = ref(false);
  const error = ref<string | null>(null);
  const cellCount = ref(0);

  const summaryText = computed(() => {
    if (phase.value === "applied" && result.value) return appliedSummary(result.value);
    if (phase.value === "overflow") return "";
    if (plan.value) return `将写入 ${rowCount(plan.value)} 行 × ${colCount(plan.value)} 列`;
    return "";
  });

  function setPlan(p: PastePlan, count = 0): void {
    plan.value = p;
    result.value = null;
    acked.value = false;
    error.value = null;
    cellCount.value = count;
    phase.value = "previewing";
  }

  function setOverflow(count: number): void {
    plan.value = null;
    result.value = null;
    cellCount.value = count;
    phase.value = "overflow";
  }

  function toggleAck(): void {
    acked.value = !acked.value;
  }

  function beginApply(): void {
    phase.value = "applying";
    error.value = null;
  }

  function setResult(r: ApplyPasteResult): void {
    result.value = r;
    phase.value = "applied";
    error.value = null;
  }

  function setError(message: string): void {
    error.value = message;
    phase.value = "error";
  }

  function reset(): void {
    phase.value = "idle";
    plan.value = null;
    result.value = null;
    acked.value = false;
    error.value = null;
    cellCount.value = 0;
  }

  return {
    phase,
    plan,
    result,
    acked,
    error,
    cellCount,
    summaryText,
    setPlan,
    setOverflow,
    toggleAck,
    beginApply,
    setResult,
    setError,
    reset,
  };
});

// Local helpers (kept here to avoid coupling pasteStore to pasteFlowHelpers
// for the simple count cases; richer diagnostics come from the component).
function rowCount(plan: PastePlan): number {
  return (plan as { rows?: number }).rows ?? 0;
}
function colCount(plan: PastePlan): number {
  return (plan as { columns?: number }).columns ?? 0;
}
function appliedSummary(r: ApplyPasteResult): string {
  const parts: string[] = [];
  if (r.createdRowKeys.length) parts.push(`创建 ${r.createdRowKeys.length} 行`);
  if (r.updatedRowKeys.length) parts.push(`更新 ${r.updatedRowKeys.length} 行`);
  if (r.skippedRowKeys.length) parts.push(`跳过 ${r.skippedRowKeys.length} 行`);
  return parts.length ? `已${parts.join("，")}` : "无变更";
}
```

- [ ] **Step 5: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- pasteStore
```
Expected: 7 个测试全 PASS。

- [ ] **Step 6: 实现 pasteService**

`desktop/web-grid-v2/src/services/pasteService.ts`:
```ts
import { useHostBridge } from "./bridgeContext";
import { usePasteStore } from "@/stores/pasteStore";
import { PASTE_CELL_LIMIT } from "@/grid/clipboardParser";
import type {
  ApplyPasteResult,
  PasteAppliedPayload,
  PastePlan,
  PastePreviewReadyPayload,
} from "@/contracts";

export function usePasteService(): {
  init: () => void;
  preview: (clipboardText: string, table: string) => Promise<void>;
  apply: (token: string) => void;
} {
  const bridge = useHostBridge();
  const store = usePasteStore();

  function init(): void {
    bridge.on("table.pastePreviewReady", (payload: PastePreviewReadyPayload) => {
      store.setPlan(payload.plan, payload.cellCount ?? 0);
    });
    bridge.on("table.pasteApplied", (payload: PasteAppliedPayload) => {
      store.setResult(payload.result as ApplyPasteResult);
    });
  }

  async function preview(clipboardText: string, table: string): Promise<void> {
    bridge.notify("table.previewPasteRequested", { clipboardText, table });
  }

  function apply(token: string): void {
    store.beginApply();
    bridge.notify("table.applyPasteRequested", { token } as never);
  }

  // reference PASTE_CELL_LIMIT so the import is not tree-shaken (used by
  // pasteContext.ts at the grid layer; documented here for service-level
  // awareness of the cap).
  void PASTE_CELL_LIMIT;

  return { init, preview, apply };
}
```
注：`PastePreviewReadyPayload` / `PasteAppliedPayload` / `applyPasteRequested` 的真实 payload 形状以 contracts 为准——核对 `token` 字段名、`table.previewPasteRequested` 的入参（可能是 `{ clipboardText, table }` 或别的）。若不符，用准确字段。

- [ ] **Step 7: 验证 typecheck**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck
```
修正所有类型不匹配。

- [ ] **Step 8: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add pasteStore + pasteService"
```

---

### Task 9: tableAdminStore（表单状态入 store）+ tableAdminService

**Files:**
- Create: `desktop/web-grid-v2/src/stores/tableAdminStore.ts`
- Create: `desktop/web-grid-v2/src/stores/tableAdminStore.test.ts`
- Create: `desktop/web-grid-v2/src/services/tableAdminService.ts`

**Interfaces:**
- Consumes: `HostBridge`，contracts 的 `CollectionSummary` / `TABLE_FIELD_TYPES` / `TABLE_NAME_PATTERN`，复用的 `tableAdminValidation.ts`
- Produces: `useTableAdminStore()`，含 `form: { name, fields: FieldRow[] }`；`useTableAdminService()`（createTable/deleteTable/init）

**Key architecture fix:** 表单状态从 DOM 移到 store（修架构债 #3）。

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/stores/tableAdminStore.test.ts`:
```ts
import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTableAdminStore } from "./tableAdminStore";

describe("tableAdminStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts idle with empty form", () => {
    const s = useTableAdminStore();
    expect(s.phase).toBe("idle");
    expect(s.form.name).toBe("");
    expect(s.form.fields).toEqual([]);
    expect(s.canSubmit).toBe(false);
  });

  it("openCreate resets form to one empty field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    expect(s.phase).toBe("creating");
    expect(s.form.fields).toHaveLength(1);
  });

  it("addField appends an empty field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.addField();
    expect(s.form.fields).toHaveLength(2);
  });

  it("updateField patches a single field by index", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.updateField(0, { name: "id", type: "integer" });
    expect(s.form.fields[0]).toMatchObject({ name: "id", type: "integer" });
  });

  it("removeField removes by index", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.addField();
    s.removeField(0);
    expect(s.form.fields).toHaveLength(1);
  });

  it("canSubmit true when name + at least one named field", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.form.name = "orders";
    s.updateField(0, { name: "id" });
    expect(s.canSubmit).toBe(true);
  });

  it("requestDelete sets pendingDelete and phase=deleting", () => {
    const s = useTableAdminStore();
    s.requestDelete("users");
    expect(s.phase).toBe("deleting");
    expect(s.pendingDelete).toBe("users");
  });

  it("succeed returns to idle and clears form", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.succeed();
    expect(s.phase).toBe("idle");
    expect(s.form.name).toBe("");
  });

  it("fail records error", () => {
    const s = useTableAdminStore();
    s.openCreate();
    s.fail("bad name");
    expect(s.phase).toBe("failed");
    expect(s.error).toBe("bad name");
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- tableAdminStore
```
Expected: FAIL。

- [ ] **Step 3: 实现 tableAdminStore**

`desktop/web-grid-v2/src/stores/tableAdminStore.ts`:
```ts
import { defineStore } from "pinia";
import { computed, reactive, ref } from "vue";
import type { CollectionSummary } from "@/contracts";
import { TABLE_FIELD_TYPES } from "@/contracts";
import { isValidTableName, isValidFieldName } from "@/services/tableAdminValidation";

export type TableAdminPhase = "idle" | "creating" | "deleting" | "failed";

export interface FieldRow {
  name: string;
  type: string;
}

function emptyField(): FieldRow {
  return { name: "", type: TABLE_FIELD_TYPES[0] };
}

export const useTableAdminStore = defineStore("tableAdmin", () => {
  const phase = ref<TableAdminPhase>("idle");
  const collections = ref<readonly CollectionSummary[]>([]);
  const pendingDelete = ref<string | null>(null);
  const error = ref<string | null>(null);

  // Form state lives in the store, NOT in the DOM (architecture fix #3).
  const form = reactive({
    name: "" as string,
    fields: [] as FieldRow[],
  });

  const canSubmit = computed(() => {
    if (phase.value !== "creating") return false;
    if (!isValidTableName(form.name)) return false;
    const named = form.fields.filter((f) => isValidFieldName(f.name));
    return named.length >= 1;
  });

  function setCollections(cols: readonly CollectionSummary[]): void {
    collections.value = cols;
  }

  function openCreate(): void {
    phase.value = "creating";
    form.name = "";
    form.fields = [emptyField()];
    error.value = null;
  }

  function addField(): void {
    form.fields.push(emptyField());
  }

  function updateField(index: number, patch: Partial<FieldRow>): void {
    if (index < 0 || index >= form.fields.length) return;
    form.fields[index] = { ...form.fields[index], ...patch };
  }

  function removeField(index: number): void {
    if (index < 0 || index >= form.fields.length) return;
    form.fields.splice(index, 1);
  }

  function beginSubmit(): void {
    // phase stays "creating" until success/fail; service drives notify.
    error.value = null;
  }

  function requestDelete(name: string): void {
    pendingDelete.value = name;
    phase.value = "deleting";
    error.value = null;
  }

  function succeed(): void {
    phase.value = "idle";
    form.name = "";
    form.fields = [];
    pendingDelete.value = null;
    error.value = null;
  }

  function fail(message: string): void {
    phase.value = "failed";
    error.value = message;
  }

  function close(): void {
    phase.value = "idle";
    pendingDelete.value = null;
    error.value = null;
  }

  return {
    phase,
    collections,
    pendingDelete,
    error,
    form,
    canSubmit,
    setCollections,
    openCreate,
    addField,
    updateField,
    removeField,
    beginSubmit,
    requestDelete,
    succeed,
    fail,
    close,
  };
});
```

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- tableAdminStore
```
Expected: 9 个测试全 PASS。若 `isValidTableName` / `isValidFieldName` 的真实导出名不符，查 `services/tableAdminValidation.ts` 调整。

- [ ] **Step 5: 实现 tableAdminService**

`desktop/web-grid-v2/src/services/tableAdminService.ts`:
```ts
import { useHostBridge } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import type { DatabaseOpenedPayload } from "@/contracts";

export function useTableAdminService(): {
  init: () => void;
  createTable: () => void;
  deleteTable: (name: string) => void;
  openAdmin: () => void;
} {
  const bridge = useHostBridge();
  const store = useTableAdminStore();

  function init(): void {
    bridge.on("database.opened", (payload: DatabaseOpenedPayload) => {
      store.setCollections(payload.collections);
    });
    bridge.on("database.collectionsChanged", (payload) => {
      store.setCollections(payload.collections);
    });
  }

  function createTable(): void {
    if (!store.canSubmit) return;
    store.beginSubmit();
    bridge.notify("tableAdmin.createRequested", {
      name: store.form.name,
      fields: store.form.fields.map((f) => ({ name: f.name, type: f.type })),
    });
  }

  function deleteTable(name: string): void {
    store.requestDelete(name);
    bridge.notify("tableAdmin.deleteRequested", { name });
  }

  function openAdmin(): void {
    bridge.notify("admin.openRequested", {});
  }

  return { init, createTable, deleteTable, openAdmin };
}
```
注：`tableAdmin.createRequested` / `tableAdmin.deleteRequested` 的 payload 形状以 contracts 为准——核对 `fields` 是 `{name, type}` 还是别的形状。

- [ ] **Step 6: 验证 typecheck**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck
```

- [ ] **Step 7: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add tableAdminStore (form state in store) + service"
```

---

### Task 10: uiStore（modal/panel 开关 + 主题模式）

**Files:**
- Create: `desktop/web-grid-v2/src/stores/uiStore.ts`
- Create: `desktop/web-grid-v2/src/stores/uiStore.test.ts`

**Interfaces:**
- Produces: `useUiStore()` — `createModalOpen` / `deleteModalOpen` / `pastePanelOpen` / `shortcutsOpen` / `themeMode`

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/stores/uiStore.test.ts`:
```ts
import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useUiStore } from "./uiStore";

describe("uiStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts with all panels closed and theme=system", () => {
    const s = useUiStore();
    expect(s.createModalOpen).toBe(false);
    expect(s.deleteModalOpen).toBe(false);
    expect(s.pastePanelOpen).toBe(false);
    expect(s.shortcutsOpen).toBe(false);
    expect(s.themeMode).toBe("system");
  });

  it("openCreate/closeCreate toggles createModalOpen", () => {
    const s = useUiStore();
    s.openCreate();
    expect(s.createModalOpen).toBe(true);
    s.closeCreate();
    expect(s.createModalOpen).toBe(false);
  });

  it("openDelete/closeDelete toggles deleteModalOpen", () => {
    const s = useUiStore();
    s.openDelete("users");
    expect(s.deleteModalOpen).toBe(true);
    expect(s.deleteTarget).toBe("users");
    s.closeDelete();
    expect(s.deleteModalOpen).toBe(false);
  });

  it("openShortcuts/closeShortcuts toggles shortcutsOpen", () => {
    const s = useUiStore();
    s.openShortcuts();
    expect(s.shortcutsOpen).toBe(true);
    s.closeShortcuts();
    expect(s.shortcutsOpen).toBe(false);
  });

  it("setThemeMode persists to localStorage", () => {
    const s = useUiStore();
    s.setThemeMode("dark");
    expect(s.themeMode).toBe("dark");
    expect(localStorage.getItem("vt:theme")).toBe("dark");
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- uiStore
```
Expected: FAIL。

- [ ] **Step 3: 实现 uiStore**

`desktop/web-grid-v2/src/stores/uiStore.ts`:
```ts
import { defineStore } from "pinia";
import { ref } from "vue";

export type ThemeMode = "light" | "dark" | "system";

const THEME_KEY = "vt:theme";

function loadThemeMode(): ThemeMode {
  const stored = localStorage.getItem(THEME_KEY) as ThemeMode | null;
  return stored === "light" || stored === "dark" || stored === "system" ? stored : "system";
}

export const useUiStore = defineStore("ui", () => {
  const createModalOpen = ref(false);
  const deleteModalOpen = ref(false);
  const deleteTarget = ref<string | null>(null);
  const pastePanelOpen = ref(false);
  const shortcutsOpen = ref(false);
  const themeMode = ref<ThemeMode>(loadThemeMode());

  function openCreate(): void {
    createModalOpen.value = true;
  }
  function closeCreate(): void {
    createModalOpen.value = false;
  }
  function openDelete(name: string): void {
    deleteTarget.value = name;
    deleteModalOpen.value = true;
  }
  function closeDelete(): void {
    deleteModalOpen.value = false;
    deleteTarget.value = null;
  }
  function openPastePanel(): void {
    pastePanelOpen.value = true;
  }
  function closePastePanel(): void {
    pastePanelOpen.value = false;
  }
  function openShortcuts(): void {
    shortcutsOpen.value = true;
  }
  function closeShortcuts(): void {
    shortcutsOpen.value = false;
  }
  function setThemeMode(m: ThemeMode): void {
    themeMode.value = m;
    try {
      localStorage.setItem(THEME_KEY, m);
    } catch {
      // ignore
    }
  }

  return {
    createModalOpen,
    deleteModalOpen,
    deleteTarget,
    pastePanelOpen,
    shortcutsOpen,
    themeMode,
    openCreate,
    closeCreate,
    openDelete,
    closeDelete,
    openPastePanel,
    closePastePanel,
    openShortcuts,
    closeShortcuts,
    setThemeMode,
  };
});
```

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- uiStore
```
Expected: 5 个测试全 PASS。

- [ ] **Step 5: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add uiStore (modal/panel toggles + theme mode)"
```

---

### Task 11: historyStore（撤销/重做栈）

**Files:**
- Create: `desktop/web-grid-v2/src/stores/historyStore.ts`
- Create: `desktop/web-grid-v2/src/stores/historyStore.test.ts`

**Interfaces:**
- Consumes: 无外部依赖（纯栈管理 + 反向操作回调）
- Produces: `useHistoryStore()` — `push(entry)` / `undo()` / `redo()` / `clear()`，`canUndo` / `canRedo` getters

**Key constraint (spec §7.3):** undo 范围限制——只接受 `updateCell`/`insertRow`/`deleteRows`/`applyPaste`；schemaRevision 变化时清空。

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/stores/historyStore.test.ts`:
```ts
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useHistoryStore } from "./historyStore";

describe("historyStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts empty", () => {
    const s = useHistoryStore();
    expect(s.canUndo).toBe(false);
    expect(s.canRedo).toBe(false);
  });

  it("push makes undo available", () => {
    const s = useHistoryStore();
    s.push({ kind: "updateCell", label: "edit", undo: vi.fn(), redo: vi.fn() } as never);
    expect(s.canUndo).toBe(true);
    expect(s.canRedo).toBe(false);
  });

  it("undo calls entry.undo and moves to redo stack", async () => {
    const s = useHistoryStore();
    const undo = vi.fn().mockResolvedValue(undefined);
    const redo = vi.fn().mockResolvedValue(undefined);
    s.push({ kind: "updateCell", label: "edit", undo, redo } as never);
    await s.undo();
    expect(undo).toHaveBeenCalledOnce();
    expect(s.canUndo).toBe(false);
    expect(s.canRedo).toBe(true);
  });

  it("redo calls entry.redo and moves back to undo stack", async () => {
    const s = useHistoryStore();
    const undo = vi.fn().mockResolvedValue(undefined);
    const redo = vi.fn().mockResolvedValue(undefined);
    s.push({ kind: "updateCell", label: "edit", undo, redo } as never);
    await s.undo();
    await s.redo();
    expect(redo).toHaveBeenCalledOnce();
    expect(s.canUndo).toBe(true);
    expect(s.canRedo).toBe(false);
  });

  it("clear empties both stacks", () => {
    const s = useHistoryStore();
    s.push({ kind: "updateCell", label: "x", undo: vi.fn(), redo: vi.fn() } as never);
    s.clear();
    expect(s.canUndo).toBe(false);
    expect(s.canRedo).toBe(false);
  });

  it("stack caps at 50 entries (FIFO)", () => {
    const s = useHistoryStore();
    for (let i = 0; i < 55; i++) {
      s.push({ kind: "updateCell", label: `e${i}`, undo: vi.fn(), redo: vi.fn() } as never);
    }
    // Internal stack length capped; verify via undo behavior on oldest.
    expect(s.undoStackSize).toBe(50);
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- historyStore
```
Expected: FAIL。

- [ ] **Step 3: 实现 historyStore**

`desktop/web-grid-v2/src/stores/historyStore.ts`:
```ts
import { defineStore } from "pinia";
import { computed, ref } from "vue";

export type HistoryEntryKind =
  | "updateCell"
  | "insertRow"
  | "deleteRows"
  | "applyPaste";

export interface HistoryEntry {
  readonly id: string;
  readonly kind: HistoryEntryKind;
  readonly label: string;
  readonly timestamp: number;
  readonly undo: () => Promise<void>;
  readonly redo: () => Promise<void>;
}

const MAX_STACK = 50;

export const useHistoryStore = defineStore("history", () => {
  const undoStack = ref<HistoryEntry[]>([]);
  const redoStack = ref<HistoryEntry[]>([]);
  const lastError = ref<string | null>(null);

  const canUndo = computed(() => undoStack.value.length > 0);
  const canRedo = computed(() => redoStack.value.length > 0);
  const undoStackSize = computed(() => undoStack.value.length);

  function push(entry: HistoryEntry): void {
    undoStack.value.push(entry);
    // Clear redo on new action (standard undo semantics).
    redoStack.value = [];
    // FIFO cap.
    if (undoStack.value.length > MAX_STACK) {
      undoStack.value.shift();
    }
  }

  async function undo(): Promise<void> {
    lastError.value = null;
    const entry = undoStack.value.pop();
    if (!entry) return;
    try {
      await entry.undo();
      redoStack.value.push(entry);
    } catch (err) {
      lastError.value = err instanceof Error ? err.message : "undo failed";
      // Put it back so the user can retry after resolving the conflict.
      undoStack.value.push(entry);
    }
  }

  async function redo(): Promise<void> {
    lastError.value = null;
    const entry = redoStack.value.pop();
    if (!entry) return;
    try {
      await entry.redo();
      undoStack.value.push(entry);
    } catch (err) {
      lastError.value = err instanceof Error ? err.message : "redo failed";
      redoStack.value.push(entry);
    }
  }

  function clear(): void {
    undoStack.value = [];
    redoStack.value = [];
    lastError.value = null;
  }

  return {
    undoStack,
    redoStack,
    lastError,
    canUndo,
    canRedo,
    undoStackSize,
    push,
    undo,
    redo,
    clear,
  };
});
```

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- historyStore
```
Expected: 6 个测试全 PASS。

- [ ] **Step 5: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add historyStore (undo/redo stack, cap 50)"
```

---

### Task 12: keyboardStore + shortcuts 数据源

**Files:**
- Create: `desktop/web-grid-v2/src/keyboard/shortcuts.ts`
- Create: `desktop/web-grid-v2/src/stores/keyboardStore.ts`
- Create: `desktop/web-grid-v2/src/stores/keyboardStore.test.ts`

**Interfaces:**
- Produces: `SHORTCUTS` 数组（说明页 + 注册器共用），`useKeyboardStore()`（`lastFired` 用于测试）

- [ ] **Step 1: 创建 shortcuts.ts（单一数据源）**

`desktop/web-grid-v2/src/keyboard/shortcuts.ts`:
```ts
export type ShortcutScope = "global" | "grid";
export type ShortcutCategory = "general" | "navigation" | "notes";

export interface ShortcutDef {
  readonly id: string;
  readonly keys: string;          // display, e.g. "Ctrl+C"
  readonly action: string;        // internal action id, e.g. "copy"
  readonly scope: ShortcutScope;
  readonly category: ShortcutCategory;
  readonly descriptionZh: string;
  readonly descriptionEn: string;
}

// Single source of truth: both the registration layer (useKeyboard) and the
// help page (ShortcutsView) consume this array. Adding a shortcut here makes
// it appear in both.
export const SHORTCUTS: readonly ShortcutDef[] = [
  // General
  { id: "copy", keys: "Ctrl+C", action: "copy", scope: "grid", category: "general",
    descriptionZh: "复制选中单元格为 TSV", descriptionEn: "Copy selected cells as TSV" },
  { id: "paste", keys: "Ctrl+V", action: "paste", scope: "grid", category: "general",
    descriptionZh: "粘贴（进入预览面板）", descriptionEn: "Paste (open preview panel)" },
  { id: "undo", keys: "Ctrl+Z", action: "undo", scope: "global", category: "general",
    descriptionZh: "撤销最近一次可撤销操作", descriptionEn: "Undo last undoable action" },
  { id: "redo", keys: "Ctrl+Shift+Z", action: "redo", scope: "global", category: "general",
    descriptionZh: "重做", descriptionEn: "Redo" },
  { id: "redo-y", keys: "Ctrl+Y", action: "redo", scope: "global", category: "general",
    descriptionZh: "重做（另一绑定）", descriptionEn: "Redo (alternate)" },
  { id: "refresh", keys: "Ctrl+R", action: "refresh", scope: "global", category: "general",
    descriptionZh: "刷新当前表", descriptionEn: "Refresh current table" },
  { id: "new-table", keys: "Ctrl+N", action: "newTable", scope: "global", category: "general",
    descriptionZh: "新建表（打开窗口）", descriptionEn: "New table (open dialog)" },
  { id: "help", keys: "?", action: "help", scope: "global", category: "general",
    descriptionZh: "打开快捷键说明页", descriptionEn: "Open shortcuts help" },

  // Navigation
  { id: "enter", keys: "Enter", action: "commitOrDown", scope: "grid", category: "navigation",
    descriptionZh: "进入编辑 / 向下移动", descriptionEn: "Edit cell / move down" },
  { id: "esc", keys: "Esc", action: "cancel", scope: "global", category: "navigation",
    descriptionZh: "取消编辑 / 关闭面板", descriptionEn: "Cancel edit / close panel" },
  { id: "tab", keys: "Tab", action: "moveRight", scope: "grid", category: "navigation",
    descriptionZh: "向右移动", descriptionEn: "Move right" },
  { id: "shift-tab", keys: "Shift+Tab", action: "moveLeft", scope: "grid", category: "navigation",
    descriptionZh: "向左移动", descriptionEn: "Move left" },
  { id: "arrows", keys: "方向键", action: "moveSelection", scope: "grid", category: "navigation",
    descriptionZh: "移动选中单元格", descriptionEn: "Move selection" },
  { id: "select-all", keys: "Ctrl+A", action: "selectAll", scope: "grid", category: "navigation",
    descriptionZh: "全选当前表所有行", descriptionEn: "Select all rows" },
  { id: "delete", keys: "Delete", action: "deleteRows", scope: "grid", category: "navigation",
    descriptionZh: "删除选中行（弹确认）", descriptionEn: "Delete selected rows (confirm)" },
  { id: "f2", keys: "F2", action: "editCell", scope: "grid", category: "navigation",
    descriptionZh: "进入单元格编辑", descriptionEn: "Edit cell" },
] as const;

export const UNDO_LIMITATIONS_ZH: readonly string[] = [
  "单元格编辑、插入行可完全撤销。",
  "删除行的撤销依赖前端缓存的行快照；若快照与当前 schema 不符，撤销会失败并提示。",
  "粘贴包含已更新行时，撤销无法恢复这些行的原始值，只能撤销新增部分。",
  "表结构变更（建表、删表、字段变更）不可撤销。",
  "切换表、刷新、schema 变更后撤销栈清空。",
];

export const UNDO_LIMITATIONS_EN: readonly string[] = [
  "Cell edits and row inserts can be fully undone.",
  "Row-delete undo relies on a front-end row snapshot; if it no longer matches the schema, undo fails with a notice.",
  "Paste undo cannot restore original values of updated rows; only inserted rows can be undone.",
  "Schema changes (create/delete table, field changes) are not undoable.",
  "The undo stack clears when switching tables, refreshing, or on schema changes.",
];
```

- [ ] **Step 2: 写失败测试**

`desktop/web-grid-v2/src/stores/keyboardStore.test.ts`:
```ts
import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useKeyboardStore } from "./keyboardStore";

describe("keyboardStore", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("starts with lastFired null", () => {
    const s = useKeyboardStore();
    expect(s.lastFired).toBeNull();
  });

  it("fire records the action id", () => {
    const s = useKeyboardStore();
    s.fire("copy");
    expect(s.lastFired).toBe("copy");
  });
});
```

- [ ] **Step 3: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- keyboardStore
```
Expected: FAIL。

- [ ] **Step 4: 实现 keyboardStore**

`desktop/web-grid-v2/src/stores/keyboardStore.ts`:
```ts
import { defineStore } from "pinia";
import { ref } from "vue";

// Minimal store: tracks the last fired action id (for testing + debugging).
// The actual key-binding lives in composables/useKeyboard.ts.
export const useKeyboardStore = defineStore("keyboard", () => {
  const lastFired = ref<string | null>(null);

  function fire(action: string): void {
    lastFired.value = action;
  }

  return { lastFired, fire };
});
```

- [ ] **Step 5: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- keyboardStore
```
Expected: 2 个测试全 PASS。

- [ ] **Step 6: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add shortcuts data source + keyboardStore"
```

---

### Task 13: errorRouter — operation.failed 集中路由

**Files:**
- Create: `desktop/web-grid-v2/src/services/errorRouter.ts`
- Create: `desktop/web-grid-v2/src/services/errorRouter.test.ts`

**Interfaces:**
- Consumes: `HostBridge`（via `useHostBridge`），`tableAdminStore` / `tableStore` / `pasteStore` 的当前 phase
- Produces: `useErrorRouter().init()` — 订阅 `operation.failed`，按当前活动操作路由到对应 store

**Key architecture fix:** 替换 `main.ts:524-538` 散落的路由逻辑（修架构债 #4）。

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/services/errorRouter.test.ts`:
```ts
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useTableStore } from "@/stores/tableStore";
import { usePasteStore } from "@/stores/pasteStore";
import { useErrorRouter } from "./errorRouter";

// Build a bridge with a controllable inbound shim.
function makeShimBridge(): { bridge: HostBridge; emit: (type: string, payload: unknown) => void } {
  let listener: ((e: { data: unknown }) => void) | null = null;
  const shim = {
    addEventListener: (_: string, fn: (e: { data: unknown }) => void) => { listener = fn; },
    removeEventListener: (_: string, fn: (e: { data: unknown }) => void) => { if (listener === fn) listener = null; },
    postMessage: () => {},
  };
  const bridge = createHostBridge({ webview: () => shim });
  bridge.start();
  return {
    bridge,
    emit: (type, payload) => listener?.({ data: typeof payload === "string" ? payload : JSON.stringify({ type, payload }) }),
  };
}

describe("errorRouter", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("routes operation.failed to tableAdmin when creating", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    const table = useTableStore();
    admin.openCreate();  // phase = creating
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "bad name", code: "X" });
    expect(admin.phase).toBe("failed");
    expect(admin.error).toBe("bad name");
  });

  it("routes to tableAdmin when deleting", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const admin = useTableAdminStore();
    admin.requestDelete("users");
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "cannot delete", code: "X" });
    expect(admin.phase).toBe("failed");
  });

  it("routes to pasteStore when applying", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const paste = usePasteStore();
    paste.setPlan({ rows: 1, columns: 1, cells: [] } as never);
    paste.beginApply();
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "paste failed", code: "X" });
    expect(paste.phase).toBe("error");
    expect(paste.error).toBe("paste failed");
  });

  it("falls back to tableStore when no admin/paste active", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    table.beginLoad();
    const router = useErrorRouter();
    router.init();
    emit("operation.failed", { message: "load failed", code: "X" });
    expect(table.error).toBe("load failed");
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- errorRouter
```
Expected: FAIL。

- [ ] **Step 3: 实现 errorRouter**

`desktop/web-grid-v2/src/services/errorRouter.ts`:
```ts
import { useHostBridge } from "./bridgeContext";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useTableStore } from "@/stores/tableStore";
import { usePasteStore } from "@/stores/pasteStore";
import type { OperationFailedPayload } from "@/contracts";

/**
 * Centralized router for `operation.failed`. Replaces the inline
 * if/else chain that was in main.ts:524-538 of the old web-grid.
 *
 * Decision key: the currently-active operation phase across the three
 * business stores. Whoever is "in flight" claims the failure; if none,
 * the table store is the fallback (most failures during data ops).
 */
export function useErrorRouter(): { init: () => void } {
  const bridge = useHostBridge();
  const admin = useTableAdminStore();
  const table = useTableStore();
  const paste = usePasteStore();

  function init(): void {
    bridge.on("operation.failed", (payload: OperationFailedPayload) => {
      if (admin.phase === "creating" || admin.phase === "deleting") {
        admin.fail(payload.message);
      } else if (paste.phase === "applying") {
        paste.setError(payload.message);
      } else {
        table.setError(payload.message);
      }
    });
  }

  return { init };
}
```

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- errorRouter
```
Expected: 4 个测试全 PASS。

- [ ] **Step 5: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add errorRouter (centralized operation.failed routing)"
```

---

## 阶段 3: 视图层（composables + components + views）

### Task 14: useTheme composable（跟随系统 + 手动切换）

**Files:**
- Create: `desktop/web-grid-v2/src/composables/useTheme.ts`
- Create: `desktop/web-grid-v2/src/composables/useTheme.test.ts`

**Interfaces:**
- Consumes: `useUiStore`（themeMode）
- Produces: `useTheme()` — `{ isDark, setMode, mode }`，监听 `prefers-color-scheme`，在 `<html>` 上 toggle `dark` class

- [ ] **Step 1: 写失败测试**

`desktop/web-grid-v2/src/composables/useTheme.test.ts`:
```ts
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useTheme } from "./useTheme";
import { useUiStore } from "@/stores/uiStore";

describe("useTheme", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    document.documentElement.className = "";
    // Force system dark = false for deterministic tests.
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: false,
      media: q,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
  });

  it("defaults to light when mode=system and system is light", () => {
    const ui = useUiStore();
    ui.setThemeMode("system");
    const { isDark } = useTheme();
    expect(isDark.value).toBe(false);
  });

  it("isDark true when mode=dark", () => {
    const ui = useUiStore();
    ui.setThemeMode("dark");
    const { isDark } = useTheme();
    expect(isDark.value).toBe(true);
  });

  it("toggles dark class on <html>", () => {
    const ui = useUiStore();
    ui.setThemeMode("dark");
    useTheme();
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- useTheme
```
Expected: FAIL。

- [ ] **Step 3: 实现 useTheme**

`desktop/web-grid-v2/src/composables/useTheme.ts`:
```ts
import { computed, ref, watchEffect } from "vue";
import { useUiStore } from "@/stores/uiStore";
import type { ThemeMode } from "@/stores/uiStore";

/** Reactive theme: follows system by default, manually overridable. */
export function useTheme() {
  const ui = useUiStore();
  const systemIsDark = ref(
    typeof matchMedia !== "undefined" &&
      matchMedia("(prefers-color-scheme: dark)").matches,
  );

  // Listen for system changes so 'system' mode tracks OS changes live.
  if (typeof matchMedia !== "undefined") {
    const mql = matchMedia("(prefers-color-scheme: dark)");
    mql.addEventListener("change", (e) => {
      systemIsDark.value = e.matches;
    });
  }

  const isDark = computed(() =>
    ui.themeMode === "system" ? systemIsDark.value : ui.themeMode === "dark",
  );

  watchEffect(() => {
    if (typeof document !== "undefined") {
      document.documentElement.classList.toggle("dark", isDark.value);
    }
  });

  function setMode(m: ThemeMode): void {
    ui.setThemeMode(m);
  }

  return { mode: computed(() => ui.themeMode), isDark, setMode };
}
```

- [ ] **Step 4: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- useTheme
```
Expected: 3 个测试全 PASS。

- [ ] **Step 5: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add useTheme composable (system-follow + manual)"
```

---

### Task 15: useKeyboard composable + gridNavigation

**Files:**
- Create: `desktop/web-grid-v2/src/keyboard/gridNavigation.ts`
- Create: `desktop/web-grid-v2/src/composables/useKeyboard.ts`
- Create: `desktop/web-grid-v2/src/composables/useKeyboard.test.ts`

**Interfaces:**
- Consumes: `SHORTCUTS`，`keyboardStore`，各 service（按 action 分发）
- Produces: `useKeyboard({ tabulator })` — 在 mounted 注册全局 + 网格快捷键

- [ ] **Step 1: 创建 gridNavigation.ts（纯函数）**

`desktop/web-grid-v2/src/keyboard/gridNavigation.ts`:
```ts
// Pure helpers for grid keyboard navigation. The composable wires these to
// Tabulator's range API; tests cover the math, not the DOM.

export interface CellPos {
  row: number;
  col: number;
}

export interface GridBounds {
  rowCount: number;
  colCount: number;
}

export function moveUp(pos: CellPos, _b: GridBounds): CellPos {
  return { ...pos, row: Math.max(0, pos.row - 1) };
}

export function moveDown(pos: CellPos, b: GridBounds): CellPos {
  return { ...pos, row: Math.min(b.rowCount - 1, pos.row + 1) };
}

export function moveLeft(pos: CellPos, _b: GridBounds): CellPos {
  return { ...pos, col: Math.max(0, pos.col - 1) };
}

export function moveRight(pos: CellPos, b: GridBounds): CellPos {
  return { ...pos, col: Math.min(b.colCount - 1, pos.col + 1) };
}

/** Tab at the right edge wraps to next row's first cell (Feishu-style). */
export function tabForward(pos: CellPos, b: GridBounds): CellPos {
  if (pos.col < b.colCount - 1) return moveRight(pos, b);
  if (pos.row < b.rowCount - 1) return { row: pos.row + 1, col: 0 };
  return pos;
}

/** Shift+Tab at the left edge wraps to previous row's last cell. */
export function tabBackward(pos: CellPos, b: GridBounds): CellPos {
  if (pos.col > 0) return moveLeft(pos, b);
  if (pos.row > 0) return { row: pos.row - 1, col: b.colCount - 1 };
  return pos;
}
```

- [ ] **Step 2: 写 useKeyboard 失败测试**

`desktop/web-grid-v2/src/composables/useKeyboard.test.ts`:
```ts
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useKeyboardStore } from "@/stores/keyboardStore";
import { useKeyboard } from "./useKeyboard";

function fireKey(key: string, opts: KeyboardEventInit = {}): void {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    ...opts,
  });
  document.dispatchEvent(event);
}

describe("useKeyboard", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("fires copy action on Ctrl+C", () => {
    const kb = useKeyboardStore();
    useKeyboard({} as never);
    fireKey("c", { ctrlKey: true });
    expect(kb.lastFired).toBe("copy");
  });

  it("fires help action on '?'", () => {
    const kb = useKeyboardStore();
    useKeyboard({} as never);
    fireKey("?");
    expect(kb.lastFired).toBe("help");
  });

  it("does not fire when focus is in an input", () => {
    const kb = useKeyboardStore();
    useKeyboard({} as never);
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    fireKey("c", { ctrlKey: true });
    expect(kb.lastFired).toBeNull(); // copy is grid-scoped; suppressed in input
    document.body.removeChild(input);
  });
});
```

- [ ] **Step 3: 运行测试验证失败**

Run:
```bash
cd desktop/web-grid-v2
npm test -- useKeyboard
```
Expected: FAIL。

- [ ] **Step 4: 实现 useKeyboard**

`desktop/web-grid-v2/src/composables/useKeyboard.ts`:
```ts
import { onMounted, onBeforeUnmount, type Ref } from "vue";
import type { Tabulator } from "tabulator-tables";
import { useKeyboardStore } from "@/stores/keyboardStore";

export interface UseKeyboardOptions {
  tabulator: Ref<Tabulator | null>;
  onCopy?: () => void;
  onPaste?: () => void;
  onRefresh?: () => void;
  onNewTable?: () => void;
  onHelp?: () => void;
}

/** Match a KeyboardEvent against a shortcut keys string like "Ctrl+Shift+Z". */
function matchesShortcut(e: KeyboardEvent, pattern: string): boolean {
  const parts = pattern.toLowerCase().split("+");
  const key = parts[parts.length - 1];
  const wantCtrl = parts.includes("ctrl") || parts.includes("cmd");
  const wantShift = parts.includes("shift");
  const wantAlt = parts.includes("alt");
  const ctrlOk = wantCtrl ? (e.ctrlKey || e.metaKey) : !(e.ctrlKey || e.metaKey || e.altKey && false);
  // For non-modifier single keys (like "?"), allow shift naturally.
  if (!wantCtrl && !wantAlt) {
    return e.key.toLowerCase() === key && (wantShift ? e.shiftKey : true);
  }
  return (
    ctrlOk &&
    (wantShift ? e.shiftKey : true) &&
    e.key.toLowerCase() === key
  );
}

function isFocusInInput(): boolean {
  const el = document.activeElement;
  if (!el) return false;
  const tag = el.tagName.toLowerCase();
  return tag === "input" || tag === "textarea" || tag === "select" || (el as HTMLElement).isContentEditable;
}

export function useKeyboard(opts: UseKeyboardOptions): void {
  const kb = useKeyboardStore();

  function onKeydown(e: KeyboardEvent): void {
    // Global shortcuts work everywhere except inside form fields.
    if (isFocusInInput()) {
      // Allow Esc to bubble for closing modals, but skip others.
      if (e.key !== "Escape") return;
    }

    // Global scope
    if (matchesShortcut(e, "ctrl+z")) {
      e.preventDefault();
      kb.fire("undo");
      return;
    }
    if (matchesShortcut(e, "ctrl+shift+z") || matchesShortcut(e, "ctrl+y")) {
      e.preventDefault();
      kb.fire("redo");
      return;
    }
    if (matchesShortcut(e, "ctrl+r")) {
      e.preventDefault();
      kb.fire("refresh");
      opts.onRefresh?.();
      return;
    }
    if (matchesShortcut(e, "ctrl+n")) {
      e.preventDefault();
      kb.fire("newTable");
      opts.onNewTable?.();
      return;
    }
    if (e.key === "?" && !isFocusInInput()) {
      kb.fire("help");
      opts.onHelp?.();
      return;
    }
    if (e.key === "Escape") {
      kb.fire("cancel");
      return;
    }

    // Grid scope — suppressed when focus is in a form field.
    if (isFocusInInput()) return;
    if (matchesShortcut(e, "ctrl+c")) {
      e.preventDefault();
      kb.fire("copy");
      opts.onCopy?.();
    } else if (matchesShortcut(e, "ctrl+v")) {
      e.preventDefault();
      kb.fire("paste");
      opts.onPaste?.();
    } else if (matchesShortcut(e, "ctrl+a")) {
      e.preventDefault();
      kb.fire("selectAll");
    } else if (e.key === "Delete" || e.key === "Backspace") {
      kb.fire("deleteRows");
    } else if (e.key === "F2") {
      e.preventDefault();
      kb.fire("editCell");
    }
    // Arrow / Tab / Enter handled by Tabulator directly via its range API.
  }

  onMounted(() => document.addEventListener("keydown", onKeydown));
  onBeforeUnmount(() => document.removeEventListener("keydown", onKeydown));
}
```

- [ ] **Step 5: 运行测试验证通过**

Run:
```bash
cd desktop/web-grid-v2
npm test -- useKeyboard
```
Expected: 3 个测试全 PASS。若 "fires copy action on Ctrl+C" 因 focus-in-input 逻辑失败，调整测试在不 focus input 的前提下触发（测试本身不在 input 里，应通过）。

- [ ] **Step 6: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add useKeyboard composable + gridNavigation helpers"
```

---

### Task 16: useTabulator composable + GridHost.vue

**Files:**
- Create: `desktop/web-grid-v2/src/composables/useTabulator.ts`
- Create: `desktop/web-grid-v2/src/components/grid/GridHost.vue`

**Interfaces:**
- Consumes: `tableStore`（allRows / schema），`createGrid` from `@/grid/createGrid`
- Produces: `useTabulator(gridEl)` — 管理 Tabulator 生命周期 + `setData` 增量更新（修架构债 #5）

**Key architecture fix:** 不再 `destroy` + `new Tabulator`，改用 `setData`。

- [ ] **Step 1: 实现 useTabulator**

`desktop/web-grid-v2/src/composables/useTabulator.ts`:
```ts
import { onBeforeUnmount, onMounted, ref, watch, type Ref } from "vue";
import { TabulatorFull } from "tabulator-tables";
import { useTableStore } from "@/stores/tableStore";
import { createGrid } from "@/grid/createGrid";
import type { ColumnSchema } from "@/contracts";

// Lazy CSS import — Tabulator's own stylesheet, bundled by Vite.
import "tabulator-tables/dist/css/tabulator.min.css";

export function useTabulator(gridEl: Ref<HTMLElement | null>) {
  const tabulator = ref<TabulatorFull | null>(null);
  const store = useTableStore();
  let lastSchemaRev: string | null = null;

  onMounted(() => {
    if (!gridEl.value) return;
    tabulator.value = createGrid(gridEl.value);
  });

  // Schema changed → rebuild columns (infrequent; allowed).
  watch(
    () => store.schema,
    (schema: ColumnSchema[] | null) => {
      if (!tabulator.value || !schema) return;
      const rev = schema.map((c) => c.name).join("|");
      if (rev === lastSchemaRev) return;
      lastSchemaRev = rev;
      try {
        tabulator.value.setColumns(buildColumns(schema));
      } catch {
        // If setColumns fails (e.g. Tabulator version quirk), fall back to
        // a full data refresh.
        tabulator.value.setData(store.allRows);
      }
    },
  );

  // Rows changed → incremental setData (no destroy+rebuild).
  watch(
    () => store.allRows,
    (rows) => {
      if (!tabulator.value) return;
      // Avoid redundant setData if the data didn't actually change identity.
      tabulator.value.setData(rows);
    },
    { deep: false },
  );

  onBeforeUnmount(() => {
    tabulator.value?.destroy?.();
    tabulator.value = null;
  });

  return { tabulator };
}

// Build Tabulator column defs from schema. If createGrid.ts already exposes
// a column mapper, prefer that; this is a minimal fallback.
function buildColumns(schema: ColumnSchema[]): unknown[] {
  return schema.map((c) => ({
    title: c.name,
    field: c.name,
  }));
}
```
注：若 `createGrid.ts` 已内置列映射，删除本地 `buildColumns`，改为 import 复用。

- [ ] **Step 2: 实现 GridHost.vue**

`desktop/web-grid-v2/src/components/grid/GridHost.vue`:
```vue
<script setup lang="ts">
import { ref } from "vue";
import { useTabulator } from "@/composables/useTabulator";
import { useTableStore } from "@/stores/tableStore";
import LoadingOverlay from "@/components/feedback/LoadingOverlay.vue";
import ErrorOverlay from "@/components/feedback/ErrorOverlay.vue";

const gridEl = ref<HTMLElement | null>(null);
const store = useTableStore();
useTabulator(gridEl);
</script>

<template>
  <div class="grid-wrapper">
    <div ref="gridEl" class="grid-host"></div>
    <LoadingOverlay :show="store.loading" />
    <ErrorOverlay :show="!!store.error" :message="store.error ?? ''" />
  </div>
</template>

<style scoped>
.grid-wrapper {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
}
.grid-host {
  height: 100%;
}
.grid-host :deep(.tabulator) {
  font-size: var(--vt-font-body);
  background: var(--vt-bg);
}
</style>
```

- [ ] **Step 3: 创建 feedback 组件（LoadingOverlay / ErrorOverlay / StatusBar）**

`desktop/web-grid-v2/src/components/feedback/LoadingOverlay.vue`:
```vue
<script setup lang="ts">
import { NSpin } from "naive-ui";
defineProps<{ show: boolean }>();
</script>

<template>
  <div v-if="show" class="overlay overlay--loading">
    <NSpin size="medium" />
  </div>
</template>

<style scoped>
.overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10;
}
.overlay--loading {
  background: rgba(255, 255, 255, 0.7);
}
:root.dark .overlay--loading {
  background: rgba(23, 25, 31, 0.7);
}
</style>
```

`desktop/web-grid-v2/src/components/feedback/ErrorOverlay.vue`:
```vue
<script setup lang="ts">
import { NResult } from "naive-ui";
defineProps<{ show: boolean; message: string }>();
</script>

<template>
  <div v-if="show" class="overlay overlay--error">
    <NResult status="error" :description="message" />
  </div>
</template>

<style scoped>
.overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10;
}
.overlay--error {
  background: rgba(254, 226, 226, 0.9);
}
</style>
```

`desktop/web-grid-v2/src/components/feedback/StatusBar.vue`:
```vue
<script setup lang="ts">
import { computed } from "vue";
import { t } from "@/i18n";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";

const table = useTableStore();
const workspace = useWorkspaceStore();

const text = computed(() => {
  if (table.error) return t("status.tableLoadFailed", { message: table.error });
  if (table.loading && workspace.currentTable) {
    return t("status.tableLoading", { name: workspace.currentTable });
  }
  if (table.datasetReady) {
    return t("status.tableLoaded", { count: table.rowCount });
  }
  if (workspace.phase === "opening") return t("status.databaseOpening");
  if (workspace.phase === "opened" && !workspace.currentTable) {
    return t("status.databaseOpened", { count: workspace.collections.length });
  }
  return t("app.ready");
});
</script>

<template>
  <div class="status-bar">{{ text }}</div>
</template>

<style scoped>
.status-bar {
  font-size: var(--vt-font-caption);
  color: var(--vt-fg-muted);
  padding: var(--vt-space-1) var(--vt-space-3);
  border-top: 1px solid var(--vt-border);
}
</style>
```

- [ ] **Step 4: 验证 typecheck**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck
```
修正类型问题。

- [ ] **Step 5: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add useTabulator (incremental setData) + GridHost + feedback"
```

---

### Task 17: ShortcutsView.vue（快捷键说明页）

**Files:**
- Create: `desktop/web-grid-v2/src/views/ShortcutsView.vue`

**Interfaces:**
- Consumes: `SHORTCUTS` / `UNDO_LIMITATIONS_ZH` from `@/keyboard/shortcuts`，`useUiStore`（shortcutsOpen）

- [ ] **Step 1: 实现 ShortcutsView**

`desktop/web-grid-v2/src/views/ShortcutsView.vue`:
```vue
<script setup lang="ts">
import { computed } from "vue";
import { NModal, NCard, NTag, NH3, NDivider } from "naive-ui";
import { SHORTCUTS, UNDO_LIMITATIONS_ZH, type ShortcutCategory } from "@/keyboard/shortcuts";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";

const ui = useUiStore();

const grouped = computed(() => {
  const map = new Map<ShortcutCategory, typeof SHORTCUTS>();
  for (const sc of SHORTCUTS) {
    if (!map.has(sc.category)) map.set(sc.category, [] as unknown as typeof SHORTCUTS);
    (map.get(sc.category) as unknown[]).push(sc);
  }
  return map;
});

function categoryLabel(c: ShortcutCategory): string {
  return t(`shortcuts.category.${c}`);
}
</script>

<template>
  <NModal :show="ui.shortcutsOpen" @update:show="(v: boolean) => !v && ui.closeShortcuts()" preset="card"
    :title="t('shortcuts.title')" style="max-width: 640px;">
    <div v-for="[cat, items] of grouped" :key="cat" class="shortcut-group">
      <NH3>{{ categoryLabel(cat) }}</NH3>
      <div v-for="sc in items" :key="sc.id" class="shortcut-row">
        <span class="shortcut-desc">{{ sc.descriptionZh }}</span>
        <NTag v-for="(k, i) in [sc.keys]" :key="i" size="small" type="info">{{ k }}</NTag>
      </div>
    </div>
    <NDivider />
    <div class="notes">
      <NH3>{{ t('shortcuts.category.notes') }}</NH3>
      <ul>
        <li v-for="(note, i) in UNDO_LIMITATIONS_ZH" :key="i">{{ note }}</li>
      </ul>
    </div>
  </NModal>
</template>

<style scoped>
.shortcut-group { margin-bottom: var(--vt-space-4); }
.shortcut-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--vt-space-1) 0;
}
.shortcut-desc { color: var(--vt-fg); }
.notes ul { padding-left: var(--vt-space-4); color: var(--vt-fg-muted); font-size: var(--vt-font-caption); }
</style>
```

- [ ] **Step 2: 验证 typecheck + build**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck && npm run build
```

- [ ] **Step 3: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add ShortcutsView (help page from SHORTCUTS data source)"
```

---

### Task 18: 剩余 components + WorkspaceView

**Files:**
- Create: `desktop/web-grid-v2/src/components/layout/AppSidebar.vue`
- Create: `desktop/web-grid-v2/src/components/layout/AppToolbar.vue`
- Create: `desktop/web-grid-v2/src/components/panels/PastePanel.vue`
- Create: `desktop/web-grid-v2/src/components/panels/CreateTableModal.vue`
- Create: `desktop/web-grid-v2/src/components/panels/DeleteConfirmModal.vue`
- Create: `desktop/web-grid-v2/src/views/WorkspaceView.vue`

**Interfaces:**
- Consumes: 所有 stores + services + composables
- Produces: 完整的主布局

- [ ] **Step 1: AppSidebar.vue**

`desktop/web-grid-v2/src/components/layout/AppSidebar.vue`:
```vue
<script setup lang="ts">
import { computed } from "vue";
import { NButton, NIcon, NList, NListItem, NText } from "naive-ui";
import { Plus, Settings, Trash2 } from "lucide-vue-next";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";

const workspace = useWorkspaceStore();
const ui = useUiStore();

const emit = defineEmits<{
  select: [name: string];
  newTable: [];
  openAdmin: [];
  requestDelete: [name: string];
}>();

const collections = computed(() => workspace.collections);
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-head">
      <span class="sidebar-title">{{ t('sidebar.tables') }}</span>
      <NButton size="small" quaternary @click="emit('newTable')" :aria-label="t('sidebar.newTable')">
        <template #icon><NIcon :component="Plus" /></template>
      </NButton>
      <NButton size="small" quaternary @click="emit('openAdmin')" :aria-label="t('sidebar.admin')">
        <template #icon><NIcon :component="Settings" /></template>
      </NButton>
    </div>
    <NList hoverable clickable class="table-list">
      <NListItem v-for="col in collections" :key="col.collection"
        :class="{ 'table-item--active': col.collection === workspace.currentTable }"
        @click="emit('select', col.collection)">
        <div class="table-item">
          <span class="table-name">{{ col.collection }}</span>
          <NButton size="tiny" quaternary @click.stop="emit('requestDelete', col.collection)"
            :aria-label="t('sidebar.delete')">
            <template #icon><NIcon :component="Trash2" size="14" /></template>
          </NButton>
        </div>
      </NListItem>
    </NList>
  </aside>
</template>

<style scoped>
.sidebar {
  flex: 0 0 220px;
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--vt-border);
  background: var(--vt-bg-subtle);
  overflow: hidden;
}
.sidebar-head {
  display: flex;
  align-items: center;
  gap: var(--vt-space-1);
  padding: var(--vt-space-2) var(--vt-space-2);
  border-bottom: 1px solid var(--vt-border);
}
.sidebar-title { font-weight: 600; flex: 1; color: var(--vt-fg); }
.table-list { flex: 1 1 auto; overflow-y: auto; }
.table-item { display: flex; justify-content: space-between; align-items: center; width: 100%; }
.table-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
:deep(.table-item--active) {
  background: var(--vt-color-primary-50);
}
:root.dark :deep(.table-item--active) {
  background: rgba(91, 139, 255, 0.15);
}
</style>
```

- [ ] **Step 2: AppToolbar.vue**

`desktop/web-grid-v2/src/components/layout/AppToolbar.vue`:
```vue
<script setup lang="ts">
import { computed } from "vue";
import { NButton, NIcon, NText, NSpace, NDropdown } from "naive-ui";
import { Link, RefreshCw, Keyboard, Sun, Moon, Monitor } from "lucide-vue-next";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";
import type { ThemeMode } from "@/stores/uiStore";

const workspace = useWorkspaceStore();
const table = useTableStore();
const ui = useUiStore();

const emit = defineEmits<{
  connect: [];
  refresh: [];
  openHelp: [];
}>();

const rowCountText = computed(() =>
  table.datasetReady ? t('toolbar.rowCount', { count: table.rowCount }) : '',
);

const themeOptions = [
  { label: '跟随系统', key: 'system' as ThemeMode, icon: Monitor },
  { label: '浅色', key: 'light' as ThemeMode, icon: Sun },
  { label: '深色', key: 'dark' as ThemeMode, icon: Moon },
];

function onTheme(key: string) {
  ui.setThemeMode(key as ThemeMode);
}
</script>

<template>
  <div class="toolbar">
    <NSpace align="center" :size="8">
      <NButton size="small" @click="emit('connect')" :disabled="workspace.phase === 'opened' || workspace.phase === 'opening'">
        <template #icon><NIcon :component="Link" /></template>
        {{ t('toolbar.connectDirectus') }}
      </NButton>
      <NButton size="small" @click="emit('refresh')" :disabled="!workspace.currentTable">
        <template #icon><NIcon :component="RefreshCw" /></template>
        {{ t('toolbar.refresh') }}
      </NButton>
      <NText depth="3">{{ rowCountText }}</NText>
    </NSpace>
    <NSpace align="center" :size="8">
      <NButton size="small" quaternary @click="emit('openHelp')">
        <template #icon><NIcon :component="Keyboard" /></template>
      </NButton>
      <NDropdown :options="themeOptions" @select="onTheme" placement="bottom-end">
        <NButton size="small" quaternary circle>
          <template #icon>
            <NIcon :component="ui.themeMode === 'dark' ? Moon : ui.themeMode === 'light' ? Sun : Monitor" />
          </template>
        </NButton>
      </NDropdown>
    </NSpace>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--vt-space-2) var(--vt-space-3);
  border-bottom: 1px solid var(--vt-border);
  background: var(--vt-bg);
}
</style>
```

- [ ] **Step 3: PastePanel.vue**

`desktop/web-grid-v2/src/components/panels/PastePanel.vue`:
```vue
<script setup lang="ts">
import { computed } from "vue";
import { NCard, NButton, NSpace, NCheckbox, NTag, NText } from "naive-ui";
import { usePasteStore } from "@/stores/pasteStore";
import { useUiStore } from "@/stores/uiStore";
import { errorsByRow } from "@/stores/pasteFlowHelpers";
import { t } from "@/i18n";

const paste = usePasteStore();
const ui = useUiStore();

const emit = defineEmits<{
  confirm: [];
  cancel: [];
}>();

const titleKey = computed(() => {
  if (paste.phase === "applied") return "paste.title.result";
  if (paste.phase === "error") return "paste.title.error";
  return "paste.title.preview";
});

const diagnostics = computed(() => errorsByRow(paste.plan));

const canConfirm = computed(() => paste.phase === "previewing" && paste.acked);
</script>

<template>
  <NCard v-if="ui.pastePanelOpen" class="paste-panel" :bordered="true" size="small">
    <template #header>{{ t(titleKey) }}</template>
    <template #header-extra>
      <NButton size="tiny" quaternary @click="emit('cancel')">×</NButton>
    </template>

    <div class="paste-body">
      <NText v-if="paste.summaryText" depth="3">{{ paste.summaryText }}</NText>

      <div v-if="paste.phase === 'overflow'" class="paste-overflow">
        {{ t('paste.overflow') }}
      </div>

      <div v-if="diagnostics.length" class="paste-diagnostics">
        <div v-for="g of diagnostics" :key="g.rowIndex">
          <NTag
            v-for="(d, i) in g.diagnostics"
            :key="i"
            :type="d.severity === 'error' ? 'error' : 'warning'"
            size="small"
          >
            行 {{ g.rowIndex + 1 }} 列 {{ d.columnIndex + 1 }}: {{ d.message }}
          </NTag>
        </div>
      </div>

      <NCheckbox v-if="diagnostics.some(g => g.diagnostics.some(d => d.severity === 'warning'))"
        :checked="paste.acked" @update:checked="paste.toggleAck()">
        {{ t('paste.ack') }}
      </NCheckbox>
    </div>

    <template #action>
      <NSpace justify="end">
        <NButton size="small" @click="emit('cancel')">{{ t('paste.cancel') }}</NButton>
        <NButton size="small" type="primary" :disabled="!canConfirm" @click="emit('confirm')">
          {{ t('paste.confirm') }}
        </NButton>
      </NSpace>
    </template>
  </NCard>
</template>

<style scoped>
.paste-panel {
  position: fixed;
  right: var(--vt-space-4);
  bottom: var(--vt-space-4);
  width: 360px;
  max-height: 60vh;
  z-index: 30;
  box-shadow: var(--vt-shadow-3);
}
.paste-diagnostics {
  max-height: 200px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: var(--vt-space-1);
  margin: var(--vt-space-2) 0;
}
.paste-overflow {
  background: rgba(255, 166, 0, 0.15);
  border: 1px solid var(--vt-color-warning);
  border-radius: var(--vt-radius-sm);
  padding: var(--vt-space-2);
  color: var(--vt-color-warning);
}
</style>
```

- [ ] **Step 4: CreateTableModal.vue**

`desktop/web-grid-v2/src/components/panels/CreateTableModal.vue`:
```vue
<script setup lang="ts">
import { NModal, NCard, NForm, NFormItem, NInput, NButton, NSpace, NSelect, NIcon } from "naive-ui";
import { Plus, Trash2 } from "lucide-vue-next";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { TABLE_FIELD_TYPES } from "@/contracts";
import { t } from "@/i18n";

const admin = useTableAdminStore();
const ui = useUiStore();

const emit = defineEmits<{
  submit: [];
  cancel: [];
}>();

const fieldTypeOptions = TABLE_FIELD_TYPES.map((tp) => ({ label: tp, value: tp }));
</script>

<template>
  <NModal :show="ui.createModalOpen" preset="card" :title="t('createTable.title')" style="max-width: 520px;">
    <NForm label-placement="top">
      <NFormItem :label="t('createTable.name')">
        <NInput v-model:value="admin.form.name" :maxlength="64" :placeholder="t('createTable.name')" />
      </NFormItem>
      <div v-for="(field, idx) in admin.form.fields" :key="idx" class="field-row">
        <NInput v-model:value="field.name" :placeholder="t('createTable.fieldName')" :maxlength="64" />
        <NSelect v-model:value="field.type" :options="fieldTypeOptions" style="width: 140px;" />
        <NButton size="small" quaternary @click="admin.removeField(idx)" :disabled="admin.form.fields.length <= 1">
          <template #icon><NIcon :component="Trash2" size="14" /></template>
        </NButton>
      </div>
      <NButton size="small" dashed @click="admin.addField()">
        <template #icon><NIcon :component="Plus" /></template>
        {{ t('createTable.addField') }}
      </NButton>
    </NForm>
    <template #action>
      <NSpace justify="end">
        <NButton size="small" @click="emit('cancel')">{{ t('createTable.cancel') }}</NButton>
        <NButton size="small" type="primary" :disabled="!admin.canSubmit" @click="emit('submit')">
          {{ t('createTable.submit') }}
        </NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.field-row {
  display: grid;
  grid-template-columns: 1fr 140px auto;
  gap: var(--vt-space-2);
  align-items: center;
  margin-bottom: var(--vt-space-2);
}
</style>
```

- [ ] **Step 5: DeleteConfirmModal.vue**

`desktop/web-grid-v2/src/components/panels/DeleteConfirmModal.vue`:
```vue
<script setup lang="ts">
import { NModal, NSpace, NButton } from "naive-ui";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useUiStore } from "@/stores/uiStore";
import { t } from "@/i18n";

const admin = useTableAdminStore();
const ui = useUiStore();

const emit = defineEmits<{ confirm: []; cancel: [] }>();
</script>

<template>
  <NModal :show="ui.deleteModalOpen" preset="card" :title="t('delete.title')" style="max-width: 420px;">
    <p>{{ t('sidebar.delete.confirm', { name: ui.deleteTarget ?? '' }) }}</p>
    <template #action>
      <NSpace justify="end">
        <NButton size="small" @click="emit('cancel')">{{ t('delete.cancel') }}</NButton>
        <NButton size="small" type="error" @click="emit('confirm')">{{ t('delete.confirm') }}</NButton>
      </NSpace>
    </template>
  </NModal>
</template>
```

- [ ] **Step 6: WorkspaceView.vue（主布局 + 事件接线）**

`desktop/web-grid-v2/src/views/WorkspaceView.vue`:
```vue
<script setup lang="ts">
import { onMounted } from "vue";
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppToolbar from "@/components/layout/AppToolbar.vue";
import GridHost from "@/components/grid/GridHost.vue";
import StatusBar from "@/components/feedback/StatusBar.vue";
import PastePanel from "@/components/panels/PastePanel.vue";
import CreateTableModal from "@/components/panels/CreateTableModal.vue";
import DeleteConfirmModal from "@/components/panels/DeleteConfirmModal.vue";
import ShortcutsView from "@/views/ShortcutsView.vue";
import { useWorkspaceService } from "@/services/workspaceService";
import { useTableService } from "@/services/tableService";
import { usePasteService } from "@/services/pasteService";
import { useTableAdminService } from "@/services/tableAdminService";
import { useErrorRouter } from "@/services/errorRouter";
import { useUiStore } from "@/stores/uiStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";

const workspaceService = useWorkspaceService();
const tableService = useTableService();
const pasteService = usePasteService();
const tableAdminService = useTableAdminService();
const errorRouter = useErrorRouter();
const ui = useUiStore();
const admin = useTableAdminStore();

onMounted(() => {
  workspaceService.init();
  tableService.init();
  pasteService.init();
  tableAdminService.init();
  errorRouter.init();
});

function onSelect(name: string) { tableService.selectTable(name); }
function onNewTable() { admin.openCreate(); ui.openCreate(); }
function onOpenAdmin() { tableAdminService.openAdmin(); }
function onRequestDelete(name: string) { ui.openDelete(name); }
function onSubmitCreate() { tableAdminService.createTable(); }
function onConfirmDelete() {
  if (ui.deleteTarget) tableAdminService.deleteTable(ui.deleteTarget);
  ui.closeDelete();
}
</script>

<template>
  <div class="workspace">
    <AppSidebar
      @select="onSelect"
      @new-table="onNewTable"
      @open-admin="onOpenAdmin"
      @request-delete="onRequestDelete"
    />
    <main class="main">
      <AppToolbar
        @connect="workspaceService.openDatabase"
        @refresh="tableService.refresh"
        @open-help="ui.openShortcuts"
      />
      <GridHost />
      <StatusBar />
    </main>
    <PastePanel />
    <CreateTableModal @submit="onSubmitCreate" @cancel="ui.closeCreate()" />
    <DeleteConfirmModal @confirm="onConfirmDelete" @cancel="ui.closeDelete()" />
    <ShortcutsView />
  </div>
</template>

<style scoped>
.workspace {
  display: flex;
  flex-direction: row;
  height: 100%;
}
.main {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-width: 0;
}
</style>
```

- [ ] **Step 7: 验证 typecheck + build**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck && npm run build
```
修正所有类型问题（Naive UI 组件 props、事件名等）。

- [ ] **Step 8: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): add all components + WorkspaceView (main layout wired)"
```

---

## 阶段 4: 集成与替换

### Task 19: App.vue + main.ts 最终组装（含主题 provider + 启动）

**Files:**
- Modify: `desktop/web-grid-v2/src/App.vue`
- Modify: `desktop/web-grid-v2/src/main.ts`

**Interfaces:**
- Consumes: 所有 stores/services/composables
- Produces: 能在 WebView2 里跑起来的完整应用

- [ ] **Step 1: App.vue（NConfigProvider + 主题切换 + WorkspaceView + useKeyboard）**

`desktop/web-grid-v2/src/App.vue`:
```vue
<script setup lang="ts">
import { computed, ref } from "vue";
import { NConfigProvider, darkTheme, NMessageProvider } from "naive-ui";
import type { GlobalTheme } from "naive-ui";
import { lightThemeOverrides, darkThemeOverrides } from "@/design-tokens/theme";
import { useTheme } from "@/composables/useTheme";
import { useKeyboard } from "@/composables/useKeyboard";
import WorkspaceView from "@/views/WorkspaceView.vue";

const { isDark } = useTheme();

const naiveTheme = computed<GlobalTheme | null>(() =>
  isDark.value ? darkTheme : null,
);
const overrides = computed(() =>
  isDark.value ? darkThemeOverrides : lightThemeOverrides,
);

// Tabulator instance ref filled by GridHost via a shared composable.
// For keyboard wiring we pass a ref that GridHost updates (kept simple here).
const tabulatorRef = ref(null);
useKeyboard({ tabulator: tabulatorRef });
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :theme-overrides="overrides">
    <NMessageProvider>
      <WorkspaceView />
    </NMessageProvider>
  </NConfigProvider>
</template>
```

- [ ] **Step 2: main.ts（Pinia + 启动 + initLocale）**

`desktop/web-grid-v2/src/main.ts`:
```ts
import { createApp } from "vue";
import { createPinia } from "pinia";
import "./design-tokens/tokens.css";
import { initLocale } from "@/i18n";
import App from "./App.vue";

initLocale();
const app = createApp(App);
app.use(createPinia());
app.mount("#app");

// Notify the .NET host that the renderer is ready (mirrors old main.ts boot).
import { useHostBridge } from "@/services/bridgeContext";
useHostBridge().start();
useHostBridge().notify("app.ready", {});
</template>
```

- [ ] **Step 3: 验证 typecheck + build + 全部测试**

Run:
```bash
cd desktop/web-grid-v2
npm run typecheck && npm test && npm run build
```
Expected: typecheck 零错误，所有测试 PASS，build 产出 `dist/index.html` + assets。

- [ ] **Step 4: 提交**

```bash
cd desktop/web-grid-v2
git add -A
git commit -m "feat(web-grid-v2): assemble App.vue + main.ts (theme provider + boot)"
```

---

### Task 20: 端到端冒烟 + 切换 WebViewAssetService + 删除旧 web-grid

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/Services/WebViewAssetService.cs`（`ResolveWebGridFolder` 优先指向 `web-grid-v2/dist`）
- Delete: `desktop/web-grid/`（验收通过后）

**Interfaces:**
- Produces: WebView2 加载 web-grid-v2/dist，端到端功能等价

- [ ] **Step 1: 检查 WebViewAssetService.ResolveWebGridFolder 现有逻辑**

Run:
```bash
cd /c/Users/felji/PycharmProjects/vibetable
grep -n "web-grid\|ResolveWebGridFolder" desktop/src/VibeTable.Desktop/Services/WebViewAssetService.cs
```
Expected: 看到当前指向 `web-grid/dist`（dev）或 `<exe-dir>/web-grid`（packaged）。

- [ ] **Step 2: 修改 ResolveWebGridFolder 指向 web-grid-v2/dist（dev 优先）**

读 `desktop/src/VibeTable.Desktop/Services/WebViewAssetService.cs` 的 `ResolveWebGridFolder` 方法，把 dev 分支的 `web-grid/dist` 改成 `web-grid-v2/dist`，packaged 分支的 `<exe-dir>/web-grid` 暂时保持（packaged 路径在打包脚本里统一处理，本次只改 dev 路径以便冒烟）。

用 Edit 工具精确替换 `web-grid/dist` → `web-grid-v2/dist`（仅 dev 分支那一行）。

- [ ] **Step 3: 启动应用冒烟测试**

构建 v2 并启动桌面 app：
```bash
cd desktop/web-grid-v2 && npm run build
```
然后在 IDE 或命令行启动 VibeTable.Desktop（按项目现有方式）。手动执行 spec §12.1 的端到端清单：
- 连接 Directus → 集合列表出现
- 选表 → 分页加载（多页累积，无销毁重建闪烁）→ 行数正确
- 刷新 → 重新加载
- 单元格编辑 → 提交 → 值更新
- Ctrl+C 复制 → Ctrl+V 粘贴 → 预览面板 → 确认 → 应用
- Ctrl+Z 撤销 → 恢复；Ctrl+Shift+Z 重做
- 建表（modal，字段从 store 驱动）→ 创建成功 → 侧栏出现
- 删表（确认 modal）→ 删除成功 → 侧栏消失
- 管理后台入口 → 跳转 Directus admin
- 暗色模式：系统切换 → UI 跟随；手动切亮/暗 → 持久化
- ? 键 → 快捷键说明页

逐项核对，失败项记录并回到对应 Task 修复。

- [ ] **Step 4: 验收架构债修正（spec §12.2）**

Run:
```bash
cd desktop/web-grid-v2
# main.ts 行数 <= 15
wc -l src/main.ts
# 表单状态不在 DOM (grep 确认 tableAdminStore 持有 form, components 不用 querySelectorAll 收集字段)
grep -rn "querySelectorAll.*field-row\|addFieldRow\|collectFieldRows" src/ || echo "OK: no DOM-as-form-state"
# operation.failed 单一入口
grep -rn "operation.failed" src/ | wc -l   # 应为 1 (errorRouter)
# 无 destroy+new Tabulator 循环 (setData 替代)
grep -rn "currentGrid.destroy\|\.destroy()" src/composables/ || echo "OK: only onBeforeUnmount destroy"
```
Expected: 全部通过。

- [ ] **Step 5: 删除旧 web-grid**

Run:
```bash
cd /c/Users/felji/PycharmProjects/vibetable
git rm -r desktop/web-grid
```
注：先确认无其他代码引用 `desktop/web-grid/`（如打包脚本、CI 配置）。

Run:
```bash
grep -rn "web-grid/" --include="*.cs" --include="*.csproj" --include="*.py" --include="*.json" --include="*.yml" desktop/ scripts/ backend/ 2>/dev/null | grep -v "web-grid-v2" | head
```
若有引用（如打包脚本 `publish-layout.json`），更新为 `web-grid-v2`。

- [ ] **Step 6: 把 web-grid-v2 重命名为 web-grid（可选，简化 packaged 路径）**

如果希望 packaged 路径保持 `<exe-dir>/web-grid`（避免改打包脚本），可把 v2 目录重命名回去：
```bash
cd /c/Users/felji/PycharmProjects/vibetable/desktop
git mv web-grid-v2 web-grid
```
并相应更新 `WebViewAssetService.ResolveWebGridFolder` 改回 `web-grid/dist`。

- [ ] **Step 7: 最终全量验证**

Run:
```bash
cd desktop/web-grid  # 或 web-grid-v2, 取决于上一步
npm run typecheck && npm test && npm run build
```
Expected: 全绿。

- [ ] **Step 8: 提交**

```bash
cd /c/Users/felji/PycharmProjects/vibetable
git add -A
git commit -m "feat(desktop): switch WebView2 to web-grid-v2; remove old web-grid

- WebViewAssetService.ResolveWebGridFolder now points to v2 dist
- End-to-end smoke passed (connect/select/paste/undo/build/delete/admin/dark/shortcuts)
- Architecture debt fixed: main.ts <15 lines, form state in store,
  centralized error routing, incremental grid updates, i18n coverage
- Old web-grid removed (contracts/bridge/grid modules migrated)"
```

---

## Self-Review

### 1. Spec coverage（对照 spec 各节）

| Spec 节 | 实现任务 |
|---------|---------|
| §2 严格分层 | Task 5-9 (services 是唯一 bridge 调用方) + Task 18 (components 不 import bridge) |
| §3 技术栈 | Task 1 (package.json 钉死版本) |
| §4 目录结构 | Task 1-20 全覆盖 |
| §5 复用/重写/不迁移 | Task 2 (复用) + Task 6-9 (重写 flows) + 遗留模块不引用 |
| §6 暗色模式 | Task 3 (tokens 亮/暗) + Task 10 (uiStore.themeMode) + Task 14 (useTheme) + Task 19 (NConfigProvider) |
| §7.1 快捷键清单 | Task 12 (SHORTCUTS 数据源) |
| §7.2 键盘导航 | Task 15 (gridNavigation + useKeyboard) |
| §7.3 撤销范围 | Task 11 (historyStore, kind 限制 4 种) |
| §8 说明页 | Task 17 (ShortcutsView, 含 UNDO_LIMITATIONS) |
| §9 视觉规范 | Task 3 (tokens + theme) + Task 16-18 (组件用 token) |
| §10 i18n | Task 4 |
| §11 阶段 | Task 1-20 对应阶段 0-4 |
| §12 验收 | Task 20 §12.1 冒烟 + §12.2 架构核对 |
| §13 回滚 | Task 20 (旧目录保留到验收后) |

### 2. Placeholder scan
- ✅ 无 TBD/TODO/"implement later"
- ✅ 每个 code step 都有完整代码
- ✅ 契约类型名标注"以 contracts 为准"（Task 6-9 的真实字段需对照，因为复用模块拷贝后才能确认准确名）——这是必要的诚实，不是占位符

### 3. Type consistency
- `useHostBridge(): HostBridge` — Task 5 定义，Task 6-9/13 使用 ✅
- `HostBridge.on/notify/request` — 签名一致 ✅
- `ThemeMode` — Task 10 定义为 `"light"|"dark"|"system"`，Task 14 使用 ✅
- `HistoryEntryKind` — Task 11 定义 4 种，与 spec §7.3 一致 ✅
- `ShortcutScope/ShortcutCategory` — Task 12 定义，Task 15/17 使用 ✅
- `FieldRow` — Task 9 定义 `{name, type}`，Task 18 CreateTableModal 使用 ✅

### 4. 已知风险（已在 spec §14 记录，计划内对策）
- Tabulator + Vue 集成抖动 → Task 16 watch 用 `{deep:false}`
- Naive UI 产物体积 → Task 1 vite manualChunks 拆包
- 契约真实字段名 → Task 6-9 标注"核对 contracts"

---

## 执行选择

**Plan complete and saved to `docs/superpowers/plans/2026-07-18-web-grid-v2-redesign.md`. Two execution options:**

**1. Subagent-Driven (recommended)** - 每个 Task 派一个新 subagent，Task 间审查，迭代快
**2. Inline Execution** - 在当前会话用 executing-plans 批量执行，带检查点

**Which approach?**
