import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createBridgeFormulaPreviewPort,
  FormulaPreviewCoordinator,
  type FormulaPreviewPort,
  type FormulaPreviewRequest,
} from "./formulaPreviewCoordinator";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((ok, fail) => {
    resolve = ok;
    reject = fail;
  });
  return { promise, resolve, reject };
}

describe("FormulaPreviewCoordinator", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("debounces rapid edits and sends only the latest definition", async () => {
    vi.useFakeTimers();
    let sent: FormulaPreviewRequest | undefined;
    const preview = vi.fn(async (
      value: FormulaPreviewRequest,
      _signal: AbortSignal,
    ) => {
      sent = value;
      return { values: { total: 6 } };
    });
    const coordinator = new FormulaPreviewCoordinator({ preview }, 250);
    const onResult = vi.fn();

    coordinator.schedule(request("qty * 2"), { onResult });
    await vi.advanceTimersByTimeAsync(200);
    coordinator.schedule(request("qty * 3"), { onResult });
    await vi.advanceTimersByTimeAsync(249);
    expect(preview).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);

    expect(preview).toHaveBeenCalledTimes(1);
    expect(sent?.field.formula?.source).toBe("qty * 3");
  });

  it("aborts the prior request and suppresses a stale response", async () => {
    vi.useFakeTimers();
    const first = deferred<{ values: Readonly<Record<string, unknown>> }>();
    const second = deferred<{ values: Readonly<Record<string, unknown>> }>();
    const signals: AbortSignal[] = [];
    const port: FormulaPreviewPort = {
      preview: vi.fn((_request, signal) => {
        signals.push(signal);
        return signals.length === 1 ? first.promise : second.promise;
      }),
    };
    const coordinator = new FormulaPreviewCoordinator(port, 10);
    const onResult = vi.fn();

    coordinator.schedule(request("qty * 2"), { onResult });
    await vi.advanceTimersByTimeAsync(10);
    coordinator.schedule(request("qty * 3"), { onResult });
    await vi.advanceTimersByTimeAsync(10);
    expect(signals[0]?.aborted).toBe(true);

    first.resolve({ values: { total: 4 } });
    second.resolve({ values: { total: 6 } });
    await Promise.resolve();
    await Promise.resolve();

    expect(onResult).toHaveBeenCalledTimes(1);
    expect(onResult).toHaveBeenCalledWith({ values: { total: 6 } });
  });

  it("does not surface AbortError as a user-visible validation failure", async () => {
    vi.useFakeTimers();
    const port: FormulaPreviewPort = {
      preview: vi.fn((_request, signal): Promise<{ values: Readonly<Record<string, unknown>> }> => new Promise((_resolve, reject) => {
        signal.addEventListener("abort", () =>
          reject(new DOMException("cancelled", "AbortError")));
      })),
    };
    const coordinator = new FormulaPreviewCoordinator(port, 1);
    const onError = vi.fn();
    coordinator.schedule(request("qty * 2"), { onResult: vi.fn(), onError });
    await vi.advanceTimersByTimeAsync(1);
    coordinator.schedule(request("qty * 3"), { onResult: vi.fn(), onError });
    await vi.advanceTimersByTimeAsync(1);
    await Promise.resolve();

    expect(onError).not.toHaveBeenCalled();
    coordinator.dispose();
  });

  it("rejects mapped product errors instead of treating them as preview values", async () => {
    const bridge = {
      request: vi.fn().mockResolvedValue({
        error: {
          code: "formula.null_member",
          message: "Author is required before reading its name.",
          path: "author_label",
          retryable: false,
        },
      }),
    };
    const port = createBridgeFormulaPreviewPort(bridge);

    await expect(port.preview(request("author.name"), new AbortController().signal))
      .rejects.toMatchObject({
        name: "formula.null_member",
        message: "Author is required before reading its name.",
      });
  });
});

function request(source: string): FormulaPreviewRequest {
  return {
    tableId: "tbl_preview",
    field: {
      contract: "vibetable.schema.v2",
      identity: {
        fieldId: "fld_formula_preview",
        physicalName: "f_formula_preview",
        providerFieldId: "pb_formula_preview",
      },
      displayName: "Total",
      help: "",
      logicalType: "formula",
      lifecycle: { state: "active", retiredAt: null },
      value: {
        required: false,
        default: { enabled: false, value: null, source: "recommended", defaultsVersion: 1 },
        presence: { mode: "computed" },
      },
      constraints: {
        unique: { enabled: false, blankPolicy: "ignoreMissing" },
        range: { min: null, max: null },
        length: { min: null, max: null },
        pattern: { enabled: false, value: "" },
        domains: { only: [], except: [] },
        selection: { min: 0, max: null },
      },
      storage: {
        kind: "computed",
        options: { onlyInt: false, maxSize: 0, convertURLs: false, presentable: false },
      },
      display: {
        kind: "readonly", preset: "number", displayScale: 2, scaleMode: "max",
        trimTrailingZeros: true, useGrouping: true, currency: "",
        percentStorage: "ratio", unit: null, precision: "exact", timezone: "UTC",
        mode: "default", trueLabel: "true", falseLabel: "false",
      },
      formula: { language: "cel-v1", source, resultType: "number" },
    },
    row: { qty: 2 },
    changedFieldIds: ["fld_qty"],
  };
}
