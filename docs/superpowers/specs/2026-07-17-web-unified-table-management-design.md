# 设计：前端统一 — 表管理搬入 web 单页

- **日期**: 2026-07-17
- **状态**: 已批准（设计阶段），待写实施计划
- **范围**: 把原生 WPF 表管理窗口搬进现有 WebView2 web-grid 单页，C# 宿主保留

## 1. 背景与动机

### 1.1 现状

VibeTable 桌面 app 是 WPF 壳 + WebView2 渲染 TypeScript web-grid（Tabulator）的混合架构。Directus 起来之后：

- **表管理**（建表/删表/列表）在**原生 WPF 窗口**里（`TableManagementWindow` + `TableAdminWindow`），由 `MainWindow` 的 `表管理` 按钮触发，直接持有 `IDirectusRpcGateway`。
- **数据交互**（行级 CRUD/粘贴/导入导出）在 **WebView2 的 TS web-grid** 里，通过 postMessage → C# `WorkspaceRequestDispatcher` → RPC。

两套 UI 技术栈并存，操作割裂：用户点 `表管理` 弹原生窗，关掉再回 web 表格。

### 1.2 动机

统一前端表现，消除"原生壳 vs 网页"的割裂感。具体目标：表管理和数据交互在同一 web 单页内完成。

### 1.3 明确不做（YAGNI）

- **不换 WinUI 3**：WPF 没坏，换框架是纯重写债，不解决职责划分问题。
- **不换 Electron**：C# 宿主不是"纯壳"——它承担 Directus 进程编排（含 Windows Job Object 孤儿防护，Electron 无等价物）、npm ci 完整性校验、schema 播种、首启窗口等已验证职责。换 Electron 是用重写整层宿主的代价换一个不解决原始痛点的语言切换。安装包还会 +150MB。
- **不做 schema 深度编辑**：字段重命名/类型迁移/关系编辑是后续轮次。本轮只平移现有能力。
- **不做启动期窗口 web 化**：首启凭证窗/7步进度窗/交互登录窗在 Directus 未就绪时显示，那时 WebView2 主单页还无法渲染，强行 web 化会引入双轨架构。它们留在 C#。
- **不补 SQLite 加密**：个人单机、数据可信的定位下，文件加密性价比低。
- **不做 E2E**：项目无 E2E 基建，WebView2 内交互难自动化。手工冒烟作为发布前检查。

## 2. 总体架构与不变量

### 2.1 改动边界

只动"Directus 就绪之后"的 UI 层。完全不碰：

- 启动期窗口：`DirectusFirstRunWindow` / `DirectusStartupWindow` / `DirectusLoginWindow`
- 进程编排层：`DirectusSupervisor` / `DirectusPackageManager` / `DirectusSchemaBootstrapper` / `DirectusFirstRunState`
- WebView2 硬化（`ApplyHardening`：导航白名单、`NewWindowRequested` cancel、Release 关 devtools）
- Python BFF（`table_admin_service` 等后端逻辑零改动）

### 2.2 新前端结构

web 单页内 `#app` 从 `flex column` 改为 `flex row`：

```
#app (flex row)
├── #sidebar              ← 新增
│   ├── [+ 新建表] 按钮
│   └── 表列表 (每行: 表名 + ⋯ 删除)
└── #main (flex col)
    ├── #toolbar          ← 连接/刷新/状态 (移除原 #table-select <select>)
    ├── #status
    └── #grid-wrapper     ← Tabulator 表格 + overlay (不变)
```

侧边栏取代 toolbar 里的表选择 `<select>`：点侧边栏表名 = 切表。

### 2.3 核心不变量

1. **token 仍不出 broker**——侧边栏所有操作走 postMessage → C# → `IDirectusRpcGateway` → RPC，和原生窗口用同一实例。CSP `connect-src 'none'` 不变，webview 仍零网络。
2. **导航白名单不变**——webview 仍只允许 `app.vibetable.local`。
3. **表列表单一数据源**——只由 host 下发（`database.opened` 初始 + `database.collectionsChanged` 变更后），web 不自己拉。

## 3. 桥接契约（方案 A）

### 3.1 新增 web→host 请求（2 个）

**`tableAdmin.createRequested`**

```
payload: {
  name: string,                                        // 客户端先校验 ^[A-Za-z][A-Za-z0-9_]{0,63}$
  fields: Array<{ key: string, type: TableFieldType }> // type ∈ 6 种
}
host 处理: _directusGateway.CreateTableAsync(name, fields, token)
回复: operation.failed (失败时, 透传 backend 错误)
成功后: host 重新 ListCollectionsAsync → 过滤排序 → 发 database.collectionsChanged
```

**`tableAdmin.deleteRequested`**

```
payload: { collection: string }
host 处理: _directusGateway.DeleteTableAsync(collection, token)
回复: operation.failed (失败时)
成功后: 同样发 database.collectionsChanged
```

> 确认对话框（建表校验失败/删表二次确认）留在 web 层，host 不弹 MessageBox。

### 3.2 新增 host→web 通知（1 个）

**`database.collectionsChanged`**

```
payload: {
  tables: string[],                                    // 已用 IsUserTable 过滤、OrderBy(OrdinalIgnoreCase) 排序
  capabilityHashes?: { [collection: string]: string }  // 可选, 透传
}
```

web 收到后更新侧边栏表列表。首次仍由 `database.opened` 下发（payload 不变），`collectionsChanged` 只用于 create/delete 后的刷新。

### 3.3 复用现有通道（0 改动）

- `database.opened`：初始表列表，照旧。
- `operation.failed`：所有失败走它，照旧。
- `directus.changed`：行级数据变更，照旧（表管理不依赖它）。

### 3.4 `TableFieldType` 第三份拷贝

`contracts.ts` 新增：

```ts
export const TABLE_FIELD_TYPES = ["string","integer","decimal","date","boolean","text"] as const;
export type TableFieldType = typeof TABLE_FIELD_TYPES[number];
export const TABLE_NAME_PATTERN = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;
```

这是 C#（`TableAdminWindow.SupportedFieldTypes`）/ Python（`table_admin.FieldType`）之外的第三份。**已知重复**：修改任一处需同步另两处。

### 3.5 白名单三处同步（必做，否则消息被 drop）

1. `contracts.ts` 的 `WebMessageType` union 加两个请求类型；`HostMessageType` 加 `database.collectionsChanged`
2. `hostBridge.ts` 的 `WEB_MESSAGE_TYPES` runtime Set 加两个请求类型；`HOST_EVENT_TYPES` 加 `database.collectionsChanged`
3. C# `WebMessageRouter.WebRequestWhitelist` 加两个请求类型；`HostNotificationWhitelist` 加 `database.collectionsChanged`

## 4. C# 宿主侧改动

### 4.1 给 `WorkspaceRequestDispatcher` 注入 `IDirectusRpcGateway`

现状（`WorkspaceRequestDispatcher.cs:50-60`）只注入 `TableWorkspaceService` / `IDatabasePicker` / `IWebReplySink` / `GridStateCoordinator?`。

因为 `_directusGateway` 在 `EnsureDirectusSessionAsync`（约 line 642）才创建，**晚于** dispatcher 构造期，所以**不走构造参数**，改用 **setter + 内部可空字段**：

```csharp
// 构造函数签名不变（不新增构造参数）
public WorkspaceRequestDispatcher(
    TableWorkspaceService workspace,
    IDatabasePicker picker,
    IWebReplySink reply,
    GridStateCoordinator? coordinator = null)
{
    // ... 现有
}

// 新增: 可空字段 + setter
private IDirectusRpcGateway? _directusGateway;
public void SetDirectusGateway(IDirectusRpcGateway gateway) => _directusGateway = gateway;
```

`MainWindow` 在 `EnsureDirectusSessionAsync` 成功后（`_directusGateway` 创建处，约 line 642）调用 `SetDirectusGateway(...)`。handler 内对 `_directusGateway is null` 优雅降级：返回 `operation.failed` code `NOT_AUTHENTICATED`。

### 4.2 在 `DispatchAsync` switch 加两个 case

```csharp
case "tableAdmin.createRequested": await OnCreateTableRequestedAsync(request); break;
case "tableAdmin.deleteRequested": await OnDeleteTableRequestedAsync(request); break;
```

handler 逻辑：

1. 校验 `_directusGateway` 非 null（否则 `PostOperationFailed` code `NOT_AUTHENTICATED`）
2. 从 payload 提取 `name` / `fields`（用现有 `TryGetString` / `TryGetProperty`）
3. 调 `CreateTableAsync` / `DeleteTableAsync`
4. 成功：`ListCollectionsAsync` → `IsUserTable` 过滤 → `OrderBy(OrdinalIgnoreCase)` → `_reply.PostNotification("database.collectionsChanged", { tables, capabilityHashes })`
5. 失败：`PostOperationFailed`，message 透传 backend 错误

### 4.3 `IsUserTable` 过滤逻辑抽共享方法

现状在 `TableManagementWindow.xaml.cs:70-86`。抽成共享静态方法（如 `DirectusCollectionFilter.IsUserTable`，放 Infrastructure 或 Desktop Services），过滤规则不变：

```csharp
// 排除:
// - "directus_*"
// - "vibetable_document*"
// - "vibetable_workspace*"
```

`TableManagementWindow`（删除前）和新 dispatcher 都引用。删窗口后唯一调用方是 dispatcher。

### 4.4 白名单（`WebMessageRouter.cs`）

`WebRequestWhitelist` 加：

```csharp
"tableAdmin.createRequested",
"tableAdmin.deleteRequested",
```

`HostNotificationWhitelist` 加：

```csharp
"database.collectionsChanged",
```

### 4.5 删除原生表管理窗口（一次性重写收尾）

- 删 `TableManagementWindow.xaml` + `.xaml.cs`
- 删 `TableAdminWindow.xaml` + `.xaml.cs`
- `MainWindow.xaml` 删 `表管理` 按钮（`ManageTablesButton`，line 27-34）
- `MainWindow.xaml.cs` 删 `OnManageTables`（835-849）、`ManageTablesButton.IsEnabled=...`（227）

> `IDirectusRpcGateway` / `JsonRpcDirectusGateway` / Python `table_admin_service` 全部保留——它们现在是 web 的后端。

## 5. web-grid TS 侧改动

### 5.1 契约层（`contracts.ts`）

```ts
// WebMessageType 加
| "tableAdmin.createRequested"
| "tableAdmin.deleteRequested"

// HostMessageType 加
| "database.collectionsChanged"

// 新 payload 接口
interface TableAdminCreatePayload {
  readonly name: string;
  readonly fields: ReadonlyArray<{ readonly key: string; readonly type: TableFieldType }>;
}
interface TableAdminDeletePayload { readonly collection: string; }
interface CollectionsChangedPayload {
  readonly tables: readonly string[];
  readonly capabilityHashes?: Readonly<Record<string, string>>;
}

// 注册到 WebPayloadMap / HostPayloadMap
```

### 5.2 runtime 白名单（`hostBridge.ts`）

`WEB_MESSAGE_TYPES` Set 加两个请求类型；`HOST_EVENT_TYPES` 加 `database.collectionsChanged`。

> **已知项**：这两个 runtime Set 目前比 TS union 旧（缺 G1 `history.*` / G3 `document.*` 类型）。本轮只加表管理相关类型，不顺手补 G1/G3（避免扩大改动面）。

### 5.3 新 flow：`tableAdminFlow.ts`

模仿 `fieldHistoryFlow.ts` 的**纯 reducer + async orchestrator** 模式：

```ts
interface TableAdminState {
  readonly collections: readonly string[];
  readonly status: "idle" | "creating" | "deleting" | "error";
  readonly error: string | null;
}

// 纯 reducer
function applyCreateStarted(s): TableAdminState { /* status=creating */ }
function applyCreateSucceeded(s): TableAdminState { /* status=idle */ }
function applyCreateFailed(s, msg): TableAdminState { /* status=error, error=msg */ }
// delete 同理
function applyCollectionsChanged(s, tables): TableAdminState { /* collections=tables, status=idle */ }

// async orchestrator
async function requestCreate(bridge, name, fields, dispatch): Promise<void> {
  dispatch({ type: "createStarted" });
  try {
    await bridge.request("tableAdmin.createRequested", { name, fields });
    dispatch({ type: "createSucceeded" }); // 表列表等 host 推 collectionsChanged
  } catch (e) {
    dispatch({ type: "createFailed", message: (e as Error).message });
  }
}
```

### 5.4 侧边栏 DOM（`index.html` + `styles.css`）

`index.html`：

```html
<div id="app">
  <aside id="sidebar" class="sidebar">
    <button id="new-table-btn" type="button" disabled>+ 新建表</button>
    <ul id="table-list" class="table-list"></ul>
  </aside>
  <div id="main">
    <div id="toolbar">...</div>   <!-- 移除 #table-select -->
    <div id="status">...</div>
    <div id="grid-wrapper">...</div>
  </div>
</div>
```

`styles.css`：加 `.sidebar` / `.table-list` / `.table-list__item` / `.table-list__delete` 等，复用现有 `--vibetable-*` CSS 变量，风格对齐 `.toolbar`。

### 5.5 建表 modal（web 内）

`TableAdminWindow` 的 web 等价物：

- 表名 input
- 动态字段行（name input + type `<select>`，6 种类型）
- `+ 添加字段` 按钮
- 客户端校验：表名 + 每个非空字段名用 `TABLE_NAME_PATTERN` 校验；**空字段名跳过**（和 C# 行为一致）
- 创建中禁用按钮，失败显示错误，成功关闭 modal（表列表由 `collectionsChanged` 自动刷新）

### 5.6 删除确认（web 内）

`TableManagementWindow` 的 `MessageBox` 等价物——web confirm modal，文案：`确定要删除表 "{collection}" 吗？该操作不可恢复`。确认后调 `requestDelete`。

### 5.7 `main.ts` 接线

- 订阅 `bridge.on("database.collectionsChanged", payload => dispatch({type:"collectionsChanged", tables: payload.tables}))`
- `database.opened` 处理改为：同时更新 `tableFlow`（现有表格逻辑）和 `tableAdminFlow`（侧边栏表列表）
- 侧边栏表项 click → `flow.selectTable(name, ...)`（复用现有切表逻辑，触发源从 `<select>` change 变侧边栏 click）
- `#new-table-btn` click → 打开建表 modal
- 删除菜单 click → 打开确认 modal → 确认后 `requestDelete`

### 5.8 移除 `#table-select`

删 `main.ts:89-105` 的 `populateTableSelect`；删 `table-select` 的 change 监听（`main.ts:361-364`）。切表改由侧边栏承担。

### 5.9 测试

新 `tableAdminFlow.test.ts`：

- 纯 reducer 测试（createStarted/Succeeded/Failed、delete 同理、collectionsChanged）
- orchestrator 测试（mock `bridge.request`，成功/失败两条路径）

校验单测：表名/字段名正则通过/拒绝、空字段跳过、6 种类型枚举完整性。

## 6. 认证门控与边界情况

### 6.1 auth 信号（不加新通知）

侧边栏启用心智遵循"有表列表就允许操作"：

- `database.opened` 到达 = session 已认证（host 只在 `EnsureDirectusSessionAsync` 成功后发 `database.opened`）。
- `database.opened` 一到，`#new-table-btn` enable，侧边栏允许删除。
- host 侧 dispatcher 兜底：gateway 为 null 时返回 `operation.failed` code `NOT_AUTHENTICATED`（双保险）。

**不**加显式 auth 通知，避免扩大改动面。侧边栏 enable 状态与表列表有无自然耦合。

### 6.2 session 过期（后续工作）

token 过期时 dispatcher 调 `CreateTableAsync` 返回 401 → 走 `operation.failed` → web 显示错误。**本轮不实现自动 re-login**。spec 记一笔：session 过期的优雅恢复是后续工作。

### 6.3 `capabilityHashes` 透传

`database.opened` 现带 `capabilityHashes`。`collectionsChanged` 顺带带上，但**侧边栏本轮不显示 capability 信息**——只透传，避免契约丢信息。后续若要显示行数/能力图标，数据已在。

### 6.4 表列表排序一致性

host 侧 `ListCollectionsAsync` 结果在 dispatcher 里过滤 + `OrderBy(OrdinalIgnoreCase)` 排序后下发。web 侧**不再二次排序**，直接按 host 给的顺序显示。保证侧边栏顺序和原 `TableManagementWindow` 一致。

## 7. 测试策略

| 层 | 测试 | 工具 |
|---|---|---|
| TS reducer | `tableAdminFlow.test.ts`：所有 reducer 纯函数 + orchestrator（mock bridge） | vitest（jsdom） |
| TS 校验 | 表名/字段名正则、空字段跳过、6 种类型枚举 | vitest |
| C# dispatcher | 新两个 case：gateway null → `NOT_AUTHENTICATED`；成功 → 发 `collectionsChanged`；失败 → `PostOperationFailed` | xUnit |
| C# 过滤 | `IsUserTable` 抽共享方法后，单测覆盖 `directus_*` / `vibetable_document*` / `vibetable_workspace*` / 正常表 | xUnit |
| 契约同步（可选） | 三个白名单一致性断言 | vitest + xUnit |
| 手工冒烟 | 建表→侧边栏出现→删表→消失；切表→表格切换；非法名失败 | 手动 |

## 8. 已知风险与已知项

| 项 | 说明 | 处置 |
|---|---|---|
| `FieldType`/正则三份拷贝 | C#/Python/TS 各一份 | spec 注明，修改任一处需同步另两处 |
| runtime Set 与 union 不同步 | `hostBridge.ts` Set 缺 G1/G3 类型 | 本轮不补，记已知项 |
| 开发循环无 HMR | 无 vite dev server，CSP 堵 localhost | 循环为 `npm run build` + 重启宿主；用 `window.__vibeTableBridge` debug hook |
| session 过期无优雅恢复 | token 过期时操作失败 | 后续工作 |
| 契约同步靠人 | 三处白名单不一致则消息被静默 drop | 可选：加一致性测试 |

## 9. 推进方式

一次性重写（用户决定）。建议在 feature 分支上完成全部改动后单 PR 合入，但内部按层次提交以便 review：

1. 契约层（TS + C# 白名单 + payload 类型）
2. C# dispatcher + `IsUserTable` 抽取
3. TS `tableAdminFlow` + 测试
4. 侧边栏 DOM + 样式 + main.ts 接线
5. 建/删 modal
6. 删除原生窗口 + 表管理按钮
7. 全量测试 + 手工冒烟

## 10. 关键文件清单

### C# 改动
- `desktop/src/VibeTable.Desktop/Services/WorkspaceRequestDispatcher.cs`（注入 gateway + 2 handler）
- `desktop/src/VibeTable.Desktop/Services/WebMessageRouter.cs`（白名单）
- `desktop/src/VibeTable.Desktop/MainWindow.xaml`（删按钮）
- `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`（删 handler + setter 调用）
- `desktop/src/VibeTable.Desktop/Services/DirectusCollectionFilter.cs`（新，`IsUserTable` 抽出）
- `desktop/src/VibeTable.Desktop/TableManagementWindow.xaml(.cs)`（删）
- `desktop/src/VibeTable.Desktop/TableAdminWindow.xaml(.cs)`（删）

### TS 改动
- `desktop/web-grid/src/contracts.ts`（新类型 + payload）
- `desktop/web-grid/src/hostBridge.ts`（runtime Set）
- `desktop/web-grid/src/tableAdminFlow.ts`（新，纯 reducer）
- `desktop/web-grid/src/tableAdminFlow.test.ts`（新）
- `desktop/web-grid/index.html`（侧边栏 + 移除 select）
- `desktop/web-grid/src/styles.css`（侧边栏样式）
- `desktop/web-grid/src/main.ts`（接线 + 移除 select 逻辑）

### 不动
- `desktop/src/VibeTable.Infrastructure/Directus/*`（进程编排全不动）
- `desktop/src/VibeTable.Desktop/Directus*Window.xaml(.cs)`（启动期窗口）
- `backend/`（Python BFF 零改动）
- `desktop/src/VibeTable.Desktop/Services/IDirectusRpcGateway.cs` / `JsonRpcDirectusGateway.cs`（保留，现在是 web 后端）
