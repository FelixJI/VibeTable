import type { ColumnSchema } from "@/contracts";

/**
 * Filter operand editor derivation shared by the advanced filter tree and the
 * header value menu.
 *
 * `filterInput`/`filterOptions` on `ColumnSchema` are the authoritative
 * SchemaCore projection. When the host schema-describe projection has not
 * published them, a closed-enum select column still carries its authoritative
 * `display.kind === "select"` plus canonical `enumOptions` — deriving from
 * that schema metadata (never from loaded row values) keeps the operand
 * editor authoritative while the projection catches up.
 */

export type FilterEditorInput = NonNullable<ColumnSchema["filterInput"]>;

export function filterEditorInput(column: ColumnSchema | undefined): FilterEditorInput {
  if (column?.filterInput) return column.filterInput;
  if (column?.display?.kind === "select" && (column.enumOptions?.length ?? 0) > 0) {
    return "select";
  }
  return "text";
}

export interface FilterEditorOption {
  readonly value: string;
  readonly label: string;
}

export function filterEditorOptions(
  column: ColumnSchema | undefined,
): readonly FilterEditorOption[] {
  const explicit = column?.filterOptions;
  if (explicit && explicit.length > 0) return explicit.map(option => ({ ...option }));
  if (column?.display?.kind === "select" && (column.enumOptions?.length ?? 0) > 0) {
    return column.enumOptions!.map(option => ({
      value: option.optionId,
      // 停用选项仍是合法存量值：可见并与 enumDisplayParts 的标注一致。
      label: option.state === "retired" ? `${option.label}（已停用）` : option.label,
    }));
  }
  return [];
}

/**
 * Plain text columns are the ones whose header input defaults to tokenized
 * `contains`. Option-backed text columns (single/multi select) keep exact
 * `eq` against the raw option id: their filtering UI is the closed option
 * set, not free text.
 */
export function isPlainTextColumn(column: ColumnSchema | undefined): boolean {
  if (!column || column.dataType !== "text") return false;
  if (column.filterInput === "select" || column.filterInput === "multiSelect") return false;
  return column.display?.kind !== "select" && (column.enumOptions?.length ?? 0) === 0;
}

/**
 * 表头输入对该列是否保持精确 `eq` 语义（`buildQuery` 的最终分支）。
 *
 * 普通文本列（空白分词 contains）、JSON 文档列（contains）与多选/成员
 * 列（contains 成员）的表头输入会被查询适配器改写操作符：把这些列的
 * `eq` 条件镜像进输入后，仅排序触发的 runtime 捕获也会把保存视图/高
 * 级条件改写成 contains 或列级 OR。列信息缺失时同样返回 false：无法
 * 证明可往返的条件一律不镜像。
 */
export function headerInputPreservesEq(column: ColumnSchema | undefined): boolean {
  if (!column) return false;
  if (column.filterInput === "multiSelect") return false;
  if (column.filterOperators?.includes("contains") && !column.filterOperators.includes("eq")) {
    return false;
  }
  if (column.dataType === "json") return false;
  return !isPlainTextColumn(column);
}
