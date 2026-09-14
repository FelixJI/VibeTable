/** The Host's JSON numeric tokens must survive device-local state round trips. */
export class GridStateNumberError extends Error {
  constructor() {
    super("This WebView2 runtime cannot preserve exact grid filter numbers. Update WebView2 and reload the saved layout.");
    this.name = "GridStateNumberError";
  }
}

/** Compare decimal values without rounding either the coefficient or exponent. */
function normalizedDecimal(source: string): string {
  // Both callers supply a numeric token already validated by JSON.parse/stringify.
  const parts = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(source)!;
  const fraction = parts[3] ?? "";
  const digits = (parts[2]! + fraction).replace(/^0+/, "");
  if (!digits) return `${parts[1]}0`;
  const coefficient = digits.replace(/0+$/, "");
  const exponent = BigInt(parts[4] ?? "0") - BigInt(fraction.length)
    + BigInt(digits.length - coefficient.length);
  return `${parts[1]}${coefficient}e${exponent}`;
}

/** Scoped to gridState replies and their snapshots; other protocols keep their parsers. */
export function parseGridStateJson(text: string): unknown {
  return JSON.parse(text, (_key: string, value: unknown, context?: { source?: string }) => {
    if (typeof value !== "number") return value;
    // A rounded Number alone cannot tell us whether even a small integer was exact.
    if (!context?.source) throw new GridStateNumberError();
    if (Number.isFinite(value)
      && normalizedDecimal(context.source) === normalizedDecimal(JSON.stringify(value))) return value;
    const rawJSON = (JSON as JSON & { rawJSON?: (source: string) => object }).rawJSON;
    if (!rawJSON) throw new GridStateNumberError();
    return rawJSON(context.source);
  });
}

export function gridStateNumberText(value: unknown): string | null {
  const isRawJSON = (JSON as JSON & { isRawJSON?: (value: unknown) => boolean }).isRawJSON;
  return isRawJSON?.(value) ? JSON.stringify(value) : null;
}
