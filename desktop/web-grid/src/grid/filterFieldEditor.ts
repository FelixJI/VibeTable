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
