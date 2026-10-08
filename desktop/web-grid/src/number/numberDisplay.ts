/**
 * Shared pure numeric display formatting.
 *
 * One function serves the grid, formula-ready values, Lookup scalar/list
 * elements, the field-settings live preview and the record card/summary
 * surfaces. It is strictly display-only:
 *
 *   - Only real `number` inputs are formatted. `null`, non-finite values
 *     (NaN/±Infinity) and non-numeric shapes return `null` so callers keep
 *     rendering their state/empty markers; a value is never coerced into a
 *     number and a number is never rewritten back into the data layer.
 *   - `scaleMode === "fixed"` always keeps exactly `displayScale` fraction
 *     digits (12 → "12.00"), including legacy configs that still carry
 *     `trimTrailingZeros: true`; `max` shows up to `displayScale` digits
 *     (12 → "12"). The settings UI normalizes trim to false when switching to
 *     fixed, but rendering never degrades fixed to max.
 *   - `preset === "percent"` renders the stored value per `percentStorage`
 *     through the same Intl percent styling: ratio 0.125 → "12.5%", percent
 *     12.5 → "12.5%". The ÷100 rescale for percent storage happens only
 *     inside this pure display function; no write path ever rescales input.
 *   - `preset === "currency"` uses Intl currency styling with `display.currency`.
 *   - A non-empty `unit` string is appended verbatim.
 *
 * Rounding follows `Intl.NumberFormat` defaults (half-expand). Callers pass
 * the active UI locale explicitly; expectations are pinned in tests instead
 * of trusting implementation output.
 */

import type { DisplaySpec } from "@/contracts/generated/schemaV2";

const MAX_CACHE = 256;

const formatterCache = new Map<string, Intl.NumberFormat>();

function cachedFormatter(locale: string, options: Intl.NumberFormatOptions): Intl.NumberFormat {
  const key = `${locale}:${JSON.stringify(options)}`;
  let formatter = formatterCache.get(key);
  if (!formatter) {
    formatter = new Intl.NumberFormat(locale, options);
    if (formatterCache.size >= MAX_CACHE) formatterCache.clear();
    formatterCache.set(key, formatter);
  }
  return formatter;
}

function clampScale(scale: number): number {
  return Number.isSafeInteger(scale) && scale >= 0 ? Math.min(scale, 15) : 2;
}

/** Fraction digit range derived from the closed scale semantics above. */
export function fractionDigits(display: DisplaySpec): { readonly min: number; readonly max: number } {
  const scale = clampScale(display.displayScale);
  // fixed always pins both bounds; the legacy trim flag never degrades it.
  return display.scaleMode === "fixed"
    ? { min: scale, max: scale }
    : { min: 0, max: scale };
}

/**
 * Format one numeric value for display. Returns `null` when the input must
 * not be rendered as a number (null/undefined, non-finite, non-number types,
 * or an unusable display configuration such as an invalid currency code).
 */
export function formatNumberDisplay(
  value: unknown,
  display: DisplaySpec | null | undefined,
  locale: string,
): string | null {
  if (display === null || display === undefined) return null;
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  if (!["", "plain", "number", "integer", "currency", "percent", "unit", "progress", "rating"].includes(display.preset)) return null;
  const numeric = Object.is(value, -0) ? 0 : value;
  const { min, max } = fractionDigits(display);
  const grouping: boolean = display.useGrouping === true;
  try {
    if (display.preset === "rating") {
      const maximum = display.ratingMax ?? 5;
      if (!Number.isInteger(maximum) || maximum < 1 || maximum > 10
        || !Number.isInteger(numeric) || numeric < 0 || numeric > maximum) return null;
      return `${"★".repeat(numeric)}${"☆".repeat(maximum - numeric)} ${numeric}/${maximum}`;
    }
    if (display.preset === "progress") {
      if (display.percentStorage !== "ratio") return null;
      const start = display.progressStart ?? 0;
      const target = display.progressTarget ?? 1;
      if (!Number.isFinite(start) || !Number.isFinite(target) || start >= target) return null;
      if (!Number.isFinite(target - start)) return null;
      const ratio = numeric;
      return cachedFormatter(locale, { style: "percent", minimumFractionDigits: min,
        maximumFractionDigits: max, useGrouping: grouping }).format(ratio);
    }
    if (display.preset === "percent") {
      // Both storages render through Intl percent styling so spacing and sign
      // position stay locale-correct. percent storage means the stored figure
      // already is the displayed percentage: rescale ÷100 for display only.
      const stored = display.percentStorage === "percent" ? numeric / 100 : numeric;
      return cachedFormatter(locale, {
        style: "percent",
        minimumFractionDigits: min,
        maximumFractionDigits: max,
        useGrouping: grouping,
      }).format(stored);
    }
    if (display.preset === "currency" && typeof display.currency === "string" && display.currency) {
      return cachedFormatter(locale, {
        style: "currency",
        currency: display.currency,
        minimumFractionDigits: min,
        maximumFractionDigits: max,
        useGrouping: grouping,
      }).format(numeric);
    }
    const formatted = cachedFormatter(locale, {
      minimumFractionDigits: min,
      maximumFractionDigits: max,
      useGrouping: grouping,
    }).format(numeric);
    return typeof display.unit === "string" && display.unit ? `${formatted}${display.unit}` : formatted;
  } catch {
    return null;
  }
}
