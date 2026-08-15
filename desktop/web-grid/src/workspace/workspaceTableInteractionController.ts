import type { Ref } from "vue";
import type { TabulatorFull } from "tabulator-tables";

import type {
  ColumnSchema,
  MutationRevision,
  PasteCellPayload,
  PreviewPasteRequestedPayload,
  QuerySnapshot,
} from "@/contracts";
import { ROW_NUMBER_FIELD } from "@/grid/createGrid";
import { classifyClipboard, mapCellsToColumns, parseClipboard } from "@/grid/clipboardParser";
import { resolvePasteContext, type PasteContext } from "@/grid/pasteContext";
import type { ApplyPasteInput, usePasteService } from "@/services/pasteService";
import type { useMutationService } from "@/services/mutationService";
import type { useTableAdminService } from "@/services/tableAdminService";
import type { usePasteStore } from "@/stores/pasteStore";
import type { useTableAdminStore } from "@/stores/tableAdminStore";
import type { useTableStore } from "@/stores/tableStore";
import type { useUiStore } from "@/stores/uiStore";
import type { useWorkspaceStore } from "@/stores/workspaceStore";

type PasteService = ReturnType<typeof usePasteService>;
type MutationService = ReturnType<typeof useMutationService>;
type TableAdminService = ReturnType<typeof useTableAdminService>;

interface ActiveTableRange {
  readonly rows: readonly Readonly<Record<string, unknown>>[];
  readonly fields: readonly string[];
}

export interface TableInteractionGridPort {
  readActiveRange(): ActiveTableRange | null;
  resolvePasteContext(input: {
    readonly columns: readonly ColumnSchema[];
    readonly querySnapshot: QuerySnapshot | null;
    readonly revision: MutationRevision | null;
  }): PasteContext;
  selectAll(): void;
  editActiveCell(): void;
}

interface TabulatorRangeLike {
  getRanges(): Array<{
    getRows(): Array<{
      getData(): Readonly<Record<string, unknown>>;
      getCell(field: string): unknown;
    }>;
    getColumns(): Array<{ getField(): string }>;
    getCells(): Array<Array<{ edit(): void }>>;
    remove(): void;
  }>;
}

interface TabulatorSelectionLike extends TabulatorRangeLike {
  getRows(): Array<{
    getData(): Readonly<Record<string, unknown>>;
    getCell(field: string): unknown;
  }>;
  getColumns(): Array<{ getField(): string }>;
  addRange(start: unknown, end: unknown): unknown;
}

function isObject(value: unknown): value is Record<PropertyKey, unknown> {
  return typeof value === "object" && value !== null;
}

function isTabulatorRangeLike(value: unknown): value is TabulatorRangeLike {
  return isObject(value) && typeof value.getRanges === "function";
}

function isTabulatorSelectionLike(value: unknown): value is TabulatorSelectionLike {
  return isObject(value)
    && typeof value.getRanges === "function"
    && typeof value.getRows === "function"
    && typeof value.getColumns === "function"
    && typeof value.addRange === "function";
}

export function createTabulatorInteractionAdapter(
  grid: Ref<TabulatorFull | null>,
): TableInteractionGridPort {
  const rangeGrid = (): TabulatorRangeLike | null => {
    const candidate: unknown = grid.value;
    return isTabulatorRangeLike(candidate) ? candidate : null;
  };
  const selectionGrid = (): TabulatorSelectionLike | null => {
    const candidate: unknown = grid.value;
    return isTabulatorSelectionLike(candidate) ? candidate : null;
  };
  return {
    readActiveRange() {
      const range = rangeGrid()?.getRanges().at(-1);
      if (!range) return null;
      return {
        rows: range.getRows().map(row => row.getData()),
        fields: range.getColumns().map(column => column.getField()),
      };
    },
    resolvePasteContext(input) {
      return resolvePasteContext({ grid: grid.value, ...input });
    },
    selectAll() {
      const active = selectionGrid();
      const rows = active?.getRows() ?? [];
      const columns = (active?.getColumns() ?? []).filter(column =>
        column.getField() !== "rowKey" && column.getField() !== ROW_NUMBER_FIELD,
      );
      if (!active || rows.length === 0 || columns.length === 0) return;
      for (const range of active.getRanges()) range.remove();
      active.addRange(
        rows[0].getCell(columns[0].getField()),
        rows.at(-1)!.getCell(columns.at(-1)!.getField()),
      );
    },
    editActiveCell() {
      rangeGrid()?.getRanges().at(-1)?.getCells()[0]?.[0]?.edit();
    },
  };
}

export type WorkspaceTableIntent =
  | { readonly type: "table.requestDelete"; readonly table: string }
  | { readonly type: "table.create" }
  | { readonly type: "table.delete" }
  | { readonly type: "table.cancelCreate" }
  | { readonly type: "table.cancelDelete" }
  | { readonly type: "paste.confirm" }
  | { readonly type: "paste.cancel" }
  | { readonly type: "keyboard.copy" }
  | { readonly type: "keyboard.paste" }
  | { readonly type: "keyboard.delete" }
  | { readonly type: "keyboard.selectAll" }
  | { readonly type: "keyboard.editCell" };

export interface WorkspaceTableInteractionController {
  dispatch(intent: WorkspaceTableIntent): Promise<void>;
}

export interface WorkspaceTableInteractionDependencies {
  readonly workspace: Pick<ReturnType<typeof useWorkspaceStore>, "currentTable">;
  readonly table: Pick<ReturnType<typeof useTableStore>, "schema" | "pages" | "revision">;
  readonly ui: Pick<ReturnType<typeof useUiStore>,
    "deleteTarget" | "openDelete" | "closeCreate" | "closeDelete" | "closePastePanel">;
  readonly admin: Pick<ReturnType<typeof useTableAdminStore>, "close">;
  readonly paste: Pick<ReturnType<typeof usePasteStore>, "plan" | "setOverflow" | "reset">;
  readonly pasteService: Pick<PasteService, "preview" | "apply">;
  readonly mutationService: Pick<MutationService, "deleteRows">;
  readonly tableAdminService: Pick<TableAdminService, "createTable" | "deleteTable">;
  readonly grid: TableInteractionGridPort;
  readonly readClipboard: () => Promise<string>;
  readonly writeClipboard: (text: string) => Promise<void>;
  readonly createId: () => string;
}

export function createWorkspaceTableInteractionController(
  dependencies: WorkspaceTableInteractionDependencies,
): WorkspaceTableInteractionController {
  async function copy(): Promise<void> {
    const range = dependencies.grid.readActiveRange();
    if (!range) return;
    if (range.rows.length === 0 || range.fields.length === 0) return;
    const tsv = range.rows.map(row => {
      return range.fields.map(field => String(row[field] ?? "")).join("\t");
    }).join("\n");
    await dependencies.writeClipboard(tsv);
  }

  async function paste(): Promise<void> {
    const collection = dependencies.workspace.currentTable;
    if (!collection) return;
    let text: string;
    try {
      text = await dependencies.readClipboard();
    } catch {
      return;
    }
    if (!text) return;
    let parsed;
    try {
      parsed = parseClipboard(text);
    } catch {
      return;
    }
    const classified = classifyClipboard(parsed);
    if ("overflow" in classified) {
      dependencies.paste.setOverflow();
      return;
    }
    let context;
    try {
      context = dependencies.grid.resolvePasteContext({
        columns: dependencies.table.schema ?? [],
        querySnapshot: dependencies.table.pages[0]?.querySnapshot ?? null,
        revision: dependencies.table.revision,
      });
    } catch {
      return;
    }
    const mapped = mapCellsToColumns(parsed, context.editableColumns, context.anchorColumnIndex);
    const cells: PasteCellPayload[][] = mapped.map(row => row.map(cell => ({
      rowIndex: cell.rowIndex,
      columnIndex: cell.columnIndex,
      column: cell.column,
      rawValue: cell.rawValue,
      parsedValue: cell.rawValue,
    })));
    const payload: PreviewPasteRequestedPayload = {
      collection,
      schemaRevision: context.schemaRevision,
      selection: context.selection,
      startCell: context.startCell,
      cells,
    };
    dependencies.pasteService.preview(payload);
  }

  function removeRows(): void {
    const range = dependencies.grid.readActiveRange();
    if (!range) return;
    const rows = range.rows.flatMap(data => {
      const rowKey = data.rowKey;
      if (typeof rowKey !== "string" && typeof rowKey !== "number") return [];
      return {
        rowKey,
        expectedDigest: typeof data.__vibetableDigest === "string"
          ? data.__vibetableDigest
          : "",
      };
    });
    if (
      rows.length === 0
      || rows.some(row => !/^sha256:[0-9a-f]{64}$/u.test(row.expectedDigest))
    ) return;
    dependencies.mutationService.deleteRows(rows);
  }

  async function dispatch(intent: WorkspaceTableIntent): Promise<void> {
    switch (intent.type) {
      case "table.requestDelete": dependencies.ui.openDelete(intent.table); return;
      case "table.create": dependencies.tableAdminService.createTable(); return;
      case "table.delete":
        if (dependencies.ui.deleteTarget) {
          dependencies.tableAdminService.deleteTable(dependencies.ui.deleteTarget);
        }
        return;
      case "table.cancelCreate":
        dependencies.ui.closeCreate();
        dependencies.admin.close();
        return;
      case "table.cancelDelete":
        dependencies.ui.closeDelete();
        dependencies.admin.close();
        return;
      case "paste.confirm": {
        const plan = dependencies.paste.plan;
        const collection = dependencies.workspace.currentTable;
        if (!plan || !collection) return;
        const input: ApplyPasteInput = {
          collection,
          token: plan.token?.token ?? "",
          idempotencyKey: dependencies.createId(),
        };
        dependencies.pasteService.apply(input);
        return;
      }
      case "paste.cancel":
        dependencies.paste.reset();
        dependencies.ui.closePastePanel();
        return;
      case "keyboard.copy": await copy(); return;
      case "keyboard.paste": await paste(); return;
      case "keyboard.delete": removeRows(); return;
      case "keyboard.selectAll": dependencies.grid.selectAll(); return;
      case "keyboard.editCell": dependencies.grid.editActiveCell();
    }
  }

  return { dispatch };
}
