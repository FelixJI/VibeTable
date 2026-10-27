import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PastePlan } from "@/contracts";
import type { PasteContext } from "@/grid/pasteContext";
import { PASTE_CELL_LIMIT } from "@/grid/clipboardParser";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import {
  createWorkspaceTableInteractionController,
  type ClipboardCommandScope,
  type TableInteractionGridPort,
  type WorkspaceTableInteractionDependencies,
} from "./workspaceTableInteractionController";

function setup(
  range: ReturnType<TableInteractionGridPort["readActiveRange"]>,
  overrides: Partial<Pick<WorkspaceTableInteractionDependencies,
    "clipboardGuard" | "reportClipboardFailure" | "canExecuteGridCommand">> = {},
) {
  const deleteRows = vi.fn();
  const duplicateTable = vi.fn(async () => {});
  const reportClipboardFailure = vi.fn();
  const pasteService: WorkspaceTableInteractionDependencies["pasteService"] = {
    preview: vi.fn(),
    apply: vi.fn(),
  };
  const mutationService: WorkspaceTableInteractionDependencies["mutationService"] = {
    deleteRows,
  };
  const grid: TableInteractionGridPort = {
    readActiveRange: () => range,
    resolvePasteContext: vi.fn(() => { throw new Error("no paste context"); }),
    selectAll: vi.fn(),
    editActiveCell: vi.fn(),
  };
  let scopeCurrent = true;
  const captured: ClipboardCommandScope[] = [];
  const clipboardGuard: WorkspaceTableInteractionDependencies["clipboardGuard"] = {
    capture: () => {
      const scope: ClipboardCommandScope = {
        workspaceId: "ws-1",
        sessionEpoch: 1,
        tableId: "orders",
        generation: 1,
      };
      captured.push(scope);
      return scope;
    },
    isCurrent: () => scopeCurrent,
  };
  const dependencies: WorkspaceTableInteractionDependencies = {
    workspace: useWorkspaceStore(),
    table: useTableStore(),
    ui: useUiStore(),
    admin: useTableAdminStore(),
    paste: usePasteStore(),
    pasteService,
    mutationService,
    tableAdminService: { createTable: vi.fn(), deleteTable: vi.fn(), duplicateTable },
    grid,
    readClipboard: vi.fn(async () => ""),
    writeClipboard: vi.fn(async () => undefined),
    createId: () => "operation-1",
    clipboardGuard,
    reportClipboardFailure,
    canExecuteGridCommand: () => true,
    ...overrides,
  };
  return {
    controller: createWorkspaceTableInteractionController(dependencies),
    dependencies,
    deleteRows,
    duplicateTable,
    grid,
    pasteService,
    reportClipboardFailure,
    setScopeCurrent: (value: boolean) => { scopeCurrent = value; },
    captured,
    tableAdminService: dependencies.tableAdminService,
  };
}

describe("workspaceTableInteractionController", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("复制时只通过 clipboard seam 写出当前 range 的 TSV", async () => {
    const range = {
      rows: [{ title: "任务", status: "进行中" }],
      fields: ["title", "status"],
    };
    const { controller, dependencies } = setup(range);

    await controller.dispatch({ type: "keyboard.copy" });

    expect(dependencies.writeClipboard).toHaveBeenCalledWith("任务\t进行中");
  });

  it("复制对含引号/制表符/换行的单元格做 TSV 引号编码并保留原值", async () => {
    const range = {
      rows: [{
        a: '含"引号"',
        b: "制表\t内嵌",
        c: "多\n行\r\n值",
        d: 0,
        e: false,
        f: "007",
        g: null,
      }],
      fields: ["a", "b", "c", "d", "e", "f", "g"],
    };
    const { controller, dependencies } = setup(range);

    await controller.dispatch({ type: "menu.copy" });

    expect(dependencies.writeClipboard).toHaveBeenCalledWith(
      '"含""引号"""\t"制表\t内嵌"\t"多\n行\r\n值"\t0\tfalse\t007\t',
    );
  });

  it("复制超出单元格上限时整体失败并指向文件导出", async () => {
    const rows = Array.from({ length: 101 }, () => ({
      title: "x",
      status: "y",
      extra: "z",
      more: "w",
      last: "v",
      tail: "u",
      plus: "t",
      extra2: "s",
      nine: "r",
      ten: "q",
      // 101 rows x 100 fields > 10 000 cells.
    }));
    const fields = Array.from({ length: 100 }, (_, index) => `f${index}`);
    const range = { rows, fields };
    const { controller, dependencies, reportClipboardFailure } = setup(range);

    await controller.dispatch({ type: "keyboard.copy" });

    expect(PASTE_CELL_LIMIT).toBe(10_000);
    expect(dependencies.writeClipboard).not.toHaveBeenCalled();
    expect(reportClipboardFailure).toHaveBeenCalledWith("tooLarge", "copy");
  });

  it("复制写入失败时按原因上报；命令上下文失效则静默", async () => {
    const range = { rows: [{ title: "任务" }], fields: ["title"] };
    const { controller, dependencies, reportClipboardFailure, setScopeCurrent } = setup(range);
    vi.mocked(dependencies.writeClipboard).mockRejectedValue(
      Object.assign(new Error("busy"), { code: "CLIPBOARD_BUSY" }),
    );

    await controller.dispatch({ type: "keyboard.copy" });
    expect(reportClipboardFailure).toHaveBeenCalledWith("busy", "copy");

    reportClipboardFailure.mockClear();
    setScopeCurrent(false);
    await controller.dispatch({ type: "keyboard.copy" });
    expect(reportClipboardFailure).not.toHaveBeenCalled();
  });

  it("粘贴读取失败时上报可见错误，退役类失败静默", async () => {
    const { controller, dependencies, reportClipboardFailure, setScopeCurrent } = setup(null);
    useWorkspaceStore().selectTable("orders");

    vi.mocked(dependencies.readClipboard).mockRejectedValue(
      Object.assign(new Error("占用"), { code: "CLIPBOARD_BUSY" }),
    );
    await controller.dispatch({ type: "keyboard.paste" });
    expect(reportClipboardFailure).toHaveBeenCalledWith("busy", "paste");

    reportClipboardFailure.mockClear();
    vi.mocked(dependencies.readClipboard).mockRejectedValue(
      Object.assign(new Error("retired"), { code: "WORKSPACE_SESSION_STALE" }),
    );
    await controller.dispatch({ type: "keyboard.paste" });
    expect(reportClipboardFailure).not.toHaveBeenCalled();

    reportClipboardFailure.mockClear();
    vi.mocked(dependencies.readClipboard).mockRejectedValue(new Error("busy"));
    setScopeCurrent(false);
    await controller.dispatch({ type: "keyboard.paste" });
    expect(reportClipboardFailure).not.toHaveBeenCalled();
  });

  it("粘贴异步返回后命令上下文失效则不预检不回填", async () => {
    const { controller, dependencies, pasteService, setScopeCurrent } = setup(null);
    useWorkspaceStore().selectTable("orders");
    let resolveRead: (text: string) => void = () => undefined;
    vi.mocked(dependencies.readClipboard).mockImplementation(
      () => new Promise(resolve => { resolveRead = resolve; }),
    );

    const pending = controller.dispatch({ type: "keyboard.paste" });
    setScopeCurrent(false);
    resolveRead("客户一\t进行中");
    await pending;

    expect(pasteService.preview).not.toHaveBeenCalled();
  });

  it("删除必须让 range 中每一行都携带产品 digest", async () => {
    const digest = `sha256:${"a".repeat(64)}`;
    const rows: Array<Record<string, unknown>> = [
      { rowKey: "row-1", __vibetableDigest: digest },
      { rowKey: "row-2" },
    ];
    const range = { rows, fields: ["title"] };
    const { controller, deleteRows } = setup(range);

    await controller.dispatch({ type: "keyboard.delete" });
    expect(deleteRows).not.toHaveBeenCalled();

    rows[1] = { rowKey: "row-2", __vibetableDigest: digest };
    await controller.dispatch({ type: "keyboard.delete" });
    expect(deleteRows).toHaveBeenCalledWith([
      { rowKey: "row-1", expectedDigest: digest },
      { rowKey: "row-2", expectedDigest: digest },
    ]);
  });

  it("粘贴只把剪贴板矩阵和稳定 selection 交给 preview seam", async () => {
    const { controller, dependencies, grid, pasteService } = setup(null);
    useWorkspaceStore().selectTable("orders");
    vi.mocked(dependencies.readClipboard).mockResolvedValue("客户一\t进行中\n客户二\t已完成");
    const querySnapshot = {
      snapshotId: "snapshot-1",
      digest: "digest-1",
      databaseId: "database-1",
      table: "orders",
      schemaRevision: "schema-1",
      dataRevision: 7,
      normalizedQuery: { sorts: [{ field: "created", direction: "desc" }] },
    };
    const context: PasteContext = {
      schemaRevision: "schema-1",
      editableColumns: ["customer", "status"],
      anchorColumnIndex: 0,
      selection: { querySnapshot, dataRevision: 7, rowKeys: ["row-1", "row-2"] },
      startCell: { rowKey: "row-1", column: "customer" },
    };
    vi.mocked(grid.resolvePasteContext).mockReturnValue(context);

    await controller.dispatch({ type: "menu.paste" });

    expect(pasteService.preview).toHaveBeenCalledWith({
      collection: "orders",
      schemaRevision: "schema-1",
      selection: context.selection,
      startCell: context.startCell,
      cells: [
        [
          { rowIndex: 0, columnIndex: 0, column: "customer", rawValue: "客户一", parsedValue: "客户一" },
          { rowIndex: 0, columnIndex: 1, column: "status", rawValue: "进行中", parsedValue: "进行中" },
        ],
        [
          { rowIndex: 1, columnIndex: 0, column: "customer", rawValue: "客户二", parsedValue: "客户二" },
          { rowIndex: 1, columnIndex: 1, column: "status", rawValue: "已完成", parsedValue: "已完成" },
        ],
      ],
    });
  });

  it("粘贴带引号嵌入换行/制表符的单元格不再被拆行", async () => {
    const { controller, dependencies, grid, pasteService } = setup(null);
    useWorkspaceStore().selectTable("orders");
    const multiLine = '"第一行\n第二行"\t"尾\t列"';
    vi.mocked(dependencies.readClipboard).mockResolvedValue(multiLine);
    const context: PasteContext = {
      schemaRevision: "schema-1",
      editableColumns: ["customer", "status"],
      anchorColumnIndex: 0,
      selection: {
        querySnapshot: null as unknown as PasteContext["selection"]["querySnapshot"],
        dataRevision: 1,
        rowKeys: ["row-1"],
      },
      startCell: { rowKey: "row-1", column: "customer" },
    };
    vi.mocked(grid.resolvePasteContext).mockReturnValue(context);

    await controller.dispatch({ type: "keyboard.paste" });

    const payload = vi.mocked(pasteService.preview).mock.calls[0][0];
    expect(payload.cells).toEqual([
      [
        { rowIndex: 0, columnIndex: 0, column: "customer", rawValue: "第一行\n第二行", parsedValue: "第一行\n第二行" },
        { rowIndex: 0, columnIndex: 1, column: "status", rawValue: "尾\t列", parsedValue: "尾\t列" },
      ],
    ]);
  });

  it("共用 canExecute 上下文在 dispatch 入口拦截网格命令", async () => {
    const range = { rows: [{ title: "任务" }], fields: ["title"] };
    const { controller, dependencies, grid } = setup(range, {
      canExecuteGridCommand: () => false,
    });

    await controller.dispatch({ type: "keyboard.copy" });
    await controller.dispatch({ type: "menu.copy" });
    await controller.dispatch({ type: "keyboard.paste" });
    await controller.dispatch({ type: "keyboard.delete" });
    await controller.dispatch({ type: "keyboard.selectAll" });
    await controller.dispatch({ type: "keyboard.editCell" });

    expect(dependencies.writeClipboard).not.toHaveBeenCalled();
    expect(grid.selectAll).not.toHaveBeenCalled();
    expect(grid.editActiveCell).not.toHaveBeenCalled();
  });

  it("命令上下文失效时复制在起始即被拒绝", async () => {
    const range = { rows: [{ title: "任务" }], fields: ["title"] };
    const { controller, dependencies, setScopeCurrent } = setup(range);
    setScopeCurrent(false);

    await controller.dispatch({ type: "keyboard.copy" });

    expect(dependencies.writeClipboard).not.toHaveBeenCalled();
  });

  it("菜单动作仅豁免按钮焦点：活跃编辑器/文字选择仍被拒", async () => {
    const range = { rows: [{ title: "任务" }], fields: ["title"] };
    const { controller, dependencies } = setup(range);
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();

    await controller.dispatch({ type: "menu.copy" });
    expect(dependencies.writeClipboard).not.toHaveBeenCalled();

    input.blur();
    const paragraph = document.createElement("p");
    paragraph.textContent = "选中文字";
    document.body.appendChild(paragraph);
    const selectionRange = document.createRange();
    selectionRange.selectNodeContents(paragraph);
    window.getSelection()!.removeAllRanges();
    window.getSelection()!.addRange(selectionRange);
    await controller.dispatch({ type: "menu.paste" });
    expect(dependencies.readClipboard).not.toHaveBeenCalled();

    window.getSelection()!.removeAllRanges();
    document.body.removeChild(paragraph);
    document.body.removeChild(input);
  });

  it("菜单按钮焦点本身不阻断复制", async () => {
    const range = { rows: [{ title: "任务" }], fields: ["title"] };
    const { controller, dependencies } = setup(range);
    const button = document.createElement("button");
    document.body.appendChild(button);
    button.focus();

    await controller.dispatch({ type: "menu.copy" });

    expect(dependencies.writeClipboard).toHaveBeenCalledWith("任务");
    document.body.removeChild(button);
  });

  it("确认粘贴只消费 store token，并为本次 apply 创建新幂等键", async () => {
    const { controller, pasteService } = setup(null);
    useWorkspaceStore().selectTable("orders");
    const plan: PastePlan = {
      collection: "orders",
      schemaRevision: "schema-1",
      capabilityHash: "capability-1",
      summary: {
        updateRows: 1,
        insertRows: 0,
        skipRows: 0,
        errorCount: 0,
        warningCount: 0,
      },
      rows: [],
      diagnostics: [],
      token: { token: "paste-token", expiresAt: 10, consumed: false },
      overflow: false,
    };
    usePasteStore().setPlan(plan);

    await controller.dispatch({ type: "paste.confirm" });

    expect(pasteService.apply).toHaveBeenCalledWith({
      collection: "orders",
      token: "paste-token",
      idempotencyKey: "operation-1",
    });
  });

  it("表创建、删除请求与确认都通过表管理 seam", async () => {
    const { controller, dependencies, tableAdminService } = setup(null);

    await controller.dispatch({ type: "table.create" });
    await controller.dispatch({ type: "table.requestDelete", table: "orders" });
    expect(dependencies.ui.deleteTarget).toBe("orders");
    await controller.dispatch({ type: "table.delete" });

    expect(tableAdminService.createTable).toHaveBeenCalledOnce();
    expect(tableAdminService.deleteTable).toHaveBeenCalledWith("orders");
  });

  it("复制数据表意图只路由到权威 duplicate seam，不做前端伪复制", async () => {
    const { controller, duplicateTable } = setup(null);

    await controller.dispatch({
      type: "table.duplicate",
      collection: "orders",
      displayName: "订单 副本",
    });

    expect(duplicateTable).toHaveBeenCalledTimes(1);
    expect(duplicateTable).toHaveBeenCalledWith("orders", "订单 副本");
  });
});
