/** The Host's JSON numeric tokens must survive device-local state round trips. */
export class GridStateNumberError extends Error {
  constructor() {
    super("This WebView2 runtime cannot preserve large grid filter numbers. Update WebView2 and reload the saved layout.");
    this.name = "GridStateNumberError";
  }
}

/** Scoped to gridState replies and their snapshots; other protocols keep their parsers. */
export function parseGridStateJson(text: string): unknown {
  return JSON.parse(text, (_key: string, value: unknown, context?: { source?: string }) => {
    if (typeof value !== "number" || Number.isSafeInteger(value) || !Number.isInteger(value)) return value;
    const rawJSON = (JSON as JSON & { rawJSON?: (source: string) => object }).rawJSON;
    if (!context?.source || !rawJSON) throw new GridStateNumberError();
    return rawJSON(context.source);
  });
}

export function gridStateNumberText(value: unknown): string | null {
  const isRawJSON = (JSON as JSON & { isRawJSON?: (value: unknown) => boolean }).isRawJSON;
  return isRawJSON?.(value) ? JSON.stringify(value) : null;
}
