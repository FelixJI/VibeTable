> 历史实施计划归档；不属于当前产品实现。

# web-grid Mutation 接线实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 在已完成的 web-grid-v2 上接线所有 mutation：单元格即时编辑、插入行、删除行（不弹确认靠撤销）、粘贴应用、copy/paste/delete/undo 快捷键、historyStore 4 种生产者。

**Architecture:** 严格分层不变。新增 `mutationService`（唯一 mutation 出口）+ 扩展 `tableStore`（editSchema + revision）+ 扩展 `useTabulator`（启用编辑 + cellEdited）+ App.vue 快捷键回调 + WorkspaceView 粘贴接线。

**Tech Stack:** Vue 3.5 + Pinia 2.3 + Tabulator 6.5.2 + 现有 editorFactory/pendingEdits/pasteContext（Task 2 已拷贝）。

**Reference spec:** `docs/superpowers/specs/2026-07-18-web-grid-mutation-wiring.md`

## Global Constraints

- **Node 24.18.0**，`engines.node: ">=24 <25"`。
- **不动 contracts/bridge/.NET/Python/Directus**：所有接线纯前端。contracts/index.ts 字节不变。
- **严格分层**：components emit（不调 service）；stores 不 import bridge；services 是唯一 bridge 调用方。
- **即时提交**：cellEdited → mutationService.updateCell（无批量）。
- **删除不弹确认**：靠撤销恢复；deleteRows 撤销必须缓存行快照。
- **撤销范围**（spec §7.3）：updateCell/insertRow 完全可逆；deleteRows 靠快照；applyPaste 只撤销 created。
- **multi_select 列降级只读**（无宿主对话框）。
- TypeScript strict + `verbatimModuleSyntax: true` + `@/` alias。
- 在 `feature/web-grid-v2` 分支上继续，每步提交。
- 现状 baseline：HEAD `2ccff0d`，250 测试全绿。

**关键现有 API（已核实，勿改）**：
- `bridge.on/notify/request`（hostBridge.ts，白名单已含所有 mutation 事件）
- `EditSchemaResult`（contracts:210）= `{table, schemaRevision, rowKeyKind, rowKeyStable, editable, columns: ColumnEditSchema[]}`；`ColumnEditSchema`（:191）= `{name, storageName, dataType, editable, nullable, primaryKey, editor: Editor, validation}`；`Editor`（:169）discriminated union
- `UpdateCellResult`（:227）= `{rowKey, column, storedValue, currentRow, revision: MutationRevision}`；`InsertRowResult`（:236）= `{rowKey, row, revision}`；`DeleteRowsResult`（:243）= `{deletedRowKeys, revision}`
- `MutationRevision`（:220）= `{databaseSessionId, schemaRevision, dataRevision}`
- 出站 payload：`UpdateCellRequestedPayload`（:269）= `{table, rowKey, column, oldValue, newValue, schemaRevision}`；`InsertRowRequestedPayload`（:279）= `{table, values, schemaRevision}`；`DeleteRowsRequestedPayload`（:292）= `{table, rows: DeleteRowRequestItem[], schemaRevision}`；`DeleteRowRequestItem`（:286）= `{rowKey, expectedDigest}`
- `tableStore`（Task 7）：`schema`, `allRows`, `setDatasetReady`, `appendPage`, `setError`, `reset`；需扩展
- `historyStore`（Task 11）：`push(entry)`, `undo()`, `redo()`, `clear()`, `canUndo/canRedo`；`HistoryEntry` = `{id, kind: HistoryEntryKind, label, timestamp, undo: ()=>Promise<void>, redo: ()=>Promise<void>}`
- `pasteService`（Task 8）：`preview(payload)`, `apply({collection, token, idempotencyKey})`
- `resolvePasteContext`（grid/pasteContext.ts）：从 `{grid, columns, querySnapshot, revision}` 解析 `{schemaRevision, editableColumns, selection, startCell}`

---

## File Structure

```
desktop/web-grid/src/
├── stores/
│   ├── tableStore.ts                 # Task M1: +editSchema, +revision, +applyEdit/Insert/Delete
│   └── historyStore.ts               # (unchanged; producers in mutationService)
├── services/
│   ├── mutationService.ts            # Task M2: new — updateCell/insertRow/deleteRows + inbound wiring + history push
│   └── tableService.ts               # Task M1: +subscribe table.editSchemaLoaded
├── grid/
│   ├── createGrid.ts                 # Task M3: editable per-column + cellEdited hook option
│   └── editorFactory.ts              # (already copied; consumed by createGrid)
├── composables/
│   └── useTabulator.ts               # Task M3: pass editSchema + onCellEdited; Task M5: expose tabulator instance for clipboard
├── views/
│   └── WorkspaceView.vue             # Task M4: wire paste confirm; provide tabulator ref
└── App.vue                           # Task M5: wire onCopy/onPaste/onDelete/onRefresh/onNewTable
```

---

### Task M1: 扩展 tableStore（editSchema + revision + 应用变更）

**Files:**
- Modify: `desktop/web-grid/src/stores/tableStore.ts`
- Modify: `desktop/web-grid/src/stores/tableStore.test.ts`
- Modify: `desktop/web-grid/src/services/tableService.ts`

**Interfaces:**
- Produces: tableStore 新增 `editSchema: ColumnEditSchema[] | null`、`revision: MutationRevision | null`；actions `setEditSchema(cols, rev)`、`applyCellEdit(result: UpdateCellResult)`、`applyInsert(result: InsertRowResult)`、`applyDelete(result: DeleteRowsResult)`、`snapshotRows(rowKeys): Record<string,unknown>[]`（供撤销缓存）。

- [ ] **Step 1: 写失败测试（追加到 tableStore.test.ts）**

```ts
import type { ColumnEditSchema, MutationRevision, UpdateCellResult, InsertRowResult, DeleteRowsResult } from "@/contracts";

const editSchema: ColumnEditSchema[] = [
  { name: "id", storageName: "id", dataType: "integer", editable: false, nullable: false, primaryKey: true,
    editor: { kind: "number", storage: "integer" }, validation: [] },
  { name: "name", storageName: "name", dataType: "text", editable: true, nullable: true, primaryKey: false,
    editor: { kind: "text" }, validation: [] },
] as readonly ColumnEditSchema[];

describe("tableStore mutation extensions", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("setEditSchema stores columns + revision", () => {
    const s = useTableStore();
    const rev = { databaseSessionId: "s", schemaRevision: "sr1", dataRevision: 1 };
    s.setEditSchema(editSchema, rev);
    expect(s.editSchema).toHaveLength(2);
    expect(s.revision?.schemaRevision).toBe("sr1");
  });

  it("applyCellEdit updates the edited cell in allRows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "old" }]));
    const res: UpdateCellResult = { rowKey: 1, column: "name", storedValue: "new",
      currentRow: { rowKey: 1, name: "new" }, revision: { databaseSessionId:"s", schemaRevision:"sr", dataRevision:2 } };
    s.applyCellEdit(res);
    expect(s.allRows[0].name).toBe("new");
    expect(s.revision?.dataRevision).toBe(2);
  });

  it("applyInsert appends the new row", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1 }]));
    s.applyInsert({ rowKey: 2, row: { rowKey: 2, name: "x" }, revision: { databaseSessionId:"s", schemaRevision:"sr", dataRevision:2 } });
    expect(s.allRows).toHaveLength(2);
    expect(s.allRows[1].rowKey).toBe(2);
  });

  it("applyDelete removes the deleted rows", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1 }, { rowKey: 2 }, { rowKey: 3 }]));
    s.applyDelete({ deletedRowKeys: [1, 3], revision: { databaseSessionId:"s", schemaRevision:"sr", dataRevision:2 } });
    expect(s.allRows).toHaveLength(1);
    expect(s.allRows[0].rowKey).toBe(2);
  });

  it("snapshotRows returns full row data for given keys (for undo cache)", () => {
    const s = useTableStore();
    s.beginLoad();
    s.appendPage(makePage([{ rowKey: 1, name: "a" }, { rowKey: 2, name: "b" }]));
    const snap = s.snapshotRows([2]);
    expect(snap).toEqual([{ rowKey: 2, name: "b" }]);
  });

  it("reset also clears editSchema + revision", () => {
    const s = useTableStore();
    s.setEditSchema(editSchema, { databaseSessionId:"s", schemaRevision:"sr", dataRevision:1 });
    s.reset();
    expect(s.editSchema).toBeNull();
    expect(s.revision).toBeNull();
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run: `cd desktop/web-grid && npm test -- tableStore`
Expected: 6 new tests FAIL (setEditSchema/applyCellEdit/etc undefined).

- [ ] **Step 3: 实现 tableStore 扩展**

在 `stores/tableStore.ts` 增加：
```ts
import type { ColumnEditSchema, MutationRevision, UpdateCellResult, InsertRowResult, DeleteRowsResult } from "@/contracts";

// 在 setup store 内新增 state：
const editSchema = ref<readonly ColumnEditSchema[] | null>(null);
const revision = ref<MutationRevision | null>(null);

// 新 actions：
function setEditSchema(cols: readonly ColumnEditSchema[], rev: MutationRevision): void {
  editSchema.value = cols;
  revision.value = rev;
}

function applyCellEdit(result: UpdateCellResult): void {
  // 在 pages 的最后一页里就地更新该行该列（allRows 是 getter，改 pages 的行对象）
  for (const page of pages.value) {
    for (const row of page.rows as Record<string, unknown>[]) {
      if (row.rowKey === result.rowKey) {
        row[result.column] = result.storedValue;
        // 合并 currentRow 的其他字段（宿主可能回传整行）
        Object.assign(row, result.currentRow, { [result.column]: result.storedValue });
      }
    }
  }
  revision.value = result.revision;
}

function applyInsert(result: InsertRowResult): void {
  // 追加到最后一页（或新建一页）。简化：追加到最后一页的 rows。
  const last = pages.value[pages.value.length - 1];
  if (last) {
    (last.rows as Record<string, unknown>[]).push(result.row);
  }
  revision.value = result.revision;
}

function applyDelete(result: DeleteRowsResult): void {
  const dead = new Set(result.deletedRowKeys);
  for (const page of pages.value) {
    const rows = page.rows as Record<string, unknown>[];
    // 就地过滤（保留同一 page 对象引用以保持响应式）
    const kept = rows.filter((r) => !dead.has(r.rowKey));
    rows.length = 0;
    rows.push(...kept);
  }
  revision.value = result.revision;
}

function snapshotRows(rowKeys: readonly (number|string)[]): Record<string, unknown>[] {
  const want = new Set(rowKeys);
  const out: Record<string, unknown>[] = [];
  for (const page of pages.value) {
    for (const row of page.rows as Record<string, unknown>[]) {
      if (want.has(row.rowKey)) out.push({ ...row });
    }
  }
  return out;
}
```
在 `reset()` 里加 `editSchema.value = null; revision.value = null;`。在 `return { ... }` 里导出全部新增 state + actions。

- [ ] **Step 4: 运行测试验证通过**

Run: `npm test -- tableStore`
Expected: 全部 PASS（原 6 + 新 6 = 12）。

- [ ] **Step 5: tableService 订阅 editSchemaLoaded**

在 `services/tableService.ts` 的 `init()` 里加：
```ts
import type { EditSchemaResult } from "@/contracts";
// ...
bridge.on("table.editSchemaLoaded", (payload: EditSchemaResult) => {
  tableStore.setEditSchema(payload.columns, { databaseSessionId: "", schemaRevision: payload.schemaRevision, dataRevision: 0 });
  // dataRevision/databaseSessionId 会在 datasetReady 时被覆盖；这里先存 schemaRevision
});
```
（注意：`EditSchemaResult` 只有 schemaRevision，完整 MutationRevision 在 datasetReady 到。setEditSchema 先用占位 databaseSessionId/dataRevision，datasetReady 的 revision 到了再覆盖——在 setDatasetReady 里确保也写 revision。）

在 `setDatasetReady` 里加：`revision.value = payload.revision ?? revision.value;`（DatasetReadyPayload extends TablePage 含 revision?）。

- [ ] **Step 6: 验证 typecheck + 全套测试**

Run: `cd desktop/web-grid && npm run typecheck && npm test`
Expected: typecheck 0；250+6=256 全过。

- [ ] **Step 7: 提交**

```bash
git add -A && git commit -m "feat(web-grid): extend tableStore (editSchema + revision + applyEdit/Insert/Delete)"
```

---

### Task M2: mutationService（出站 + 入站 + historyStore 生产者）

**Files:**
- Create: `desktop/web-grid/src/services/mutationService.ts`
- Create: `desktop/web-grid/src/services/mutationService.test.ts`

**Interfaces:**
- Consumes: `useHostBridge`、`useTableStore`、`useWorkspaceStore`、`useHistoryStore`、contracts mutation types、`crypto.randomUUID`
- Produces: `useMutationService()` = `{init, updateCell, insertRow, deleteRows}`

- [ ] **Step 1: 写失败测试**

```ts
// services/mutationService.test.ts
import { describe, it, expect, beforeEach, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { createHostBridge, type HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";
import { useMutationService } from "./mutationService";

function makeShimBridge(): { bridge: HostBridge; emit: (type: string, payload: unknown) => void } {
  let listener: ((e: { data: unknown }) => void) | null = null;
  const shim = {
    addEventListener: (_: string, fn: (e: { data: unknown }) => void) => { listener = fn; },
    removeEventListener: (_: string, fn: (e: { data: unknown }) => void) => { if (listener === fn) listener = null; },
    postMessage: () => {},
  };
  const bridge = createHostBridge({ webview: shim });
  bridge.start();
  return { bridge, emit: (type, payload) => listener?.({ data: JSON.stringify({ type, payload }) }) };
}

describe("mutationService", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("updateCell notifies table.updateCellRequested with schemaRevision", () => {
    const { bridge } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const spy = vi.spyOn(bridge, "notify");
    const table = useTableStore();
    const ws = useWorkspaceStore();
    ws.selectTable("users");
    table.setEditSchema([], { databaseSessionId:"s", schemaRevision:"sr1", dataRevision:1 });
    const svc = useMutationService();
    svc.updateCell(5, "name", "old", "new");
    expect(spy).toHaveBeenCalledWith("table.updateCellRequested",
      { table: "users", rowKey: 5, column: "name", oldValue: "old", newValue: "new", schemaRevision: "sr1" });
  });

  it("on editCommitted, applies edit + pushes undoable history entry", async () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({ table:"u", columns:[], rows:[{rowKey:1,name:"old"}], offset:0, limit:1, totalRows:1, mode:"client" } as never);
    const history = useHistoryStore();
    const svc = useMutationService();
    svc.init();
    emit("table.editCommitted", { rowKey:1, column:"name", storedValue:"new", currentRow:{rowKey:1,name:"new"}, revision:{databaseSessionId:"s",schemaRevision:"sr",dataRevision:2} });
    expect(table.allRows[0].name).toBe("new");
    expect(history.canUndo).toBe(true);
  });

  it("on rowsDeleted, applies delete + pushes history with cached snapshot", async () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore();
    table.beginLoad();
    table.appendPage({ table:"u", columns:[], rows:[{rowKey:1,name:"a"},{rowKey:2,name:"b"}], offset:0, limit:2, totalRows:2, mode:"client" } as never);
    const history = useHistoryStore();
    const ws = useWorkspaceStore(); ws.selectTable("u");
    table.setEditSchema([], { databaseSessionId:"s", schemaRevision:"sr", dataRevision:1 });
    const svc = useMutationService();
    // deleteRows 先缓存快照再 notify
    svc.deleteRows([{ rowKey: 2, expectedDigest: "d" }]);
    emit("table.rowsDeleted", { deletedRowKeys:[2], revision:{databaseSessionId:"s",schemaRevision:"sr",dataRevision:2} });
    expect(table.allRows).toHaveLength(1);
    expect(history.canUndo).toBe(true);
    // undo 恢复
    await history.undo();
    expect(table.allRows).toHaveLength(2);
  });

  it("clears history on schema change (revision.schemaRevision differs)", () => {
    const { bridge, emit } = makeShimBridge();
    setHostBridgeForTesting(bridge);
    const table = useTableStore(); table.beginLoad();
    table.appendPage({ table:"u", columns:[], rows:[{rowKey:1}], offset:0, limit:1, totalRows:1, mode:"client" } as never);
    const history = useHistoryStore();
    const svc = useMutationService(); svc.init();
    emit("table.editCommitted", { rowKey:1, column:"x", storedValue:1, currentRow:{rowKey:1,x:1}, revision:{databaseSessionId:"s",schemaRevision:"sr",dataRevision:2} });
    expect(history.canUndo).toBe(true);
    // schema 变了
    emit("table.editCommitted", { rowKey:1, column:"x", storedValue:2, currentRow:{rowKey:1,x:2}, revision:{databaseSessionId:"s",schemaRevision:"CHANGED",dataRevision:3} });
    expect(history.canUndo).toBe(false);
  });
});
```

- [ ] **Step 2: 运行测试验证失败**

Run: `npm test -- mutationService`
Expected: FAIL（模块不存在）。

- [ ] **Step 3: 实现 mutationService**

```ts
// services/mutationService.ts
import { useHostBridge } from "./bridgeContext";
import { useTableStore } from "@/stores/tableStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import { useHistoryStore } from "@/stores/historyStore";
import type {
  UpdateCellResult, InsertRowResult, DeleteRowsResult, MutationRevision,
} from "@/contracts";

export function useMutationService() {
  const bridge = useHostBridge();
  const table = useTableStore();
  const ws = useWorkspaceStore();
  const history = useHistoryStore();
  let pendingDeleteSnapshot: Map<string, Record<string, unknown>[]> = new Map();

  function currentSchemaRev(): string {
    return table.revision?.schemaRevision ?? "";
  }

  function init(): void {
    let lastSchemaRev = table.revision?.schemaRevision ?? null;

    bridge.on("table.editCommitted", (r: UpdateCellResult) => {
      const oldVal = findOldValue(r.rowKey, r.column);
      table.applyCellEdit(r);
      maybeClearHistory(r.revision.schemaRevision, () => { lastSchemaRev = r.revision.schemaRevision; });
      history.push({
        id: crypto.randomUUID(), kind: "updateCell",
        label: `编辑 ${r.column}`, timestamp: Date.now(),
        undo: async () => { bridge.notify("table.updateCellRequested",
          { table: ws.currentTable ?? "", rowKey: r.rowKey, column: r.column, oldValue: r.storedValue, newValue: oldVal, schemaRevision: currentSchemaRev() }); },
        redo: async () => { bridge.notify("table.updateCellRequested",
          { table: ws.currentTable ?? "", rowKey: r.rowKey, column: r.column, oldValue: oldVal, newValue: r.storedValue, schemaRevision: currentSchemaRev() }); },
      });
    });

    bridge.on("table.rowsInserted", (r: InsertRowResult) => {
      table.applyInsert(r);
      maybeClearHistory(r.revision.schemaRevision, () => { lastSchemaRev = r.revision.schemaRevision; });
      history.push({
        id: crypto.randomUUID(), kind: "insertRow",
        label: "插入行", timestamp: Date.now(),
        undo: async () => { bridge.notify("table.deleteRowsRequested",
          { table: ws.currentTable ?? "", rows: [{ rowKey: r.rowKey, expectedDigest: "" }], schemaRevision: currentSchemaRev() }); },
        redo: async () => { bridge.notify("table.insertRowRequested",
          { table: ws.currentTable ?? "", values: r.row, schemaRevision: currentSchemaRev() }); },
      });
    });

    bridge.on("table.rowsDeleted", (r: DeleteRowsResult) => {
      table.applyDelete(r);
      maybeClearHistory(r.revision.schemaRevision, () => { lastSchemaRev = r.revision.schemaRevision; });
      const snapshot = pendingDeleteSnapshot.get(JSON.stringify(r.deletedRowKeys)) ?? [];
      pendingDeleteSnapshot.delete(JSON.stringify(r.deletedRowKeys));
      history.push({
        id: crypto.randomUUID(), kind: "deleteRows",
        label: `删除 ${r.deletedRowKeys.length} 行`, timestamp: Date.now(),
        undo: async () => {
          for (const row of snapshot) {
            bridge.notify("table.insertRowRequested",
              { table: ws.currentTable ?? "", values: row, schemaRevision: currentSchemaRev() });
          }
        },
        redo: async () => { bridge.notify("table.deleteRowsRequested",
          { table: ws.currentTable ?? "", rows: r.deletedRowKeys.map(k => ({ rowKey: k, expectedDigest: "" })), schemaRevision: currentSchemaRev() }); },
      });
    });
  }

  function maybeClearHistory(newSchemaRev: string, onKeep: () => void): void {
    const prev = table.revision?.schemaRevision ?? null;
    if (prev && prev !== newSchemaRev) { history.clear(); }
    else { onKeep(); }
  }

  function findOldValue(rowKey: number|string, column: string): unknown {
    for (const row of table.allRows) {
      if (row.rowKey === rowKey) return row[column];
    }
    return undefined;
  }

  function updateCell(rowKey: number|string, column: string, oldValue: unknown, newValue: unknown): void {
    bridge.notify("table.updateCellRequested",
      { table: ws.currentTable ?? "", rowKey, column, oldValue, newValue, schemaRevision: currentSchemaRev() });
  }

  function insertRow(values: Record<string, unknown>): void {
    bridge.notify("table.insertRowRequested",
      { table: ws.currentTable ?? "", values, schemaRevision: currentSchemaRev() });
  }

  function deleteRows(rows: { rowKey: number|string; expectedDigest: string }[]): void {
    // 缓存快照供撤销（在发送前，数据还在 store）
    const keys = rows.map(r => r.rowKey);
    const snapshot = table.snapshotRows(keys);
    pendingDeleteSnapshot.set(JSON.stringify(keys), snapshot);
    bridge.notify("table.deleteRowsRequested",
      { table: ws.currentTable ?? "", rows, schemaRevision: currentSchemaRev() });
  }

  return { init, updateCell, insertRow, deleteRows };
}
```

注意 `maybeClearHistory` 的逻辑：先检查 table.revision 的 schemaRevision 与新结果的 schemaRevision 是否不同——不同则清空历史。但要注意顺序：applyCellEdit 会更新 revision，所以要在 apply 之前读旧 schemaRevision。调整：先存 `prev = table.revision?.schemaRevision`，再 apply，再比较。实现时按此顺序。

- [ ] **Step 4: 运行测试验证通过**

Run: `npm test -- mutationService`
Expected: 4 PASS。若 schema-clear 测试失败，调整 maybeClearHistory 的读取顺序。

- [ ] **Step 5: 验证 typecheck + 全套**

Run: `npm run typecheck && npm test`
Expected: typecheck 0；256+4=260。

- [ ] **Step 6: 提交**

```bash
git add -A && git commit -m "feat(web-grid): add mutationService (updateCell/insertRow/deleteRows + history producers)"
```

---

### Task M3: createGrid 启用编辑 + useTabulator cellEdited

**Files:**
- Modify: `desktop/web-grid/src/grid/createGrid.ts`
- Modify: `desktop/web-grid/src/composables/useTabulator.ts`
- Create: `desktop/web-grid/src/grid/createGrid.test.ts`（如已有则追加）或独立测试

**Interfaces:**
- Consumes: `ColumnEditSchema` + `editorFactory.tabulatorEditor`；`useTabulator` 接收 `onCellEdited` 回调
- Produces: createGrid 新签名接受 `editSchema` 选项；列按 editor 配 editor；cellEdited 回调

- [ ] **Step 1: 修改 createGrid 接受 editSchema**

`buildColumns` 改为可接受可选 `editSchema`：
```ts
// grid/createGrid.ts
import { tabulatorEditor } from "./editorFactory";
import type { ColumnEditSchema } from "@/contracts";

export function buildColumns(page: TablePage, editSchema?: readonly ColumnEditSchema[] | null): GridColumnDefinition[] {
  const editByName = new Map((editSchema ?? []).map(c => [c.name, c]));
  return page.columns.map((col) => {
    const edit = editByName.get(col.name);
    const editable = !!edit?.editable && edit.editor.kind !== "multi_select"; // multi_select 降级只读
    const def = toColumnDef(col); // 保持现有 formatter 逻辑
    if (editable && edit) {
      const ed = tabulatorEditor(edit.editor);
      return { ...def, editable: true, editor: ed.editor, ...(ed.editorParams ? { editorParams: ed.editorParams } : {}) };
    }
    return { ...def, editable: false };
  });
}
```
注意 `GridColumnDefinition` 可能需扩展允许 `editor`/`editorParams` 字段——读现有类型定义，按需扩展（它已是 `{field,title,editable,dataType,formatter?,formatterParams?}`，加可选 `editor?`、`editorParams?`）。

`buildOptions` 改为接受 `onCellEdited` 回调并设 `editable:true`（让 Tabulator 按列级 editable 判断）+ 注册 `cellEdited` 回调：
```ts
export function buildOptions(page: TablePage, opts?: { editSchema?: readonly ColumnEditSchema[] | null; onCellEdited?: (rowKey, column, oldValue, newValue) => void }): TabulatorOptions {
  // ...
  const options: TabulatorOptions = {
    columns: buildColumns(page, opts?.editSchema) as unknown[],
    data,
    layout: "fitColumns",
    selectableRange: true,
    // 编辑启用：Tabulator 按列 editable 判断；cellEdited 回调提交
    cellEdited: (cell) => {
      const row = cell.getRow().getData() as Record<string, unknown>;
      const column = cell.getField();
      const newValue = cell.getValue();
      // oldValue 在 cellEdited 触发时已被 Tabulator 覆盖；需要从 oldValue 缓存拿。
      // 见 Step 2 的 oldValue 捕获方案。
      opts?.onCellEdited?.(row.rowKey, column, undefined, newValue);
    },
    // 移除 clipboard:false，但保持粘贴走自定义（Task M5 的 onPaste）
    // clipboard 仍可 false（我们用 navigator.clipboard 自己处理 copy/paste）
    clipboard: false,
    // ...
  };
}
```
`createGrid(element, page, opts?)` 透传 opts。

- [ ] **Step 2: oldValue 捕获（cellEdited 时 Tabulator 已覆盖旧值）**

cellEdited 回调里 oldValue 拿不到。方案：在 `cellEditing` 回调（编辑开始前）缓存 `(rowKey:column) -> oldValue`，cellEdited 时取出。实现一个 module 级 Map：
```ts
const editingOldValues = new Map<string, unknown>();
// buildOptions 里加:
cellEditing: (cell) => {
  const row = cell.getRow().getData() as Record<string, unknown>;
  editingOldValues.set(`${row.rowKey}:${cell.getField()}`, cell.getValue());
},
cellEdited: (cell) => {
  const row = cell.getRow().getData() as Record<string, unknown>;
  const key = `${row.rowKey}:${cell.getField()}`;
  const oldValue = editingOldValues.get(key);
  editingOldValues.delete(key);
  opts?.onCellEdited?.(row.rowKey, cell.getField(), oldValue, cell.getValue());
},
```
（若 Tabulator 类型 shim 不含 cellEditing/cellEdited，扩展 env.d.ts 的 TabulatorOptions。）

- [ ] **Step 3: useTabulator 传 editSchema + onCellEdited**

```ts
// composables/useTabulator.ts
export function useTabulator(gridEl, options?: { onCellEdited?: (rowKey, column, oldValue, newValue) => void }) {
  // ...
  // 初始化时用当前 editSchema：
  watch([() => gridEl.value, () => store.pages, () => store.editSchema], ([el, pages]) => {
    if (!el || tabulator.value || pages.length === 0) return;
    tabulator.value = createGrid(el, pages[0], { editSchema: store.editSchema, onCellEdited: options?.onCellEdited });
  }, { immediate: true });
  // editSchema 变化时重建列（setColumns + 新 editor）：
  watch(() => store.editSchema, () => {
    if (tabulator.value && store.pages[0]) {
      try { tabulator.value.setColumns(buildColumns(store.pages[0], store.editSchema) as unknown[]); }
      catch { /* fallback */ }
    }
  });
}
```
暴露 `tabulator` ref（供 Task M5 clipboard 用）。

- [ ] **Step 4: WorkspaceView 接 onCellEdited → mutationService.updateCell**

在 WorkspaceView.vue 里 useTabulator 传 `onCellEdited: (rk, col, old, nw) => mutationService.updateCell(rk, col, old, nw)`。mutationService 在 WorkspaceView init。

- [ ] **Step 5: 测试 + 提交**

写一个 createGrid 测试：给定 editSchema，buildColumns 返回的列含 editor。验证 multi_select 列 editable=false。
Run: `npm test && npm run typecheck && npm run build`
Expected: 全过，build 成功。
```bash
git add -A && git commit -m "feat(web-grid): enable cell editing (per-column editor + cellEdited→mutation)"
```

---

### Task M4: 粘贴应用接线 + WorkspaceView 集成 mutationService

**Files:**
- Modify: `desktop/web-grid/src/views/WorkspaceView.vue`

- [ ] **Step 1: WorkspaceView init mutationService**

在 `onMounted` 加 `mutationService.init()`（和其他 service.init 一起）。import useMutationService。

- [ ] **Step 2: PastePanel confirm → pasteService.apply**

WorkspaceView 的 PastePanel `@confirm` handler：
```ts
async function onPasteConfirm() {
  const plan = pasteStore.plan;
  const table = workspaceStore.currentTable;
  if (!plan || !table) return;
  const token = (plan.token as { token?: string } | undefined)?.token;
  if (!token) return;
  pasteService.apply({ collection: table, token, idempotencyKey: crypto.randomUUID() });
}
```
绑定 `<PastePanel @confirm="onPasteConfirm" ... />`。

- [ ] **Step 3: 测试更新**

WorkspaceView.test.ts 加一个用例：PastePanel emit confirm → 桥收到 `table.applyPasteRequested`（用 shim bridge 验证 notify 调用 + idempotencyKey 非空）。

- [ ] **Step 4: 验证 + 提交**

Run: `npm test && npm run typecheck`
```bash
git add -A && git commit -m "feat(web-grid): wire paste apply (confirm→pasteService.apply) + mutationService init"
```

---

### Task M5: 快捷键接线（copy/paste/delete/refresh/newTable）+ tabulator 实例共享

**Files:**
- Modify: `desktop/web-grid/src/views/WorkspaceView.vue`（共享 tabulator ref via provide）
- Modify: `desktop/web-grid/src/App.vue`（useKeyboard 回调）

- [ ] **Step 1: WorkspaceView provide tabulator 实例**

useTabulator 返回 tabulator ref。WorkspaceView 用 `provide('tabulator', tabulator)` 共享给 App.vue（inject）。或更简单：把 useKeyboard 的回调从 App.vue 移到 WorkspaceView（WorkspaceView 有 tabulator + 所有 service）。**采用后者**：在 WorkspaceView 调 useKeyboard（而非 App.vue），传完整回调。

注意 useKeyboard 当前在 App.vue。迁移到 WorkspaceView：移除 App.vue 的 useKeyboard 调用，在 WorkspaceView 加。但 useTheme 留 App.vue（NConfigProvider 需要）。

- [ ] **Step 2: 实现 onCopy/onPaste/onDelete**

```ts
// WorkspaceView.vue
const onCopy = () => {
  const ranges = tabulator.value?.getRanges() ?? [];
  const range = ranges.at(-1);
  if (!range) return;
  const rows = range.getRows();
  const cols = range.getColumns();
  const tsv = rows.map(r => cols.map(c => String(r.getData()[c.getField()] ?? "")).join("\t")).join("\n");
  navigator.clipboard?.writeText(tsv);
};
const onPaste = async () => {
  const text = await navigator.clipboard?.readText();
  if (!text || !workspaceStore.currentTable) return;
  // resolvePasteContext 需要 querySnapshot + revision；从 tableStore 拿
  const ctx = resolvePasteContext({
    grid: tabulator.value,
    columns: tableStore.schema ?? [],
    querySnapshot: (tableStore.pages[0] as any)?.querySnapshot ?? null,
    revision: tableStore.revision,
  });
  pasteService.preview({ /* PreviewPasteRequestedPayload 形状，用 ctx 构建 */ });
};
const onDelete = () => {
  const ranges = tabulator.value?.getRanges() ?? [];
  const range = ranges.at(-1);
  if (!range) return;
  const rows = range.getRows().map(r => ({ rowKey: r.getData().rowKey, expectedDigest: String(r.getData().rowKey) }));
  mutationService.deleteRows(rows);  // 不弹确认，靠撤销
};
useKeyboard({ onCopy, onPaste, onDelete, onRefresh: () => tableService.refresh(), onNewTable: () => { admin.openCreate(); ui.openCreate(); } });
```
注意：`PreviewPasteRequestedPayload` 的真实形状需读 contracts（含 selection/startCell/cells/schemaRevision）——onPaste 里用 resolvePasteContext 的结果 + clipboardParser 构建完整 payload。若太复杂，先做简化版（直接传 clipboardText + table，让后端解析）——查 contracts 确认 PreviewPasteRequestedPayload 是接受原始 clipboardText 还是已解析 cells。

- [ ] **Step 3: 删除历史 schemaRevision 清空已由 mutationService 处理；切换表/刷新清空**

在 tableStore.reset() 或 tableService.selectTable 里调 historyStore.clear()。在 WorkspaceView 的 selectTable handler 里加 `history.clear()`。

- [ ] **Step 4: 测试 + 提交**

Run: `npm test && npm run typecheck && npm run build`
验证：快捷键测试（useKeyboard.test.ts）仍过；新增 WorkspaceView 集成测试覆盖 onDelete/onCopy 的桥调用（用 shim bridge）。
```bash
git add -A && git commit -m "feat(web-grid): wire copy/paste/delete/refresh/newTable shortcuts (no-confirm delete, undo-backed)"
```

---

### Task M6: 端到端冒烟准备 + 收尾

**Files:**
- Read-only verification + 可能的小修

- [ ] **Step 1: 架构债复查**

```bash
cd desktop/web-grid
npm run typecheck && npm test && npm run build
grep -rn "from.*@/services/" src/components/ && echo "FAIL" || echo "OK: components pure"
grep -rn "from.*@/bridge/" src/stores/ && echo "FAIL" || echo "OK: stores via services"
wc -l src/main.ts
```

- [ ] **Step 2: 记录已知限制到说明页/账本**

确认 ShortcutsView 的撤销限制说明仍准确（spec §7.3）。在 progress ledger 记录：multi_select 只读、单单元格无乐观并发、删除非原子、applyPaste updated 行不可完美撤销。

- [ ] **Step 3: 提交最终状态**

```bash
git add -A && git commit -m "chore(web-grid): mutation wiring complete (cell edit/insert/delete/paste/undo/shortcuts)"
```

---

## Self-Review

### 1. Spec coverage
- §3 mutationService + tableStore 扩展：M1, M2 ✓
- §4 historyStore 4 生产者：M2 ✓
- §5 createGrid 编辑启用：M3 ✓
- §6 快捷键接线：M5 ✓
- §7 粘贴应用：M4 ✓
- §8 验收：M6 + 各 task 测试 ✓

### 2. Placeholder scan
- M5 onPaste 的 PreviewPasteRequestedPayload 构建标注"查 contracts 确认形状"——这是必要的运行时核对（类似 Task 6-9 的契约适配），不是占位符。
- M3 cellEditing/cellEdited 若 Tabulator 类型 shim 不支持需扩展 env.d.ts——已说明。

### 3. Type consistency
- `useMutationService().{init,updateCell,insertRow,deleteRows}` 签名一致 ✓
- `historyStore.push(entry)` 签名沿用 Task 11 ✓
- `tableStore.{editSchema,revision,applyCellEdit,applyInsert,applyDelete,snapshotRows}` M1 定义、M2 使用 ✓

---

## 执行选择

Plan saved to `docs/superpowers/plans/2026-07-18-web-grid-mutation-wiring.md`. 用 Subagent-Driven 执行（6 个任务）。
