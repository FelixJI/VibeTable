import type { ColumnSchema, LookupDefinition, NormalizedRelationDescriptor, PresetView } from "@/contracts";
import { findLookupDefinition, formatFormulaDisplayValue, renderFormulaEnvelope, renderLookupEnvelope } from "@/grid/computedValueDisplay";
import { formatFieldDisplay } from "@/grid/commonFieldDisplay";
import { normalizeTargets } from "@/grid/relationLookupRenderer";
import { coerceLegacyLabelEntry, formatRelationLabelEntry, relationRowLabels } from "@/grid/relationDisplay";
import { getLocale, t } from "@/i18n";

function isEnvelope(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value)
    && "state" in (value as Record<string, unknown>);
}

/**
 * Render one record-card field value. Numeric formatting is type-driven:
 * scalar numbers only render through the authoritative column DisplaySpec
 * when the column's declared dataType is decimal/integer; formula/lookup
 * envelopes reuse the shared computed-value renderer (state first, then the
 * authoritative result type). Runtime number-looking values under other
 * declared types never get numeric formatting.
 */
export function displayValue(
  value: unknown,
  column?: ColumnSchema | null,
  lookups?: readonly LookupDefinition[] | ReadonlyMap<string, LookupDefinition> | null,
  row?: Record<string, unknown> | null,
  relations?: readonly NormalizedRelationDescriptor[] | null,
): string {
  if (value === null || value === undefined || value === "") return "—";
  if (column?.kind === "relation" && relations) {
    const summary = relationSummaryText(value, column, row, relations);
    if (summary !== null) return summary;
  }
  if (isEnvelope(value)) {
    if (column?.kind === "formula") {
      return renderFormulaEnvelope(value, column);
    }
    if (column?.kind === "lookup") {
      return renderLookupEnvelope(value, findLookupDefinition(column, lookups), column.display);
    }
  }
  if (column?.kind === "formula" && column.dataType === "json"
    && column.resultElementType === "number" && Array.isArray(value)) {
    return formatFormulaDisplayValue(value, column);
  }
  if (column) return formatFieldDisplay(value, column, getLocale());
  if (typeof value === "boolean") return value ? "✓" : "✕";
  if (typeof value === "object") {
    try { return JSON.stringify(value); } catch { return String(value); }
  }
  return String(value);
}

export function metadataFields(
  schema: readonly ColumnSchema[],
  excluded: readonly (string | null | undefined)[],
  visibleFields: readonly string[] = [],
): readonly ColumnSchema[] {
  const names = new Set(excluded.filter((value): value is string => Boolean(value)));
  const visible = new Set(visibleFields);
  return schema.filter((column) => (
    !names.has(column.name)
    && column.name !== "rowKey"
    && (!visible.size || visible.has(column.name))
  )).slice(0, 3);
}

export function safeImageUrl(value: unknown): string | null {
  if (Array.isArray(value)) return value.map(safeImageUrl).find(Boolean) ?? null;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return safeImageUrl(record.thumbnailUrl ?? record.url ?? null);
  }
  if (typeof value !== "string") return null;
  const candidate = value.trim();
  return /^(data:image\/|\/(?!\/))/i.test(candidate) ? candidate : null;
}

export { rowTitle };

/**
 * Render a relation cell for record cards: the same batch-loaded typed label
 * projection and display contract as grid tokens (first three labels plus a
 * remainder), falling back to the raw persisted labels/IDs.
 */
function relationSummaryText(
  value: unknown,
  column: ColumnSchema,
  row: Record<string, unknown> | null | undefined,
  relations: readonly NormalizedRelationDescriptor[],
): string | null {
  const targets = normalizeTargets(value);
  if (targets.length === 0) return "—";
  const descriptor = relations.find(
    candidate => candidate.relationId === column.relationId
      || candidate.fieldRef === column.name,
  ) ?? null;
  const labels = relationRowLabels(row, column.name);
  const separator = t("grid.valueSeparator");
  const parts: string[] = [];
  for (const target of targets.slice(0, 3)) {
    const entry = coerceLegacyLabelEntry(labels[target.itemId]);
    const formatted = entry !== null ? formatRelationLabelEntry(entry, descriptor) : null;
    parts.push(formatted ?? (target.label && target.label.trim() !== "" ? target.label : target.itemId));
  }
  const remainder = targets.length > 3 ? ` +${targets.length - 3}` : "";
  return parts.join(separator) + remainder;
}

function rowTitle(
  row: Record<string, unknown>, view: PresetView, schema: readonly ColumnSchema[] = [],
  lookups?: readonly LookupDefinition[], relations?: readonly NormalizedRelationDescriptor[],
): string {
  const value = view.titleField ? row[view.titleField] : null;
  if (value !== null && value !== undefined && String(value).trim()) {
    return displayValue(value, schema.find(column => column.name === view.titleField), lookups, row, relations);
  }
  return t("views.recordFallback", { id: String(row.rowKey ?? "—") });
}
