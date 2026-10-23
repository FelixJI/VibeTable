import { describe, expect, it } from "vitest";
import { ProductRpcError, ProductRpcProtocolError, unwrapProductRpcResult } from "./productRpcResult";

const decodeString = (value: unknown) => (typeof value === "string" ? value : null);

describe("unwrapProductRpcResult", () => {
  it("passes a verifiable success value through", () => {
    expect(unwrapProductRpcResult("applied", decodeString, "preset.delete")).toBe("applied");
  });

  it("throws ProductRpcError for a legal envelope with full semantics preserved", () => {
    expect(() => unwrapProductRpcResult({
      error: {
        code: "preset_edit_conflict",
        path: "expectedRevision",
        message: "Preset changed elsewhere.",
        details: { expected: "rev-1" },
        retryable: true,
      },
    }, decodeString, "preset.save")).toThrow(ProductRpcError);
  });

  it("maps a legal envelope to ProductRpcError fields exactly", () => {
    try {
      unwrapProductRpcResult({
        error: {
          code: "settings.calendar.revision_conflict",
          path: "expectedRevision",
          message: "reload",
          details: null,
          retryable: false,
        },
      }, decodeString, "settings.commitWorkCalendar");
      expect.unreachable();
    } catch (error) {
      expect(error).toBeInstanceOf(ProductRpcError);
      const rpc = error as ProductRpcError;
      expect(rpc.code).toBe("settings.calendar.revision_conflict");
      expect(rpc.path).toBe("expectedRevision");
      expect(rpc.message).toBe("reload");
      expect(rpc.details).toBeNull();
      expect(rpc.retryable).toBe(false);
    }
  });

  it("keeps a legal domain error intact when optional fields are omitted or null", () => {
    try {
      unwrapProductRpcResult({
        error: { code: "version_main_conflict", path: "", message: "The record changed after comparison.", details: null },
      }, decodeString, "version.promote");
      expect.unreachable();
    } catch (error) {
      expect(error).toBeInstanceOf(ProductRpcError);
      const rpc = error as ProductRpcError;
      expect(rpc.details).toBeNull();
      expect(rpc.retryable).toBe(false);
    }
    try {
      unwrapProductRpcResult({
        error: { code: "version_main_conflict", path: "", message: "The record changed after comparison.", details: { field: "title" } },
      }, decodeString, "version.promote");
      expect.unreachable();
    } catch (error) {
      expect((error as ProductRpcError).details).toEqual({ field: "title" });
    }
  });

  it("defaults retryable to false when the host omits it", () => {
    try {
      unwrapProductRpcResult({
        error: { code: "version_main_conflict", path: "", message: "The record changed after comparison." },
      }, decodeString, "version.promote");
      expect.unreachable();
    } catch (error) {
      expect((error as ProductRpcError).retryable).toBe(false);
      expect((error as ProductRpcError).details).toBeNull();
    }
  });

  it("reports a malformed error envelope as a distinguishable protocol error", () => {
    for (const malformed of [
      { error: { code: "authority.failed" } },
      { error: "boom" },
      { error: null },
      { error: { code: "c", path: "p" } },
      { error: { code: 1, path: "p", message: "m" } },
      { error: ["nope"] },
      { error: { code: "c", path: "p", message: "m", details: 42 } },
      { error: { code: "c", path: "p", message: "m", details: [] } },
      { error: { code: "c", path: "p", message: "m", details: "expected" } },
      { error: { code: "c", path: "p", message: "m", retryable: "true" } },
      { error: { code: "c", path: "p", message: "m", retryable: 1 } },
    ]) {
      try {
        unwrapProductRpcResult(malformed, decodeString, "preset.list");
        expect.unreachable();
      } catch (error) {
        expect(error).toBeInstanceOf(ProductRpcProtocolError);
        expect((error as ProductRpcProtocolError).reason).toBe("malformed-error-envelope");
        expect((error as ProductRpcProtocolError).message).toContain("preset.list");
      }
    }
  });

  it("reports a contract-violating success value as a protocol error", () => {
    try {
      unwrapProductRpcResult({ presets: [] }, decodeString, "preset.list");
      expect.unreachable();
    } catch (error) {
      expect(error).toBeInstanceOf(ProductRpcProtocolError);
      expect((error as ProductRpcProtocolError).reason).toBe("invalid-success-payload");
    }
  });
});
