import type { ColumnSchema, PresetView } from "@/contracts";
import { t } from "@/i18n";

export function rowTitle(row: Record<string, unknown>, view: PresetView): string {
  const value = view.titleField ? row[view.titleField] : null;
  if (value !== null && value !== undefined && String(value).trim()) return String(value);
  return t("views.recordFallback", { id: String(row.rowKey ?? "—") });
}

export function displayValue(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "boolean") return value ? "✓" : "✕";
  if (typeof value === "object") {
    try {
      return JSON.stringify(value);
    } catch {
      return String(value);
    }
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
