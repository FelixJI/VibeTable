/** Resolve a paste target from the rendered product page and Tabulator range. */

import type { Tabulator } from "tabulator-tables";

import type {
  ColumnSchema,
  MutationRevision,
  QuerySnapshot,
  SelectionSnapshot,
} from "@/contracts";

export interface PasteContext {
  readonly schemaRevision: string;
  readonly editableColumns: readonly string[];
  readonly anchorColumnIndex: number;
  readonly selection: SelectionSnapshot;
  readonly startCell: {
    readonly rowKey: string | number;
    readonly column: string;
  };
}

export interface PasteContextSource {
  readonly grid: Pick<Tabulator, "getRanges"> | null;
  readonly columns: readonly ColumnSchema[];
  readonly querySnapshot: QuerySnapshot | null;
  readonly revision: MutationRevision | null;
}

/**
 * Read the active (latest) Tabulator range and bind it to the page snapshot.
 * Throws a UI-ready error when the user has not selected an editable target.
 */
export function resolvePasteContext(source: PasteContextSource): PasteContext {
  const schemaRevision =
    source.revision?.schemaRevision ?? source.querySnapshot?.schemaRevision;
  if (!schemaRevision || !source.querySnapshot) {
    throw new Error("表格元数据尚未就绪，请刷新后再粘贴。");
  }

  const editableColumns = source.columns
    .filter((column) => column.editable)
    .map((column) => column.name);
  if (editableColumns.length === 0) {
    throw new Error("当前表没有可编辑字段。");
  }

  const ranges = source.grid?.getRanges() ?? [];
  const activeRange = ranges.at(-1);
  if (!activeRange) {
    throw new Error("请先选择要粘贴的单元格范围。");
  }

  const rangeColumns = activeRange.getColumns();
  const anchorColumn = rangeColumns[0]?.getField();
  const anchorColumnIndex = anchorColumn
    ? editableColumns.indexOf(anchorColumn)
    : -1;
  if (!anchorColumn || anchorColumnIndex < 0) {
    throw new Error("粘贴起点必须是可编辑字段。");
  }

  const rowKeys = activeRange.getRows().map((row) => row.getData().rowKey);
  if (rowKeys.length === 0) {
    throw new Error("请选择至少一行作为粘贴目标。");
  }
  if (rowKeys.some((key) => typeof key !== "string" && typeof key !== "number")) {
    throw new Error("当前选择缺少稳定行标识，请刷新后重试。");
  }

  const typedRowKeys = rowKeys as Array<string | number>;
  return {
    schemaRevision,
    editableColumns,
    anchorColumnIndex,
    selection: {
      querySnapshot: source.querySnapshot,
      dataRevision: source.revision?.dataRevision ?? source.querySnapshot.dataRevision,
      rowKeys: typedRowKeys,
    },
    startCell: {
      rowKey: typedRowKeys[0],
      column: anchorColumn,
    },
  };
}
