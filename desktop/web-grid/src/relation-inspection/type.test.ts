import { describe, expect, it } from "vitest";
import { parseRelationInspectionReport } from "./type";
const endpoint = { tableId: "t", fieldId: "f", schemaRevision: "s", dataRevision: 1 };
const finished = { pairId: "pair", endpoints: [endpoint, endpoint], counts: {}, samples: [],
  samplesTruncated: false, rowsScanned: [0, 200], pageComplete: true, finished: true, complete: true };

describe("parseRelationInspectionReport", () => {
  it("accepts a complete scan that contains issues, without inventing health", () => {
    const result = parseRelationInspectionReport({ ...finished, counts: { presence_mismatch: 1 },
      samples: [{ code: "presence_mismatch", endpoint: 1, recordId: "r", targetId: "t", detail: "hidden link" }] });
    expect(result.complete).toBe(true);
    expect(result.counts.presence_mismatch).toBe(1);
  });
  it.each([
    null, [], {}, { ...finished, surprise: true }, { ...finished, counts: null },
    { ...finished, counts: { unknown: 1 } }, { ...finished, counts: { duplicate: -1 } },
    { ...finished, counts: { duplicate: Number.MAX_SAFE_INTEGER + 1 } },
    { ...finished, pairId: 1 }, { ...finished, pairId: "x".repeat(129) },
    { ...finished, endpoints: [endpoint] }, { ...finished, endpoints: [{ ...endpoint, fieldId: "a\0b" }, endpoint] },
    { ...finished, samplesTruncated: "false" }, { ...finished, rowsScanned: [201, 0] },
    { ...finished, samples: {} }, { ...finished, samples: Array.from({ length: 51 }, () => ({ code: "dangling", endpoint: 0 })) },
    { ...finished, samples: [{ code: "unknown", endpoint: 0 }] },
    { ...finished, samples: [{ code: "dangling", endpoint: 2 }] },
    { ...finished, samples: [{ code: "dangling", endpoint: 0, detail: null }] },
    { ...finished, finished: false }, { ...finished, pageComplete: false },
    { ...finished, next: null },
  ])("rejects malformed or contradictory result %#", (value) => {
    expect(() => parseRelationInspectionReport(value)).toThrow("无效结果");
  });
  const next = { pairId: "pair", endpoints: [endpoint, endpoint], after: ["a", "b"], done: [false, true], incomplete: false };
  it("accepts exact structured progress without re-encoding it", () => {
    expect(parseRelationInspectionReport({ ...finished, finished: false, complete: false, next }).next).toEqual(next);
  });
  it.each([
    { ...next, pairId: "" }, { ...next, pairId: "other" }, { ...next, done: [true, true] },
    { ...next, endpoints: [{ ...endpoint, dataRevision: 2 }, endpoint] },
    { ...next, after: ["a"] },
  ])("rejects foreign or unusable continuation %#", (value) => {
    expect(() => parseRelationInspectionReport({ ...finished, finished: false, complete: false, next: value })).toThrow();
  });
  it("requires incomplete coverage to propagate into the continuation", () => {
    expect(() => parseRelationInspectionReport({ ...finished, pageComplete: false, finished: false, complete: false, next })).toThrow();
  });
});

it("converts the exact Host error envelope into the existing bridge domain error", () => {
  expect(() => parseRelationInspectionReport({ error: { code: "relation.inspect.storage_failed", path: "",
    message: "storage unavailable", details: null, retryable: false } })).toThrow("storage unavailable");
});
it.each([
  { error: null },
  { error: { code: "x", path: "", message: "message", details: {}, retryable: false }, extra: 1 },
  { error: { code: "x", path: "", message: "message", details: [], retryable: false } },
  { error: { code: "x", path: "", message: "message", details: "private", retryable: false } },
  { error: { code: "", path: "", message: "message", details: null, retryable: false } },
  { error: { code: "x", path: "", message: " ", details: null, retryable: false } },
  { error: { code: "x", path: 1, message: "message", details: null, retryable: false } },
  { error: { code: "x", path: "", message: "message", details: null, retryable: "false" } },
])("rejects malformed mapped errors %#", (value) => {
  expect(() => parseRelationInspectionReport(value)).toThrow("无效结果");
});
