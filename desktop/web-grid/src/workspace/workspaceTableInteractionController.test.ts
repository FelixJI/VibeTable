import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PastePlan } from "@/contracts";
import type { PasteContext } from "@/grid/pasteContext";
import { usePasteStore } from "@/stores/pasteStore";
import { useTableAdminStore } from "@/stores/tableAdminStore";
import { useTableStore } from "@/stores/tableStore";
import { useUiStore } from "@/stores/uiStore";
import { useWorkspaceStore } from "@/stores/workspaceStore";
import {
  createWorkspaceTableInteractionController,
  type TableInteractionGridPort,
  type WorkspaceTableInteractionDependencies,
} from "./workspaceTableInteractionController";

function setup(range: ReturnType<TableInteractionGridPort["readActiveRange"]>) {
  const deleteRows = vi.fn();
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
  const dependencies: WorkspaceTableInteractionDependencies = {
    workspace: useWorkspaceStore(),
    table: useTableStore(),
    ui: useUiStore(),
    admin: useTableAdminStore(),
    paste: usePasteStore(),
    pasteService,
    mutationService,
    tableAdminService: { createTable: vi.fn(), deleteTable: vi.fn() },
    grid,
    readClipboard: vi.fn(async () => ""),
    writeClipboard: vi.fn(async () => undefined),
    createId: () => "operation-1",
  };
  return {
    controller: createWorkspaceTableInteractionController(dependencies),
    dependencies,
    deleteRows,
    grid,
    pasteService,
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

    await controller.dispatch({ type: "keyboard.paste" });

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
});
