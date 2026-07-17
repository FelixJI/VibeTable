# 设计：web-grid v2 前端重做 — Vue 3 + 分层架构 + 飞书风视觉

- **日期**: 2026-07-18
- **状态**: 已批准（设计阶段），待写实施计划
- **范围**: 重写 `desktop/web-grid/` 为 `desktop/web-grid-v2/`，迁移到 Vue 3 + 严格分层架构 + 现代组件库 + 飞书风设计语言，并修正现有架构债。新增暗色模式（跟随系统）、复制/粘贴/撤销快捷键、快捷键说明页、键盘导航。**不动** host bridge 协议、.NET 宿主、Python 后端、Directus。

## 1. 背景与动机

### 1.1 现状

`desktop/web-grid/` 是嵌入 WebView2 的 TypeScript 单页，零框架，唯一运行时依赖是 `tabulator-tables 6.5.2`。

**视觉问题**：CSP `connect-src 'none'` 禁止一切 CDN，`script-src 'self'` 禁止 inline script，加上零组件库、零图标库、零设计 token，所有"图标"用 `×`/`+` 字符，`styles.css` 仅 385 行 5 个 CSS 变量，标注为 "Phase A styles"。结果是功能可用的工程骨架，不是设计成品。

**架构问题**（"不分层级"）：`main.ts` 741 行混了 6 件事——DOM 查询、三个状态机的编排、所有渲染、所有事件订阅、表单状态（直接存在 DOM 里）、中英文混杂的硬编码文案。三个状态机（`tableFlow`/`pasteFlow`/`tableAdminFlow`）无共享根，跨机协调靠 main.ts 内联 glue（如 `operation.failed` 路由散在 `main.ts:524-538`）。`renderGrid` 每收到一页就 `destroy()` + `new Tabulator()`（N 页 = N 次完整重建）。

**好消息**：叶子模块（`hostBridge.ts`/`contracts.ts`/`grid/*`/`query/*`/`state/*`/纯 flow 模块）分层干净，可复用。

### 1.2 动机

参考飞书多维表格（设计标杆，蓝紫主色 `#3370FF`，公开设计规范：清晰/简洁/动效）、NocoDB（开源 Vue 3 + Pinia + Ant Design Vue 技术参考）、WPS 多维表（保守企业风参考）。目标是让 VibeTable 从"工程骨架"升级为"现代精致的多维表工具"，同时把架构债清掉。

### 1.3 明确不做（YAGNI / 范围外）

- **不动 bridge 协议**：`hostBridge.ts` 的 16 个出站白名单 + 13 个入站白名单 + `{type, requestId, payload}` 信封形状完全不变。任何新前端功能必须用现有白名单消息实现；不新增消息类型（避免协调 .NET `WebMessageRouter` + Python）。
- **不动契约**：`contracts.ts`（862 行，与 .NET `VibeTable.Contracts` + Python pydantic 三方镜像）一字不改。
- **不做多视图**：看板/甘特/画廊/日历/表单是下一轮。
- **不处理遗留模块**：`fieldHistoryFlow.ts` 和 contracts.ts 里的 G1/G3 契约（history/documents）本次不接入、不删除，原样保留在旧目录，v2 不引用。
- **不做 E2E 自动化**：项目无 E2E 基建，WebView2 内交互难自动化。手工冒烟作为验收。
- **不引 Tailwind / shadcn**：选 Vue + Naive UI 路线，不需要 Tailwind runtime（产物更小，CSP 更干净）。
- **不引 vue-i18n**：自写 30 行 `t()` 轻量方案。
- **不做服务端撤销**：撤销仅基于前端可见数据 + 现有 mutation 接口反向操作（详见 §7.3 范围限制）。

## 2. 总体架构与不变量

### 2.1 改动边界

新建 `desktop/web-grid-v2/`（绿地）。完全不碰：

- `desktop/src/`（WPF 宿主，仅 `WebViewAssetService.ResolveWebGridFolder()` 在阶段 4 改指 `web-grid-v2/dist`）
- `backend/`（Python BFF）
- `directus/`（Directus 扩展与 capability）
- 旧 `desktop/web-grid/`（阶段 4 验收通过后删除）

### 2.2 严格分层（强制单向数据流）

```
┌─────────────────────────────────────────────────────────┐
│ Views (views/)          — 页面级组合，只读 store         │
├─────────────────────────────────────────────────────────┤
│ Components (components/) — 纯展示 + emit 事件，不碰 store│
│   例外：GridHost 用 composable 直接 watch store（性能） │
├─────────────────────────────────────────────────────────┤
│ Stores (stores/)        — Pinia，唯一状态源             │
├─────────────────────────────────────────────────────────┤
│ Services (services/)    — 业务封装，唯一能调 bridge     │
│   出站：store action → service → bridge.notify/request  │
│   入站：bridge.on → service → store action              │
├─────────────────────────────────────────────────────────┤
│ Bridge (bridge/)        — hostBridge.ts 原样复用        │
├─────────────────────────────────────────────────────────┤
│ Contracts (contracts/)  — contracts.ts 原样复用         │
└─────────────────────────────────────────────────────────┘
   横切层（贯穿所有层）：
   - design-tokens/   飞书风 CSS 变量 + Naive UI themeOverrides
   - i18n/            文案目录（zh-CN 默认，en-US）
   - composables/     Vue 组合式函数
   - keyboard/        快捷键与键盘导航
```

**强制规则**：
1. components 绝不 import bridge 或 services。
2. stores 绝不 import bridge，只能通过 service。
3. services 是 bridge 的唯一调用方。
4. contracts 一字不改（三方镜像约束）。

### 2.3 数据流（以"点侧栏选表"为例）

```
用户点侧栏表名
  ↓ Vue emit
AppSidebar → WorkspaceView 调 tableService.selectTable(name)
  ↓
tableService: workspaceStore.selectTable(name) → tableStore.reset()+beginLoad()
            → bridge.notify('table.selected', {table})
  ↓ [host 分页拉取,每页回推]
bridge.on('table.pageLoaded') → tableService → tableStore.appendPage(page)
  ↓ tableStore.allRows getter 变化
GridHost 的 useTabulator watch 触发 → tabulator.setData(rows)  ← 增量,不销毁
  ↓ 最终 datasetReady
tableStore.setDatasetReady → loading=false → overlay 消失
```

对比现状：N 页 = N 次 `setData`（增量 diff），而非 N 次 `destroy + new Tabulator`。

## 3. 技术栈

| 类别 | 选择 | 版本约束 |
|------|------|---------|
| 框架 | Vue 3（Composition API + `<script setup>`） | ^3.5 |
| 构建 | Vite + `@vitejs/plugin-vue` | ^8（与现有一致） |
| 语言 | TypeScript | ^6（与现有一致） |
| 状态 | Pinia | ^2.3 |
| 组件库 | Naive UI | ^2（按需引入，tree-shake） |
| 图标 | `lucide-vue-next` | ^0.300.0（线性 1.5px 描边，飞书风） |
| 表格 | `tabulator-tables` | 6.5.2（保留，不升级） |
| 测试 | Vitest + jsdom（与现有一致） | ^4 |
| i18n | 自写（不引 vue-i18n） | — |
| Node | >=24 <25（`.nvmrc` 约束） | — |

**CSP 合规**：所有依赖经 Vite 打包内联到 `dist/`，无 CDN、无 inline script、无运行时网络请求。Naive UI 的按需引入保证产物体积可控。`style-src 'unsafe-inline'` 保留（Tabulator 与 Naive UI 运行时样式需要）。

## 4. 目录结构

```
desktop/web-grid-v2/
├── index.html                    # Vue 挂载点 + 原 CSP（不变）
├── package.json
├── vite.config.ts                # @vitejs/plugin-vue, build.outDir='dist'
├── tsconfig.json
└── src/
    ├── main.ts                   # ≤15 行：createApp + pinia + naive + 挂载 + 启动 services
    ├── App.vue                   # 根组件 + NConfigProvider（主题/暗色） + NMessageProvider
    ├── contracts/                # ← 复用：原 contracts.ts 原样拷贝
    │   └── index.ts
    ├── bridge/                   # ← 复用：原 hostBridge.ts 原样拷贝
    │   └── hostBridge.ts
    ├── services/
    │   ├── workspaceService.ts
    │   ├── tableService.ts
    │   ├── pasteService.ts
    │   ├── tableAdminService.ts
    │   ├── errorRouter.ts        # operation.failed 集中路由（修架构债 #4）
    │   └── tableAdminValidation.ts  # ← 复用：校验纯函数
    ├── stores/
    │   ├── workspaceStore.ts
    │   ├── tableStore.ts
    │   ├── pasteStore.ts
    │   ├── tableAdminStore.ts    # 含表单状态（修架构债 #3）
    │   ├── uiStore.ts            # modal/panel 开关 + 主题模式
    │   ├── historyStore.ts       # 撤销栈（见 §7）
    │   └── keyboardStore.ts      # 快捷键说明页数据源
    ├── views/
    │   ├── WorkspaceView.vue     # 主布局
    │   └── ShortcutsView.vue     # 快捷键说明页（见 §8）
    ├── components/
    │   ├── layout/
    │   │   ├── AppSidebar.vue
    │   │   └── AppToolbar.vue
    │   ├── grid/
    │   │   └── GridHost.vue
    │   ├── panels/
    │   │   ├── PastePanel.vue
    │   │   ├── CreateTableModal.vue
    │   │   └── DeleteConfirmModal.vue
    │   └── feedback/
    │       ├── StatusBar.vue
    │       ├── LoadingOverlay.vue
    │       └── ErrorOverlay.vue
    ├── composables/
    │   ├── useTabulator.ts       # Tabulator 生命周期 + setData 增量更新
    │   ├── useTheme.ts           # 系统明暗跟随（见 §6）
    │   └── useKeyboard.ts        # 快捷键注册与分发（见 §7）
    ├── keyboard/
    │   ├── shortcuts.ts          # 快捷键定义（数据源，供说明页与注册共用）
    │   └── gridNavigation.ts     # 网格键盘导航逻辑
    ├── grid/                     # ← 复用：纯函数
    │   ├── createGrid.ts
    │   ├── clipboardParser.ts
    │   ├── pasteContext.ts
    │   ├── editorFactory.ts
    │   ├── pendingEdits.ts
    │   ├── queryAdapter.ts
    │   └── gridState.ts
    ├── design-tokens/
    │   ├── tokens.css            # 飞书风 CSS 变量（含暗色覆盖）
    │   └── theme.ts              # Naive UI themeOverrides
    └── i18n/
        ├── index.ts              # t() + setLocale()
        └── locales/
            ├── zh-CN.ts
            └── en-US.ts
```

## 5. 复用 / 重写 / 不迁移

### 5.1 复用（原样拷贝，单测一并拷贝）

| 模块 | 去向 |
|------|------|
| `contracts.ts` (862 行) | `src/contracts/index.ts` |
| `hostBridge.ts` (479 行) | `src/bridge/hostBridge.ts` |
| `createGrid.ts` / `clipboardParser.ts` / `pasteContext.ts` / `editorFactory.ts` / `pendingEdits.ts` | `src/grid/` |
| `queryAdapter.ts` / `gridState.ts` | `src/grid/` |
| `tableAdminValidation.ts` | `src/services/` |
| `pasteFlow.ts` 的纯函数（`summaryLine`/`outcomeLine`/`errorsByRow`） | `pasteStore` 内部调用 |
| 所有 `.test.ts` | 对应位置 |

**验证**：拷贝的复用模块单测必须 100% 通过（证明逻辑等价）。

### 5.2 重写

| 旧 | 新 | 原因 |
|----|----|------|
| `main.ts` (741 行) | `main.ts`(≤15行) + `App.vue` + `WorkspaceView.vue` + services + stores + components | 拆 6 件事 |
| `tableFlow.ts` 状态机 | `workspaceStore` + `tableStore` | 命令式 → Pinia 响应式 |
| `pasteFlow.ts` 状态部分 | `pasteStore` | 同上（纯函数保留） |
| `tableAdminFlow.ts` | `tableAdminStore`（表单状态入 store） | 表单状态移出 DOM |
| `renderGrid`/`renderSidebar`/`openCreateTableModal` 等命令式 DOM | Vue 组件 | 框架迁移 |
| `main.ts:524-538` operation.failed 路由 | `errorRouter.ts` | 集中化 |
| 手动 `destroy`+`new Tabulator` | `useTabulator` + `setData` | 增量更新 |

### 5.3 不迁移

- `fieldHistoryFlow.ts`：v2 不引用，旧目录保留。
- contracts.ts 的 G1/G3 契约：保留在 contracts.ts（三方镜像不能删），v2 代码不 import。
- 旧 `web-grid/` 整目录：保留到阶段 4 验收通过后删除。

## 6. 暗色模式（跟随系统）

### 6.1 默认行为

- 默认 `system`：跟随操作系统 `prefers-color-scheme`。
- 用户可手动切换 `light` / `dark` / `system`，选择持久化到 `localStorage`（key: `vt:theme`）。
- 系统主题变化时，若当前为 `system` 模式，实时跟随（监听 `matchMedia('(prefers-color-scheme: dark)')` 的 `change` 事件）。

### 6.2 实现（`composables/useTheme.ts`）

```ts
type ThemeMode = 'light' | 'dark' | 'system'

export function useTheme() {
  const mode = ref<ThemeMode>(
    (localStorage.getItem('vt:theme') as ThemeMode) ?? 'system')
  // systemIsDark 是响应式 ref,被 matchMedia change 事件更新。
  // 这样 isDark computed 能同时响应 mode 和系统变化。
  const systemIsDark = ref(matchMedia('(prefers-color-scheme: dark)').matches)
  const isDark = computed(() =>
    mode.value === 'system' ? systemIsDark.value : mode.value === 'dark')

  watchEffect(() => {
    document.documentElement.classList.toggle('dark', isDark.value)
  })

  const media = matchMedia('(prefers-color-scheme: dark)')
  media.addEventListener('change', (e) => { systemIsDark.value = e.matches })

  function setMode(m: ThemeMode) {
    mode.value = m
    localStorage.setItem('vt:theme', m)
  }
  return { mode, isDark, setMode }
}
```

### 6.3 双层 token

Naive UI 用 `NConfigProvider :theme="isDark ? darkTheme : null"` 切换组件库主题；自定义 CSS 变量在 `tokens.css` 里用 `:root`（亮）和 `:root.dark`（暗）双套覆盖。`App.vue` 在根 `<html>` 上 toggle `dark` class。

```css
:root {
  --vt-bg: #ffffff;  --vt-fg: #1f2329;  --vt-gray-50: #f7f8fa;  ...
}
:root.dark {
  --vt-bg: #17191f;  --vt-fg: #c9cdd4;  --vt-gray-50: #1e2128;  ...
}
```

Naive UI 的 `themeOverrides` 在亮/暗两套下分别配置，确保品牌色 `#3370ff` 在两种模式下都达标（暗色下主色会略提亮到 `#5b8bff` 保证对比度）。

## 7. 快捷键：复制 / 粘贴 / 撤销 + 键盘导航

### 7.1 快捷键清单（`keyboard/shortcuts.ts` 为唯一数据源）

| 快捷键 | 行为 | 作用域 | 可撤销 |
|--------|------|--------|--------|
| `Ctrl/Cmd+C` | 复制选中单元格为 TSV | 网格 | — |
| `Ctrl/Cmd+V` | 粘贴（进入预览面板） | 网格 | ✅（见 §7.3） |
| `Ctrl/Cmd+Z` | 撤销最近一次可撤销操作 | 全局 | — |
| `Ctrl/Cmd+Shift+Z` / `Ctrl+Y` | 重做 | 全局 | — |
| `Enter` | 进入单元格编辑 / 向下移动 | 网格 | — |
| `Esc` | 取消编辑 / 关闭面板 | 全局 | — |
| `Tab` / `Shift+Tab` | 向右/左移动单元格 | 网格 | — |
| `Arrow` 键 | 移动选中单元格 | 网格 | — |
| `Ctrl/Cmd+A` | 全选当前表所有行 | 网格 | — |
| `Delete` / `Backspace` | 删除选中行（弹确认） | 网格 | ✅ |
| `F2` | 进入单元格编辑 | 网格 | — |
| `Ctrl/Cmd+R` | 刷新当前表 | 全局 | — |
| `Ctrl/Cmd+N` | 新建表（打开 modal） | 全局 | — |
| `?` | 打开快捷键说明页 | 全局 | — |

`shortcuts.ts` 导出一个数组，每项含 `{ keys, action, scope, category, description_zh, description_en }`。说明页（`ShortcutsView.vue`）和注册器（`useKeyboard`）都消费这个数组，**保证说明页永远准确**。

> **注**：上表"可撤销"列只标了单元格级数据操作。表结构操作（建表 `Ctrl+N`、删表）**不可撤销**——撤销栈不接收这类操作。完整撤销限制见 §7.3，并会写入说明页的"说明"分组让用户预期对齐。

### 7.2 键盘导航（`keyboard/gridNavigation.ts`）

Tabulator 内置部分键盘行为，但 v2 要接管以统一风格：
- 方向键移动选中单元格（单选），`Shift+方向键` 扩展选区。
- `Tab`/`Enter` 按飞书习惯：`Tab` 横向、`Enter` 纵向，到边界换行。
- `Esc` 取消编辑或关闭浮层。
- 编辑态下方向键移动光标，非编辑态下移动选中。

实现层面：`useKeyboard` 在 `mounted` 时在 `document` 上注册全局快捷键（复制/粘贴/撤销/刷新/新建/?），在网格获得焦点时注册网格级处理器（方向键/Tab/Enter/Delete）。用 `event.target` 判断焦点是否在 input/modal 内，避免误触发。

### 7.3 撤销范围与限制（关键约束）

**契约层不返回足够信息做完美 undo**（已核实 `contracts.ts`）：

| 操作 | 结果契约 | 可撤销性 | 撤销实现 |
|------|---------|---------|---------|
| `updateCell` | 返回 `storedValue`+`currentRow`；请求 payload 含 `oldValue` | ✅ 完全可逆 | 用 `oldValue` 再发一次 `updateCell` |
| `insertRow` | 返回 `rowKey`+`row` | ✅ 完全可逆 | 用 `rowKey` 发 `deleteRows` |
| `deleteRows` | 返回 `deletedRowKeys`，**不含删前行数据** | ⚠️ 部分可逆 | 删除前前端缓存行快照，撤销时按快照 `insertRow` |
| `applyPaste` | 返回 `created`/`updated`/`skipped` keys，**不含 update 旧值** | ⚠️ 部分可逆 | created 的可按 key 删除；updated 的无旧值，撤销时提示"部分操作无法完美恢复" |
| `createTable` / `deleteTable` | 仅 success/failure | ❌ 不入撤销栈 | 表结构变更风险高，不纳入 |

**`historyStore` 设计**：

```ts
interface HistoryEntry {
  id: string
  kind: 'updateCell' | 'insertRow' | 'deleteRows' | 'applyPaste'
  table: string
  schemaRevision: string           // 撤销时用,需校验未变
  label: string                     // 显示用,如"编辑单元格"/"粘贴 12 行"
  timestamp: number
  // 反向操作所需数据(按 kind 不同)
  undo: () => Promise<void>         // 调对应 service 反向
  // 重做所需数据
  redo: () => Promise<void>
}

state: { undoStack: HistoryEntry[], redoStack: HistoryEntry[] }
```

- `undo()` 弹栈顶 → 执行 `entry.undo()` → push 到 redoStack。
- `redo()` 弹 redoStack 栈顶 → 执行 `entry.redo()` → push 回 undoStack。
- 上限 50 条（FIFO 淘汰）。
- `schemaRevision` 变化时（schema 被改、表被切换、directus.changed 收到）清空整个栈（避免脏撤销）。
- 冲突处理：反向操作若返回 `edit_conflict` / `schema_mismatch`，向用户提示"撤销失败：数据已变化"，不静默吞错。

**明确限制**（写入说明页）：
- 删除行的撤销依赖前端缓存的行快照；若快照与当前 schema 不符（字段被删），撤销会失败并提示。
- 粘贴包含 `updated` 行时，撤销无法恢复那些行的原始值，只能撤销 `created` 部分。
- 表结构变更（建表/删表/字段变更）不可撤销。
- 撤销栈在切换表、刷新、schema 变更后清空。

## 8. 快捷键说明页

`ShortcutsView.vue`，由 `?` 键或工具栏"帮助"图标打开。用 `n-modal` 全屏弹出，`n-table` 按 category 分组展示：

| 分组 | 内容 |
|------|------|
| 通用 | 复制/粘贴/撤销/重做/刷新/新建/帮助 |
| 网格导航 | 方向键/Tab/Enter/Esc/F2/Ctrl+A/Delete |
| 说明 | 撤销限制说明（§7.3 的限制清单，让用户预期对齐） |

数据来自 `keyboard/shortcuts.ts`，确保"代码注册的快捷键"与"说明页展示的快捷键"永远是同一份。

## 9. 视觉规范（飞书风 design tokens）

### 9.1 主色与语义色（`design-tokens/tokens.css`）

```css
:root {
  /* 品牌主色 — 飞书蓝紫 #3370FF + 6 级明度 */
  --vt-color-primary-50:  #e8f0ff;
  --vt-color-primary-100: #d4e3ff;
  --vt-color-primary-200: #b3ccff;
  --vt-color-primary-500: #3370ff;
  --vt-color-primary-600: #2b5fe0;
  --vt-color-primary-700: #1f47b3;

  /* 语义色(飞书) */
  --vt-color-success: #00b88a;
  --vt-color-warning: #ffa600;
  --vt-color-danger:  #f54a45;
  --vt-color-info:    #3370ff;

  /* 中性灰阶 11 级 */
  --vt-gray-0:#fff; --vt-gray-50:#f7f8fa; --vt-gray-100:#f2f3f5;
  --vt-gray-200:#e5e6eb; --vt-gray-300:#c9cdd4; --vt-gray-400:#86909c;
  --vt-gray-500:#4e5969; --vt-gray-600:#272e3b;

  /* 阴影(克制) */
  --vt-shadow-1: 0 1px 2px rgba(0,0,0,.04);
  --vt-shadow-2: 0 2px 8px rgba(0,0,0,.08);
  --vt-shadow-3: 0 8px 24px rgba(0,0,0,.12);

  /* 圆角 */
  --vt-radius-sm: 4px; --vt-radius-md: 6px; --vt-radius-lg: 8px;

  /* 间距 4px 基准 */
  --vt-space-1: 4px; --vt-space-2: 8px; --vt-space-3: 12px;
  --vt-space-4: 16px; --vt-space-5: 24px;

  /* 字阶(飞书) */
  --vt-font-caption: 12px;  --vt-font-body: 13px;
  --vt-font-label: 14px;    --vt-font-title: 16px;  --vt-font-heading: 18px;

  /* 动效(克制) */
  --vt-ease: cubic-bezier(0.34, 0.6, 0.2, 1);
  --vt-duration-fast: 120ms; --vt-duration-base: 200ms;
}
:root.dark { /* 暗色覆盖,数值见 §6.3 */ }
```

### 9.2 Naive UI themeOverrides（`design-tokens/theme.ts`）

亮/暗两套 `GlobalThemeOverrides`，覆盖 `common.primaryColor` 等为飞书值，`Button.fontWeightStrong='500'`，`Card.borderRadius='8px'`。字体栈优先系统字体（CSP 禁 CDN，不引外部 web font）：`"PingFang SC", "Microsoft YaHei", system-ui, -apple-system, "Segoe UI", sans-serif`。暗色下主色提亮到 `#5b8bff` 保证对比度。

### 9.3 图标规范

统一 `lucide-vue-next`，线性 1.5px 描边，三档尺寸：12px（状态指示）/16px（按钮，默认）/18px（关闭）。不用 emoji，不用字符图标。映射：Plus/RefreshCw/Trash2/Plug/Settings/X/Loader2(spin)/AlertCircle/CheckCircle。

### 9.4 布局

`WorkspaceView` = `flex row`：左侧 `AppSidebar`（220px，bg gray-50，右边框 gray-200）+ 右侧 `#main`（flex col：Toolbar + GridHost + StatusBar）。表格行高 32px（Tabulator `rowHeight:32`）。

## 10. i18n

自写轻量方案（不引 vue-i18n）：

- `i18n/index.ts`：`t(key, params?)` + `setLocale(l)`，约 30 行。
- 默认 `zh-CN`（与现状一致），locale 持久化到 `localStorage` key `vt:locale`。
- `locales/zh-CN.ts` + `locales/en-US.ts` 各约 50 条。
- 替换 `main.ts`/`pasteFlow.ts`/`pasteContext.ts` 里散落的中英文硬编码。
- 未来可由 host 通过 bridge 推送 locale（本次不实现，预留接口）。

## 11. 实施阶段（spec 据此展开为实施计划）

```
阶段 0: 脚手架
  - 新建 web-grid-v2/,初始化 Vue+Vite+TS+Pinia+Naive UI+Lucide+Tabulator
  - vite.config.ts 输出 dist/,base 适配虚拟主机
  - index.html 复制原 CSP
  - 验收: npm run build 产出 dist/index.html,WebView2 能加载空白页,控制台无 CSP 违规

阶段 1: 基础设施层
  - 拷贝 contracts.ts + hostBridge.ts + grid/* 纯函数 + tableAdminValidation.ts 及其单测
  - 写 design-tokens(tokens.css + theme.ts,含暗色)
  - 写 i18n 骨架 + zh-CN/en-US
  - 验收: 复用模块现有 vitest 全绿

阶段 2: 状态与服务层
  - 5 个 Pinia store(workspace/table/paste/tableAdmin/ui)
  - 5 个 service(含 errorRouter)
  - historyStore + keyboardStore
  - 验收: 每个 store 单测(hostBridge mock,复用现有 hostBridge.test.ts 模式)

阶段 3: 视图层
  - 所有 components + WorkspaceView + ShortcutsView
  - useTabulator / useTheme / useKeyboard composables
  - 验收: jsdom 下组件渲染 + 交互测试

阶段 4: 集成与替换
  - main.ts + App.vue 组装
  - 切换 WebViewAssetService 指向 web-grid-v2/dist
  - 端到端冒烟(见 §12.1)
  - 删除旧 web-grid/
  - 验收: §12 全部通过

阶段 5: 架构债验收
  - 逐项核对 §12.2
```

## 12. 验收标准

### 12.1 功能等价（端到端冒烟）

- 连接 Directus → 集合列表出现
- 选表 → 分页加载（多页累积，无销毁重建）→ 行数正确
- 刷新 → 重新加载
- 单元格编辑 → 提交 → 值更新
- Ctrl+C 复制选中 → Ctrl+V 粘贴 → 预览面板 → 确认 → 应用 → 结果正确
- 撤销编辑 → 恢复原值；重做 → 恢复编辑
- 撤销插入 → 删除插入行；撤销删除 → 恢复行
- 建表（modal，字段从 store 驱动）→ 创建成功 → 侧栏出现
- 删表（确认 modal）→ 删除成功 → 侧栏消失
- 管理后台入口 → 跳转 Directus admin
- 暗色模式：跟随系统切换 → 全 UI 跟随；手动切亮/暗 → 持久化
- 快捷键说明页（?）→ 展示全部快捷键 + 撤销限制说明

### 12.2 架构债修正验收

- `main.ts` ≤ 15 行
- 表单状态在 `tableAdminStore.form`，不在 DOM
- `operation.failed` 仅 `errorRouter.ts` 一处入口
- GridHost 用 `setData` 增量更新，无 `destroy` + `new Tabulator` 循环
- i18n 覆盖所有用户可见文案，无硬编码中英文
- components 无 bridge/services import
- stores 无 bridge import

### 12.3 质量门禁

- 复用模块单测 100% 通过
- 每个 store 有单测；historyStore 覆盖 undo/redo/conflict/clear
- `keyboard/shortcuts.ts` 的每项有对应注册器测试
- `npm run build` 产物无 CSP 违规（`connect-src 'none'` 下零网络请求）
- TypeScript `tsc --noEmit` 零错误
- 产物可直接 drop 到 `<exe-dir>/web-grid`，`index.html` 入点不变

### 12.4 契约不变量

- hostBridge 的 16 个出站 + 13 个入站白名单不变
- 消息信封 `{type, requestId?, payload?}` 形状不变
- `requestId: null` 的失败路径处理不变
- 4 MiB 入站消息上限不变
- host 侧（.NET/Python）零改动

## 13. 回滚策略

- 旧 `web-grid/` 保留到阶段 4 验收通过后删除。
- `WebViewAssetService.ResolveWebGridFolder()` 可临时改指回旧目录（一行配置回滚）。
- Git 分支隔离：`feature/web-grid-v2`，阶段 4 前不合入 main。

## 14. 风险与对策

| 风险 | 对策 |
|------|------|
| Tabulator 与 Vue 响应式集成抖动（重复 setData、watch 抖动） | `useTabulator` 用 `watchDebounced` 或手动比较 rows 引用，避免无意义 setData |
| Naive UI 产物体积偏大影响首屏 | 按需引入 + Vite manualChunks 拆包；首屏只加载用到的组件 |
| 暗色下 Tabulator 表格样式不跟随（Tabulator 自带 CSS） | 引入 Tabulator 的 `tabulator Midnight` 主题或在 tokens.css 里覆盖 `.dark .tabulator-*` 选择器 |
| 撤销冲突导致数据不一致 | schemaRevision 校验 + 冲突时明确提示，不静默吞错；历史栈在 schema 变更时清空 |
| 快捷键在 WebView2 内被宿主拦截 | 阶段 4 冒烟时逐个测试；如被拦截，与 .NET 侧协调（本次希望零 .NET 改动，故优先在前端处理） |
| Lucide 图标在 1.5px 描边下过细 | 视觉验收时统一调到 2px（一个常量） |

## 15. 参考链接

- 飞书色彩规范：https://open.feishu.cn/document/design-specification/design-language/color?lang=zh-CN
- 飞书字体规范：https://open.feishu.cn/document/design-specification/design-language/font?lang=zh-CN
- NocoDB 仓库：https://github.com/nocodb/nocodb
- Naive UI 文档：https://www.naiveui.com/
- Lucide 图标：https://lucide.dev/
- Tabulator 文档：https://tabulator.info/docs/6.5
