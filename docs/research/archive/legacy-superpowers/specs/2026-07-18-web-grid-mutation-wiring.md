> 历史设计归档；不属于当前产品实现。

# 设计：web-grid mutation 接线 — 单元格编辑/插入/删除行/粘贴应用/撤销

- **日期**: 2026-07-18
- **状态**: 已批准（设计阶段），待写实施计划
- **范围**: 在已完成的 web-grid-v2 重做基础上，接线所有 mutation 能力：单元格即时编辑、插入行、删除行（不弹确认，靠撤销）、粘贴应用、复制/粘贴/删除/撤销快捷键、historyStore 生产者。**不动** contracts/bridge/.NET 宿主/Python 后端/Directus。

## 1. 背景与动机

### 1.1 现状

web-grid-v2 重做（commit 0713126..0dd534a + I3/I4 修复）完成了 Vue 3 迁移、分层架构、飞书风视觉、暗色模式、快捷键骨架、撤销栈。**最终审查（opus）发现 4 个 Important 问题**，其中 I3（建表 modal 不关闭）和 I4（i18n 漏网）已修复（commits a3153ee、768f65b）。

剩余 I1（mutation 未接线）+ I2（快捷键回调未接）是本次设计的目标。根因：`createGrid.ts` 是 Phase A 的**只读**设计（`editable:false`、`clipboard:false`），旧代码也从未接 mutation。

### 1.2 后端可行性（已核实，零后端改动）

深入调研确认**所有 mutation 端到端可用，无需任何后端改动**：

| 请求 | 处理位置 | 状态 |
|------|---------|------|
| `table.updateCellRequested` | .NET 宿主 `DirectusTableGateway.UpdateCellAsync` → `directus.update` | ✅ 可用（无乐观并发，last-write-wins） |
| `table.insertRowRequested` | .NET 宿主 → `directus.create` | ✅ 可用 |
| `table.deleteRowsRequested` | .NET 宿主 → 逐行 `directus.archive` | ✅ 可用（非原子、软删除；`expectedDigest` 必填但被忽略） |
| `table.applyPasteRequested` | Python `PasteService.apply`（原子、幂等、token） | ✅ 完整实现 |
| `table.previewPasteRequested` | Python `PasteService.preview`（token 签发、10k 上限） | ✅ 完整实现 |
| `table.editSchemaLoaded` | .NET 宿主 `DirectusTableGateway.EditorFor` 合成 Editor union | ✅ 自动随 `table.selected` 发送；4 种 editor：checkbox/number/date/text |

旧 `table.updateCell/insertRow/deleteRows` Python RPC 已**有意移除**（`backend/__main__.py:314-317`），因为宿主层接管，翻译成通用 `directus.*` RPC。

### 1.3 已有前端基础设施（Task 2 已拷贝）

- `editorFactory.ts`：`tabulatorEditor(editor)` 工厂（number/boolean/date/single_select/multi_select）+ `validateLocally` + `parseValue`
- `pendingEdits.ts`：`PendingEdits` 类（乐观编辑存储）
- `pasteContext.ts`：`resolvePasteContext(source)` — 从 querySnapshot + revision + Tabulator ranges 解析 schemaRevision + selection.rowKeys + startCell
- `createGrid.ts`：`editable:false`（line 73）+ `clipboard:false`（line 126）是唯一阻塞点

### 1.4 明确不做（YAGNI / 范围外）

- **不动 contracts/bridge/.NET/Python/Directus**：所有接线纯前端。
- **不做乐观并发**：单单元格编辑 last-write-wins（宿主传 `expectedDateUpdated:null`）。未来要做需协调 .NET。
- **不做 multi_select 自定义对话框**：`editorFactory` 里 multi_select 是占位符（`vibetable_multi_select`），需要宿主对话框，纯前端做不了。本次 multi_select 列降级为只读。
- **不做 enum/relation/JSON editor**：后端只合成 4 种（checkbox/number/date/text），其余列只读。
- **不做 E2E 自动化**：手工冒烟。
- **applyPaste 的 updated 行撤销不完美**：spec §7.3 已说明，说明页已列。

## 2. 已确认的设计决策

| 决策点 | 选择 | 理由 |
|--------|------|------|
| 编辑提交策略 | **即时提交** | cellEdited 回调 → mutationService.updateCell；与 Directus 实时同步，简单 |
| 删除行确认 | **不弹确认，靠撤销** | 用户偏好流畅；要求 deleteRows 撤销必须可靠（前端缓存行快照） |
| 撤销范围 | **spec §7.3 原计划** | updateCell/insertRow 完全可逆；deleteRows 靠快照；applyPaste 只撤销 created |

## 3. 总体架构

复用 v2 的严格分层。新增 `mutationService`（唯一 mutation 出口）+ 扩展 `tableStore`（持有 editSchema + revision）+ 扩展 `useTabulator`（启用编辑 + cellEdited 回调）+ historyStore 生产者接线。

```
用户编辑单元格 / 按 Delete / Ctrl+V
  ↓
useTabulator (cellEdited 回调) / useKeyboard (onDelete/onPaste/onCopy)
  ↓
mutationService.updateCell / deleteRows / pasteService.apply
  ↓ (出站)
bridge.notify → .NET 宿主 → directus.* / PasteService
  ↓ (入站结果)
table.editCommitted / rowsInserted / rowsDeleted / pasteApplied
  ↓
mutationService 订阅 → tableStore 更新 + historyStore.push(撤销条目)
```

## 4. 数据结构扩展

### 4.1 tableStore 新增字段

```ts
// editSchema: table.editSchemaLoaded 的 payload（Editor union 每列）
editSchema: ColumnEditSchema[] | null   // 含每列的 Editor + editable + ValidationRule
// revision: 当前 MutationRevision（来自 DatasetReadyPayload.revision）
revision: MutationRevision | null        // 含 schemaRevision + dataRevision + databaseSessionId
```

`tableService.init()` 新增订阅 `table.editSchemaLoaded` → store.setEditSchema；`setDatasetReady` 已存 revision（DatasetReadyPayload extends TablePage，含 revision）。

### 4.2 mutationService

```ts
// services/mutationService.ts
export function useMutationService(): {
  init: () => void;
  updateCell: (rowKey, column, oldValue, newValue) => void;
  insertRow: (values) => void;
  deleteRows: (rows: {rowKey, expectedDigest}[]) => void;
}

// init() 订阅:
//   table.editCommitted → tableStore.applyCellEdit + historyStore.push(updateCell 撤销)
//   table.rowsInserted → tableStore.applyInsert + historyStore.push(insertRow 撤销)
//   table.rowsDeleted → tableStore.applyDelete + historyStore.push(deleteRows 撤销,含缓存的行快照)
```

### 4.3 historyStore 生产者（4 种 entry）

| kind | undo 操作 | redo 操作 | 数据需求 |
|------|-----------|-----------|---------|
| updateCell | 再发 updateCell(rowKey, column, newValue=oldValue) | 重发原 updateCell | oldValue + newValue（payload 已带） |
| insertRow | deleteRows([新 rowKey]) | 重发 insertRow(values) | 新 rowKey + 原 values |
| deleteRows | insertRow(缓存的行数据，逐行) | 重发 deleteRows | **删前缓存整行快照**（编辑触发时即捕获） |
| applyPaste | deleteRows([created 的 rowKeys]) | 重发 applyPaste(token) — 但 token 已消费，redo 可能失败 | created 的 rowKeys |

**deleteRows 撤销可靠性强化**（因用户选了"不弹确认"）：mutationService.deleteRows 在发送前，从 tableStore.allRows 按 rowKey 查出完整行数据，缓存到 historyEntry。撤销时按快照逐行 insertRow。若快照与当前 schemaRevision 不符（schema 变了），撤销失败并明确提示用户。

## 5. useTabulator 编辑启用

`createGrid` 的 `editable:false` 改为：按列 `ColumnSchema.editable` + `editSchema` 的 Editor 配 `editor`（via editorFactory.tabulatorEditor）。multi_select 列降级只读（无宿主对话框）。

新增 `cellEdited` 回调（Tabulator 选项）→ 调 `mutationService.updateCell`。注意 Tabulator 的 cellEdited 在用户提交编辑时触发（不是每次按键），匹配"即时提交"。

## 6. 快捷键接线（I2）

`App.vue` 的 useKeyboard 补全回调：
- `onCopy`：读 Tabulator `getRanges()` → 取选中单元格 → 拼 TSV → `navigator.clipboard.writeText`
- `onPaste`：`navigator.clipboard.readText` → `pasteService.preview`（需先 resolvePasteContext 拿 selection + schemaRevision）
- `onDelete`：读 Tabulator 选中行 → **不弹确认** → `mutationService.deleteRows`（撤销兜底）
- `onRefresh`：`tableService.refresh`
- `onNewTable`：`ui.openCreate` + `admin.openCreate`

copy/paste/delete 需要 Tabulator 实例引用——App.vue 通过 provide/inject 或提升 ref 到 WorkspaceView 共享 useTabulator 的 tabulator 实例。

## 7. 粘贴应用接线

PastePanel 的 `confirm` emit → WorkspaceView 处理：读 `pasteStore.plan.token` + `workspaceStore.currentTable` → `pasteService.apply({collection, token, idempotencyKey: crypto.randomUUID()})`。pasteService 已实现（Task 8），只是 WorkspaceView 没接 confirm。

## 8. 验收标准

### 8.1 功能（端到端，用户手工冒烟）
- 单元格编辑 → 提交 → 值更新（即时）
- 插入行 → 新行出现
- 选中行 + Delete → 删除（不弹确认）→ 撤销恢复
- Ctrl+C 复制选中 → Ctrl+V 粘贴 → 预览 → 确认 → 应用
- Ctrl+Z 撤销编辑/插入/删除 → 恢复；Ctrl+Shift+Z 重做
- Ctrl+R 刷新、Ctrl+N 新建表、? 帮助页
- 切换表/刷新/schema 变更 → 撤销栈清空

### 8.2 质量门禁
- 所有现有 250 测试不回归
- mutationService + 扩展的 tableStore/useTabulator 有单测
- historyStore 4 种 entry 的 push/undo/redo 有单测
- typecheck + build 绿
- 契约/bridge/.NET/Python 零改动

## 9. 风险

| 风险 | 对策 |
|------|------|
| deleteRows 撤销失败（schema 变更后快照不符）导致数据丢失（因不弹确认） | 撤销失败时明确 toast 提示"撤销失败，数据已删除"；考虑 schema 变更时不清空删除历史，但标记不可撤销 |
| Tabulator cellEdited 触发时序与 store 更新竞态 | mutationService 在 editCommitted 回调里更新 store，不依赖 cellEdited 的本地数据 |
| multi_select 列无法编辑 | 降级只读，说明页或 tooltip 提示 |
| applyPaste redo 因 token 已消费失败 | redo 时提示"无法重做粘贴，请重新粘贴" |
