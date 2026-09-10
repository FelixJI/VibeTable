import { BridgeOperationError } from "@/bridge/hostBridge";

export interface RelationInspectionEndpoint {
  tableId: string;
  fieldId: string;
  schemaRevision: string;
  dataRevision: number;
}
export interface RelationInspectionCursor {
  pairId: string;
  endpoints: [RelationInspectionEndpoint, RelationInspectionEndpoint];
  after: [string, string];
  done: [boolean, boolean];
  incomplete: boolean;
}
export interface RelationInspectionRequest {
  tableId: string;
  fieldId: string;
  limit: number;
  cursor?: RelationInspectionCursor;
}
export const findingLabels = {
  metadata_asymmetric: "关系元数据不对称", metadata_invalid: "关系元数据无效",
  dangling: "目标记录缺失", duplicate: "重复链接", one_conflict: "单选关系冲突",
  missing_reciprocal: "反向链接缺失", presence_mismatch: "关系值与存在标记不一致",
  invalid_value: "关系值无法解析", scan_limit: "达到本页检查上限",
} as const;
export type RelationInspectionCode = keyof typeof findingLabels;
export interface RelationInspectionFinding {
  code: RelationInspectionCode;
  endpoint: number;
  recordId?: string;
  targetId?: string;
  detail?: string;
}
export interface RelationInspectionReport {
  pairId: string;
  endpoints: [RelationInspectionEndpoint, RelationInspectionEndpoint];
  counts: Partial<Record<RelationInspectionCode, number>>;
  samples: RelationInspectionFinding[];
  samplesTruncated: boolean;
  rowsScanned: [number, number];
  pageComplete: boolean;
  finished: boolean;
  complete: boolean;
  next?: RelationInspectionCursor;
}
function invalid(): never { throw new Error("关系检查返回了无效结果，请重新检查。"); }
function object(value: unknown, required: string[], optional: string[] = []): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return invalid();
  const result = value as Record<string, unknown>;
  if (required.some((key) => !Object.hasOwn(result, key))
    || Object.keys(result).some((key) => !required.includes(key) && !optional.includes(key))) return invalid();
  return result;
}
function string(value: unknown, max = 256): string {
  if (typeof value !== "string" || Array.from(value).length > max || value.includes("\0")) return invalid();
  return value;
}
function integer(value: unknown, max = Number.MAX_SAFE_INTEGER): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0 || value > max) return invalid();
  return value;
}
function boolean(value: unknown): boolean {
  if (typeof value !== "boolean") return invalid();
  return value;
}
function pair<T>(value: unknown, parse: (item: unknown) => T): [T, T] {
  if (!Array.isArray(value) || value.length !== 2) return invalid();
  return [parse(value[0]), parse(value[1])];
}
function endpoint(value: unknown): RelationInspectionEndpoint {
  const item = object(value, ["tableId", "fieldId", "schemaRevision", "dataRevision"]);
  return { tableId: string(item.tableId, 128), fieldId: string(item.fieldId, 128),
    schemaRevision: string(item.schemaRevision), dataRevision: integer(item.dataRevision) };
}
function cursor(value: unknown): RelationInspectionCursor {
  const item = object(value, ["pairId", "endpoints", "after", "done", "incomplete"]);
  const result = { pairId: string(item.pairId, 128), endpoints: pair(item.endpoints, endpoint),
    after: pair(item.after, (id) => string(id, 200)), done: pair(item.done, boolean), incomplete: boolean(item.incomplete) };
  if (!result.pairId || result.done.every(Boolean)) return invalid();
  return result;
}
function code(value: unknown): RelationInspectionCode {
  if (typeof value !== "string" || !Object.hasOwn(findingLabels, value)) return invalid();
  return value as RelationInspectionCode;
}
function finding(value: unknown): RelationInspectionFinding {
  const item = object(value, ["code", "endpoint"], ["recordId", "targetId", "detail"]);
  const result: RelationInspectionFinding = { code: code(item.code), endpoint: integer(item.endpoint, 1) };
  for (const key of ["recordId", "targetId", "detail"] as const) {
    if (Object.hasOwn(item, key)) result[key] = string(item[key], 65536);
  }
  return result;
}
export function parseRelationInspectionReport(value: unknown): RelationInspectionReport {
  // ProductDataRpcRegistry resolves mapped domain failures as { error: ... }.
  if (value && typeof value === "object" && Object.hasOwn(value, "error")) {
    const envelope = object(value, ["error"]);
    const error = object(envelope.error, ["code", "path", "message", "details", "retryable"]);
    const errorCode = string(error.code);
    const message = string(error.message, 65536);
    string(error.path, 65536);
    boolean(error.retryable);
    if (!errorCode.trim() || !message.trim() || (error.details !== null
      && (typeof error.details !== "object" || Array.isArray(error.details)))) return invalid();
    throw new BridgeOperationError({ code: errorCode, message });
  }
  const item = object(value, ["pairId", "endpoints", "counts", "samples", "samplesTruncated",
    "rowsScanned", "pageComplete", "finished", "complete"], ["next"]);
  const counts = object(item.counts, [], Object.keys(findingLabels));
  const parsedCounts: RelationInspectionReport["counts"] = {};
  for (const [key, count] of Object.entries(counts)) parsedCounts[code(key)] = integer(count);
  if (!Array.isArray(item.samples) || item.samples.length > 50) return invalid();
  const result: RelationInspectionReport = {
    pairId: string(item.pairId, 128), endpoints: pair(item.endpoints, endpoint), counts: parsedCounts,
    samples: item.samples.map(finding), samplesTruncated: boolean(item.samplesTruncated),
    rowsScanned: pair(item.rowsScanned, (count) => integer(count, 200)),
    pageComplete: boolean(item.pageComplete), finished: boolean(item.finished), complete: boolean(item.complete),
  };
  if (item.next !== undefined) result.next = cursor(item.next);
  if (result.finished === Boolean(result.next) || (result.complete && (!result.finished || !result.pageComplete))) return invalid();
  if (result.next && (result.pairId !== result.next.pairId
    || JSON.stringify(result.endpoints) !== JSON.stringify(result.next.endpoints)
    || (!result.pageComplete && !result.next.incomplete))) return invalid();
  return result;
}
