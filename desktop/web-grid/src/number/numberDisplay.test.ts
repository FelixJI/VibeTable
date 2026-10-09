import { describe, expect, it } from "vitest";
import type { DisplaySpec } from "@/contracts/generated/schemaV2";
import { formatNumberDisplay } from "./numberDisplay";

function display(patch: Partial<DisplaySpec> = {}): DisplaySpec {
  return {
    kind: "number",
    preset: "number",
    displayScale: 2,
    scaleMode: "max",
    trimTrailingZeros: true,
    useGrouping: true,
    currency: "CNY",
    percentStorage: "ratio",
    unit: null,
    precision: "exact",
    timezone: "system",
    mode: "default",
    indent: 0,
    trueLabel: "是",
    falseLabel: "否",
    ...patch,
  };
}

describe("formatNumberDisplay", () => {
  it("renders the documented fixed/max/grouping combinations (AC2)", () => {
    const fixed = display({ scaleMode: "fixed", trimTrailingZeros: false });
    const max = display({ scaleMode: "max" });
    expect(formatNumberDisplay(1234.56789, fixed, "zh-CN")).toBe("1,234.57");
    expect(formatNumberDisplay(12, fixed, "zh-CN")).toBe("12.00");
    expect(formatNumberDisplay(12, max, "zh-CN")).toBe("12");
    expect(formatNumberDisplay(1234.56789, display({ scaleMode: "fixed", trimTrailingZeros: false, useGrouping: false }), "zh-CN")).toBe("1234.57");
    // Legacy fixed + trimTrailingZeros configs still render as fixed; the
    // settings UI normalizes the flag on save, rendering never degrades it.
    expect(formatNumberDisplay(12, display({ scaleMode: "fixed", trimTrailingZeros: true }), "zh-CN")).toBe("12.00");
    expect(formatNumberDisplay(1234.5, display({ scaleMode: "fixed", trimTrailingZeros: true }), "zh-CN")).toBe("1,234.50");
  });

  it("attaches currency, unit and percent per the documented semantics (AC2/AC3)", () => {
    expect(formatNumberDisplay(1234.56789, display({ preset: "currency", scaleMode: "fixed", trimTrailingZeros: false }), "zh-CN")).toBe("¥1,234.57");
    expect(formatNumberDisplay(1234.56789, display({ preset: "currency", scaleMode: "fixed", trimTrailingZeros: false, currency: "USD" }), "en-US")).toBe("$1,234.57");
    expect(formatNumberDisplay(1234.56789, display({ unit: "kg" }), "zh-CN")).toBe("1,234.57kg");
    // ratio storage: 0.125 is displayed as 12.5% without rewriting the value.
    expect(formatNumberDisplay(0.125, display({ preset: "percent" }), "zh-CN")).toBe("12.5%");
    // percent storage: 12.5 already is the displayed figure; the ÷100
    // rescale happens only inside the pure display function.
    expect(formatNumberDisplay(12.5, display({ preset: "percent", percentStorage: "percent" }), "zh-CN")).toBe("12.5%");
    expect(formatNumberDisplay(0.125, display({ preset: "percent", scaleMode: "fixed", trimTrailingZeros: false }), "zh-CN")).toBe("12.50%");
    // Both storages share Intl percent styling, so locale spacing/position
    // cannot diverge between the two interpretations (fr-FR uses a no-break
    // space before the percent sign and a comma decimal separator).
    expect(formatNumberDisplay(0.125, display({ preset: "percent" }), "fr-FR")).toBe("12,5\u00A0%");
    expect(formatNumberDisplay(12.5, display({ preset: "percent", percentStorage: "percent" }), "fr-FR")).toBe("12,5\u00A0%");
    expect(formatNumberDisplay(0.125, display({ preset: "percent" }), "de-DE")).toBe("12,5\u00A0%");
    expect(formatNumberDisplay(12.5, display({ preset: "percent", percentStorage: "percent" }), "de-DE")).toBe("12,5\u00A0%");
  });

  it("never coerces null, non-numeric or non-finite inputs into numbers (AC4)", () => {
    const spec = display();
    expect(formatNumberDisplay(null, spec, "zh-CN")).toBeNull();
    expect(formatNumberDisplay(undefined, spec, "zh-CN")).toBeNull();
    expect(formatNumberDisplay(Number.NaN, spec, "zh-CN")).toBeNull();
    expect(formatNumberDisplay(Number.POSITIVE_INFINITY, spec, "zh-CN")).toBeNull();
    expect(formatNumberDisplay("12.5", spec, "zh-CN")).toBeNull();
    expect(formatNumberDisplay({}, spec, "zh-CN")).toBeNull();
    expect(formatNumberDisplay(12.5, null, "zh-CN")).toBeNull();
    expect(formatNumberDisplay(12.5, undefined, "zh-CN")).toBeNull();
  });

  it("pins zero, negative zero, negatives and large-number expectations (AC4)", () => {
    const spec = display();
    expect(formatNumberDisplay(0, spec, "zh-CN")).toBe("0");
    // Negative zero is presented as plain zero, never "-0".
    expect(formatNumberDisplay(-0, spec, "zh-CN")).toBe("0");
    expect(formatNumberDisplay(-1234.5, display({ scaleMode: "fixed", trimTrailingZeros: false }), "zh-CN")).toBe("-1,234.50");
    expect(formatNumberDisplay(9007199254740991, display({ displayScale: 0 }), "zh-CN")).toBe("9,007,199,254,740,991");
  });

  it("uses Intl half-expand rounding with the declared locale", () => {
    expect(formatNumberDisplay(1234.56789, display({ displayScale: 3 }), "en-US")).toBe("1,234.568");
    expect(formatNumberDisplay(1234.56789, display({ displayScale: 0 }), "en-US")).toBe("1,235");
    expect(formatNumberDisplay(1234.56789, display({ scaleMode: "fixed", trimTrailingZeros: false }), "de-DE")).toBe("1.234,57");
  });

  it("clamps out-of-range scale defensively without throwing", () => {
    expect(formatNumberDisplay(12, display({ displayScale: 15 }), "zh-CN")).toBe("12");
    expect(formatNumberDisplay(12, display({ displayScale: Number.NaN }), "zh-CN")).toBe("12");
  });
});
