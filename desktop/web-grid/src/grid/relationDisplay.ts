/**
 * Shared relation label display projection (issue #447).
 *
 * Every surface — grid tokens, record cards, gallery/kanban details and the
 * relation picker — renders relation labels through these helpers so there is
 * exactly one display authority:
 *
 *   - Label values arrive as raw typed scalars (numbers stay numbers; 0 and
 *     false are valid values), together with the controlled source that
 *     produced them ("display" = the relation's configured displayFieldId,
 *     "primary" = the target table's global primary display fallback).
 *   - Numeric labels format through the merged #445 contract
 *     (`formatNumberDisplay`) keyed off the *source field's own* DisplaySpec,
 *     so a percentage display field falling back to a currency primary never
 *     formats with one shared spec.
 *   - Missing labels fall back to the record ID; record IDs remain the only
 *     relation identity and never join mutation business values.
 */

import type {
  NormalizedRelationDescriptor,
  RelationDisplayFieldInfo,
  RelationLabelEntry,
  RelationTargetRef,
} from "@/contracts";
import { formatNumberDisplay } from "@/number/numberDisplay";
import { getLocale } from "@/i18n";

function isNumeric(info: RelationDisplayFieldInfo | null | undefined): boolean {
  return info?.dataType === "decimal" || info?.dataType === "integer";
}

/** Format one raw label scalar with the owning field's render contract. */
export function formatRelationLabelValue(
  value: unknown,
  info: RelationDisplayFieldInfo | null | undefined,
): string | null {
  if (typeof value === "number" && isNumeric(info)) {
    return formatNumberDisplay(value, info?.display ?? undefined, getLocale());
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    return trimmed !== "" ? trimmed : null;
  }
  if (typeof value === "boolean") return value ? "✓" : "✕";
  return null;
}

function displayInfoFor(
  descriptor: NormalizedRelationDescriptor | null | undefined,
  source: RelationLabelEntry["source"],
): RelationDisplayFieldInfo | null | undefined {
  if (!descriptor) return undefined;
  return source === "display" ? descriptor.displayFieldInfo : descriptor.fallbackDisplayFieldInfo;
}

/** Render one typed row label entry, formatting with its own source spec. */
export function formatRelationLabelEntry(
  entry: unknown,
  descriptor: NormalizedRelationDescriptor | null | undefined,
): string | null {
  if (!isRelationLabelEntry(entry)) return null;
  return formatRelationLabelValue(entry.value, displayInfoFor(descriptor, entry.source));
}

/** Render a picker/search target label from its typed projection. */
export function relationTargetLabel(
  target: RelationTargetRef,
  descriptor: NormalizedRelationDescriptor | null | undefined,
): string {
  const display = formatRelationLabelValue(target.displayValue ?? null, descriptor?.displayFieldInfo);
  if (display !== null) return display;
  const fallback = formatRelationLabelValue(target.secondaryValue ?? null, descriptor?.fallbackDisplayFieldInfo);
  if (fallback !== null) return fallback;
  return target.label || target.itemId;
}

/** Render the auxiliary global primary label (secondary line/tooltips). */
export function relationTargetSecondaryLabel(
  target: RelationTargetRef,
  descriptor: NormalizedRelationDescriptor | null | undefined,
): string {
  if (target.displayValue !== null && target.displayValue !== undefined) {
    return formatRelationLabelValue(target.secondaryValue ?? null, descriptor?.fallbackDisplayFieldInfo) ?? "";
  }
  return "";
}

/** Extract one field's typed label map from a grid row. */
export function relationRowLabels(
  row: Record<string, unknown> | null | undefined,
  field: string,
): Readonly<Record<string, RelationLabelEntry>> {
  const metadata = row?.__vibetableRelationLabels;
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) return {};
  const entries = (metadata as Record<string, unknown>)[field];
  if (!entries || typeof entries !== "object" || Array.isArray(entries)) return {};
  return entries as Record<string, RelationLabelEntry>;
}

/** Legacy string-shaped labels (older hosts/tests) still render as text. */
export function coerceLegacyLabelEntry(value: unknown): RelationLabelEntry | null {
  if (typeof value === "string") {
    return value.trim() !== "" ? { value, source: "display" } : null;
  }
  return isRelationLabelEntry(value) ? value : null;
}

function isRelationLabelEntry(value: unknown): value is RelationLabelEntry {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const entry = value as Record<string, unknown>;
  return (entry.source === "display" || entry.source === "primary")
    && (typeof entry.value === "string" || typeof entry.value === "number" || typeof entry.value === "boolean");
}
