import type { PresetView } from "@/contracts";
import { isFilterCondition } from "./viewQuery";

function compare(left: unknown, right: unknown): number {
  if (left === right) return 0;
  if (left === null || left === undefined) return 1;
  if (right === null || right === undefined) return -1;
  if (typeof left === "number" && typeof right === "number") return left - right;
  return String(left).localeCompare(String(right));
}

export function projectPresetRows(
  rows: readonly Record<string, unknown>[],
  view: PresetView,
): readonly Record<string, unknown>[] {
  const availableFields = new Set(rows.flatMap((row) => Object.keys(row)));
  const filters = view.filters.filter(isFilterCondition).filter((filter) => (
    availableFields.has(filter.field) && filter.operator === "eq"
  ));
  const search = view.search.trim().toLocaleLowerCase();
  const searchableFields = view.visibleFields.length
    ? view.visibleFields.filter((name) => availableFields.has(name))
    : [...availableFields].filter((name) => name !== "rowKey");
  const projected = rows.filter((row) => (
    filters.every((filter) => Object.is(row[filter.field], filter.value))
    && (!search || searchableFields.some((name) => String(row[name] ?? "").toLocaleLowerCase().includes(search)))
  ));
  const sorts = view.sorts.flatMap((sort) => {
    const name = sort.field;
    return availableFields.has(name)
      ? [{ field: name, direction: sort.direction === "desc" ? -1 : 1 }]
      : [];
  });
  return [...projected].sort((left, right) => {
    for (const sort of sorts) {
      const result = compare(left[sort.field], right[sort.field]);
      if (result !== 0) return result * sort.direction;
    }
    return 0;
  });
}
