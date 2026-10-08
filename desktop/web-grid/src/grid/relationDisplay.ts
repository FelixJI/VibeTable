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
 *   - Labels format through the merged #459 contract (`formatFieldDisplay`)
 *     keyed off the *source field's own* DisplaySpec/enumOptions, so a
 *     percentage display field falling back to a currency primary never
 *     formats with one shared spec, while date/time/custom-bool/select
 *     labels render exactly like their grid columns.
 *   - Eligibility stays scalar-only (string/number/boolean): the general
 *     formatter's array/object handling never widens which relation label
 *     values may surface, and empty text still falls back to the record ID.
 *     Record IDs remain the only relation identity and never join mutation
 *     business values.
 */

import type {
  NormalizedRelationDescriptor,
  RelationDisplayFieldInfo,
  RelationLabelEntry,
  RelationTargetRef,
} from "@/contracts";
import { formatFieldDisplay } from "@/grid/commonFieldDisplay";
import { getLocale } from "@/i18n";

function isScalarLabelValue(value: unknown): value is string | number | boolean {
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean";
}

/** Format one raw label scalar with the owning field's render contract. */
export function formatRelationLabelValue(
  value: unknown,
  info: RelationDisplayFieldInfo | null | undefined,
): string | null {
  // Scalar eligibility only: arrays/objects/envelopes never surface as
  // relation labels, and empty text is missing (falls back to the record ID).
  if (!isScalarLabelValue(value)) return null;
  if (typeof value === "string" && value.trim() === "") return null;
  const scalar = typeof value === "string" ? value.trim() : value;
  // Booleans keep the legacy ✓/✕ contract unless the source field itself is
  // boolean-typed, where the shared formatter applies custom true/false
  // labels; eligibility stays driven by the declared dataType, not the
  // runtime shape of the value.
  if (typeof value === "boolean" && info?.dataType !== "boolean") {
    return value ? "✓" : "✕";
  }
  // Numbers stay bounded to numerically-typed sources: a number under any
  // other declared type is not a formatted label and falls back to the
  // record ID chain, exactly like the grid's type-driven numeric contract.
  // Non-finite numbers stay missing too: the shared formatter would render
  // "NaN"/"Infinity", but the old relation contract (formatNumberDisplay
  // null) keeps them out of labels entirely.
  if (typeof value === "number"
    && (!Number.isFinite(value) || (info?.dataType !== "decimal" && info?.dataType !== "integer"))) {
    return null;
  }
  if (!info) return String(scalar);
  return formatFieldDisplay(
    scalar,
    {
      dataType: info.dataType,
      display: info.display ?? undefined,
      enumOptions: info.enumOptions ?? undefined,
    },
    getLocale(),
  );
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
