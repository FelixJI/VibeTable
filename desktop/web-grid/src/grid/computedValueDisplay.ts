/**
 * Shared pure display helpers for computed cell values (Formula envelopes and
 * Lookup envelopes). The grid, record cards and any future surface render the
 * same envelope through these functions so state/typing rules stay in one
 * place:
 *
 *   - Freshness first: non-ready states (`updating`, `failed`, `cancelled`,
 *     `invalid`, `too_expensive`, `restricted`, ...) never surface their stale
 *     value as a new result.
 *   - Numeric formatting is type-driven: Formula results key off the column's
 *     authoritative dataType/resultElementType from the compiler, Lookup
 *     values off the definition's outputType/resultCardinality. A runtime
 *     number-looking value under a non-numeric declared type is never
 *     formatted as a number.
 */

import type { ColumnSchema, LookupDefinition } from "@/contracts";
import type { DisplaySpec } from "@/contracts/generated/schemaV2";
import { formatNumberDisplay } from "@/number/numberDisplay";
import { getLocale, t } from "@/i18n";

export function formulaStateLabel(state: unknown): string | null {
  switch (state) {
    case "updating": return "grid.formula.updating";
    case "failed": return "grid.formula.failed";
    case "cancelled": return "grid.formula.cancelled";
    case "invalid": return "grid.formula.invalid";
    case "too_expensive": return "grid.formula.too_expensive";
    default: return null;
  }
}

/** Numeric render gate shared by scalar columns and computed results. */
function numericText(
  value: unknown,
  numericType: boolean,
  display: DisplaySpec | null | undefined,
): string | null {
  if (!numericType) return null;
  return formatNumberDisplay(value, display, getLocale());
}

/**
 * Render a formula result envelope as plain text. Callers that need DOM
 * structure (state badges, tooltips) keep their own renderers but must reuse
 * {@link formulaStateLabel} and the same typing rules.
 */
export function renderFormulaEnvelope(
  envelope: Readonly<Record<string, unknown>>,
  options: Pick<ColumnSchema, "dataType" | "resultElementType" | "display">,
): string {
  if (envelope.state === "ready") {
    return formatFormulaDisplayValue(envelope.value, options);
  }
  const label = formulaStateLabel(envelope.state);
  if (label !== null) return t(label);
  return JSON.stringify(envelope);
}

/** Format Formula scalars and explicitly typed numeric lists from bare ready values. */
export function formatFormulaDisplayValue(
  value: unknown,
  options: Pick<ColumnSchema, "dataType" | "resultElementType" | "display">,
): string {
  if (value === null || value === undefined) return "—";
  if (options.dataType === "json" && options.resultElementType === "number" && Array.isArray(value)) {
    return value.map((item) => formatComputedScalar(item, true, options.display))
      .join(t("grid.valueSeparator"));
  }
  return numericText(value, options.dataType === "decimal" || options.dataType === "integer", options.display)
    ?? String(value);
}

const LOOKUP_STATE_LABELS: Readonly<Record<string, string>> = {
  restricted: "grid.lookup.restricted",
  invalid: "grid.lookup.invalid",
  too_expensive: "grid.lookup.tooExpensive",
};

/**
 * Render a lookup cell envelope as plain text. The authoritative
 * LookupDefinition (outputType + resultCardinality) decides element typing;
 * without a definition the value stays in its raw textual form.
 */
export function renderLookupEnvelope(
  envelope: Readonly<Record<string, unknown>>,
  definition: LookupDefinition | undefined,
  display: DisplaySpec | null | undefined,
): string {
  const state = String(envelope.state ?? "");
  if (state !== "ok" && state in LOOKUP_STATE_LABELS) {
    const diagnostic = envelope.diagnostic as { readonly code?: unknown } | undefined;
    return diagnostic?.code === "lookup.value.source_missing"
      ? "#REF!"
      : t(LOOKUP_STATE_LABELS[state]);
  }
  return formatLookupDisplayValue(envelope.value, definition, display);
}

/**
 * Format a lookup scalar or value list. Typing comes from the definition's
 * outputType (decimal/integer for SUM-style scalars and numeric list
 * elements) and resultCardinality decides the scalar/list shape — never an
 * observed cell value.
 */
export function formatLookupDisplayValue(
  value: unknown,
  definition: LookupDefinition | undefined,
  display: DisplaySpec | null | undefined,
): string {
  const numeric = !!definition
    && (definition.outputType === "decimal" || definition.outputType === "integer");
  const cardinality = definition?.resultCardinality;
  const list = cardinality === "many"
    || (cardinality === undefined && Array.isArray(value));
  if (list) {
    const items = Array.isArray(value) ? value : [value];
    return items
      .map((item) => formatComputedScalar(item, numeric, display))
      .join(t("grid.valueSeparator"));
  }
  return formatComputedScalar(value, numeric, display);
}

function formatComputedScalar(
  value: unknown,
  numeric: boolean,
  display: DisplaySpec | null | undefined,
): string {
  if (value === null || value === undefined) return "";
  const formatted = numericText(value, numeric, display);
  if (formatted !== null) return formatted;
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

/** Resolve the authoritative definition for a lookup column, if available. */
export function findLookupDefinition(
  column: ColumnSchema | null | undefined,
  lookups: readonly LookupDefinition[] | ReadonlyMap<string, LookupDefinition> | null | undefined,
): LookupDefinition | undefined {
  if (!column?.lookupId || !lookups) return undefined;
  if ("get" in lookups) return lookups.get(column.lookupId);
  return lookups.find((item) => item.lookupId === column.lookupId);
}
