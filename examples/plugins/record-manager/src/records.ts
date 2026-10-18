import { PluginCapabilityError } from "@vibetable/plugin-sdk";
import type {
  ComputedCell,
  DataDescription,
  DataField,
  DataQueryPage,
  DataQueryRequest,
  DataSort,
  JsonObject,
  JsonValue,
  SurfaceMutationPlan,
} from "@vibetable/plugin-sdk";

export { describeCellValue, formatCellValue, isMissingCell } from "@vibetable/plugin-sdk";

export type PageQuery = (
  request: DataQueryRequest,
  signal?: AbortSignal,
) => Promise<DataQueryPage & { readonly rowGuards: Readonly<Record<string, string>> }>;

export interface FoldResult {
  readonly rows: readonly JsonObject[];
  readonly guards: Readonly<Record<string, string>>;
  readonly pagesRead: number;
  /** Authoritative total reported by the host, never derived from pages read. */
  readonly totalRows: number;
}

/** Follow the opaque cursor until it is exhausted; refuses to report a full
 * result while a projected computed value is not fresh (`page.complete`). */
export async function foldAllPages(
  query: PageQuery,
  releaseCursor: (cursor: string) => Promise<void>,
  collection: string,
  fields: readonly string[],
  options?: { readonly sorts?: readonly DataSort[]; readonly pageSize?: number; readonly signal?: AbortSignal },
): Promise<FoldResult> {
  const pageSize = options?.pageSize ?? 200;
  const rows: JsonObject[] = [];
  const guards: Record<string, string> = {};
  let pagesRead = 0;
  let totalRows = -1;
  let cursor: string | undefined;
  try {
    do {
      const input = cursor;
      cursor = undefined;
      const page = await query(
        { contract: "vibetable.plugin-query.v2", collection, fields, sorts: options?.sorts ?? [], pageSize, ...(input ? { cursor: input } : {}) }, options?.signal,
      ).catch(async error => { if (input) await releaseCursor(input); throw error; });
      cursor = page.nextCursor ?? undefined;
      if (!page.complete) throw new Error("计算数据尚未就绪；未把不完整结果当成全部记录。");
      if (!Number.isSafeInteger(page.totalRows) || page.totalRows < 0) throw new Error("宿主未返回权威总行数。");
      totalRows = page.totalRows;
      rows.push(...page.items);
      for (const [id, digest] of Object.entries(page.rowGuards ?? {})) guards[id] = digest;
      pagesRead += 1;
    } while (cursor);
    return { rows, guards, pagesRead, totalRows };
  } finally { if (cursor) await releaseCursor(cursor); }
}

export interface PagerView {
  readonly items: readonly JsonObject[];
  readonly guards: Readonly<Record<string, string>>;
  readonly totalRows: number;
  readonly hasNext: boolean;
  readonly hasPrev: boolean;
  readonly pageIndex: number;
  readonly empty: boolean;
}

/** Own at most one continuation. Forward consumes it directly; backward
 * replays only the prefix to its target, consuming every intermediate handle. */
export function createPager(
  query: PageQuery,
  releaseCursor: (cursor: string) => Promise<void>,
  collection: string,
  fields: readonly string[],
  pageSize = 50,
  sorts: readonly DataSort[] = [],
) {
  let current: PagerView | null = null;
  let nextCursor: string | null = null;
  let abandoned: string | null = null;
  let active: AbortController | null = null;
  let closed = false;
  let settled: Promise<void> = Promise.resolve();
  let cleanupError: unknown = null;
  async function releaseSaved(): Promise<void> {
    if (abandoned) { await releaseCursor(abandoned); abandoned = null; }
    if (nextCursor) { await releaseCursor(nextCursor); nextCursor = null; }
  }
  async function move(target: number, forward: boolean, signal?: AbortSignal): Promise<PagerView> {
    if (closed) throw new PluginCapabilityError("plugin_surface_closed", "分页会话已关闭。");
    if (active) throw new Error("分页查询仍在进行。");
    const controller = new AbortController(); active = controller;
    cleanupError = null;
    let finish!: () => void;
    settled = new Promise<void>(resolve => { finish = resolve; });
    const abort = (): void => controller.abort();
    if (signal?.aborted) abort();
    signal?.addEventListener("abort", abort, { once: true });
    let cursor: string | null = null;
    try {
      if (abandoned) { await releaseCursor(abandoned); abandoned = null; }
      const continuation = forward && nextCursor !== null;
      if (continuation) { cursor = nextCursor; nextCursor = null; }
      else await releaseSaved();
      let page: Awaited<ReturnType<PageQuery>> | null = null;
      for (let index = continuation ? target : 0; index <= target; index += 1) {
        const input = cursor; cursor = null;
        try {
          page = await query({ contract: "vibetable.plugin-query.v2", collection, fields, sorts, pageSize, ...(input ? { cursor: input } : {}) }, controller.signal);
        } catch (error) {
          if (input) {
            try { await releaseCursor(input); }
            catch (releaseError) { abandoned = input; cleanupError = releaseError; throw releaseError; }
          }
          throw error;
        }
        cursor = page.nextCursor;
        if (!Number.isSafeInteger(page.totalRows) || page.totalRows < 0) throw new Error("宿主未返回权威总行数。");
        if (closed || controller.signal.aborted) throw new PluginCapabilityError("plugin_cancel_requested", "查询已取消。");
        if (!cursor && index < target) throw new Error("请求的页已不存在；数据已变化，请刷新。");
      }
      if (!page) throw new Error("分页结果不可用。");
      current = { items: page.items, guards: page.rowGuards, totalRows: page.totalRows, hasNext: cursor !== null, hasPrev: target > 0, pageIndex: target, empty: page.items.length === 0 };
      nextCursor = cursor; cursor = null;
      return current;
    } finally {
      signal?.removeEventListener("abort", abort);
      try {
        if (cursor) {
          try { await releaseCursor(cursor); }
          catch (error) { abandoned = cursor; cleanupError = error; throw error; }
        }
      } finally { if (active === controller) active = null; finish(); }
    }
  }
  return {
    get view(): PagerView | null { return current; },
    open(signal?: AbortSignal): Promise<PagerView> { return move(0, false, signal); },
    async next(signal?: AbortSignal): Promise<PagerView> {
      if (!current?.hasNext) throw new Error("已是末页。");
      return move(current.pageIndex + 1, true, signal);
    },
    async prev(signal?: AbortSignal): Promise<PagerView> {
      if (!current || current.pageIndex === 0) throw new Error("已是第一页。");
      return move(current.pageIndex - 1, false, signal);
    },
    async close(): Promise<void> {
      const waiting = active !== null;
      closed = true; active?.abort();
      await settled;
      if (waiting && cleanupError) throw cleanupError;
      await releaseSaved();
    },
  };
}

export function normalizeWhitespace(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

export interface NormalizationPreview {
  readonly changed: readonly { readonly id: string; readonly before: string; readonly after: string }[];
  readonly unchanged: number;
  readonly skipped: number;
  readonly plan: SurfaceMutationPlan | null;
}

/** Build a guarded single-table update plan from raw rows. Only genuinely
 * changed raw strings produce operations; display text never flows back. */
export function buildNormalizationPlan(
  description: DataDescription,
  rows: readonly JsonObject[],
  guards: Readonly<Record<string, string>>,
  fieldId: string,
): NormalizationPreview {
  const field = description.fields.find(item => item.fieldId === fieldId);
  if (!field || field.readonly || field.logicalType !== "text") throw new Error("当前授权没有可规范化的文本字段。");
  const changed: { id: string; before: string; after: string }[] = [];
  const operations: { kind: "update"; primaryKey: string; expectedDigest: string; values: JsonObject }[] = [];
  let unchanged = 0;
  let skipped = 0;
  for (const row of rows) {
    const raw = row[fieldId];
    if (typeof raw !== "string") {
      if (raw === null || raw === undefined) unchanged += 1;
      else skipped += 1;
      continue;
    }
    const after = normalizeWhitespace(raw);
    if (after === raw) {
      unchanged += 1;
      continue;
    }
    const digest = guards[String(row.id)];
    if (!digest) {
      skipped += 1;
      continue;
    }
    if (operations.length === 1000) throw new Error("批量规范化超过单次 1000 条变更上限；本示例不自动分批，未提交任何修改。");
    changed.push({ id: String(row.id), before: raw, after });
    operations.push({ kind: "update", primaryKey: String(row.id), expectedDigest: digest, values: { [fieldId]: after } });
  }
  return {
    changed,
    unchanged,
    skipped,
    plan: operations.length
      ? {
          contract: "vibetable.surface-mutation-plan.v1",
          collection: description.collection,
          schemaRevision: description.schemaRevision,
          operations,
        }
      : null,
  };
}

export function editableFields(description: DataDescription): readonly DataField[] {
  return description.fields.filter(field => field.fieldId !== "id" && !field.readonly);
}

export function relationFields(description: DataDescription): readonly DataField[] {
  return description.fields.filter(field => field.logicalType === "relation");
}

export function computedFields(description: DataDescription): readonly DataField[] {
  return description.fields.filter(field => field.logicalType === "formula" || field.logicalType === "lookup");
}

/** Form values stay raw: nullable fields have an explicit null checkbox. */
export function formInputValue(field: DataField, raw: JsonValue | undefined): string {
  if (raw === null || raw === undefined) return "";
  return typeof raw === "object" || ["json", "multiSelect", "geoPoint"].includes(field.logicalType) || field.resultType === "array" ? JSON.stringify(raw) : String(raw);
}

export class FormValueError extends Error {
  constructor(readonly fieldId: string, message: string) { super(message); }
}

export function parseFormValue(field: DataField, text: string, isNull = false): JsonValue {
  const invalid = (reason: string): never => { throw new FormValueError(field.fieldId, `字段 ${field.displayName} ${reason}`); };
  if (isNull) return field.nullable ? null : invalid("不允许 null。");
  if (field.logicalType === "number") {
    const value = text.trim() === "" ? NaN : Number(text);
    return Number.isFinite(value) ? value : invalid("需要数字。");
  }
  if (field.logicalType === "bool" || field.logicalType === "boolean") {
    return text === "true" ? true : text === "false" ? false : invalid("需要布尔值。");
  }
  if (["json", "multiSelect", "geoPoint"].includes(field.logicalType) || field.resultType === "array") {
    let value: JsonValue;
    try { value = JSON.parse(text); } catch { return invalid("需要有效 JSON。"); }
    if ((field.resultType === "array" || field.logicalType === "multiSelect") && !Array.isArray(value)) return invalid("需要 JSON 数组。");
    return value;
  }
  if (field.logicalType === "relation") {
    if (!text.trim()) return invalid("需要目标记录 ID，清空请显式选择 null。");
    return text;
  }
  return text;
}

export interface FormEdit { readonly text: string; readonly isNull: boolean; readonly changed: boolean }

/** Updates omit untouched fields, including JSON/relations and empty text. */
export function formValues(description: DataDescription, edits: ReadonlyMap<string, FormEdit>, creating: boolean): JsonObject {
  const values: Record<string, JsonValue> = {};
  for (const field of editableFields(description)) {
    const edit = edits.get(field.fieldId);
    if (edit && (creating || edit.changed)) values[field.fieldId] = parseFormValue(field, edit.text, edit.isNull);
  }
  return values;
}

export interface OverviewSummary {
  readonly collection: string;
  readonly displayName: string;
  readonly fieldCount: number;
  readonly editableCount: number;
  readonly relations: readonly { readonly fieldId: string; readonly displayName: string }[];
  /** Authoritative host count, never derived from a truncated page read. */
  readonly totalRows: number;
  readonly authoritative: true;
}

/** Summary built from metadata plus one authoritative `totalRows` probe. */
export function overviewSummary(
  description: DataDescription,
  count: { readonly totalRows: number; readonly complete: boolean },
): OverviewSummary {
  if (!count.complete) throw new Error("权威计数不可用；概览不把不完整结果当作全量。");
  return {
    collection: description.collection,
    displayName: description.displayName,
    fieldCount: description.fields.length,
    editableCount: editableFields(description).length,
    relations: relationFields(description).map(field => ({ fieldId: field.fieldId, displayName: String(field.displayName ?? field.fieldId) })),
    totalRows: count.totalRows,
    authoritative: true,
  };
}

export type MutationOutcomeView =
  | { readonly status: "succeeded" }
  | { readonly status: "rejected" }
  | { readonly status: "unknown"; readonly retry: false }
  | { readonly status: "cancelled"; readonly retry: false }
  | { readonly status: "conflict"; readonly code: string }
  | { readonly status: "error"; readonly code: string };

/** Map a submit result or error to a user-facing outcome. `unknown` is never
 * auto-replayed: the caller must verify authoritative data first. */
export function describeMutationFailure(error: unknown): MutationOutcomeView {
  if (error instanceof Error || (typeof error === "object" && error !== null && "code" in error)) {
    const code = String((error as { code?: unknown }).code ?? "");
    if (code === "plugin_commit_unknown") return { status: "unknown", retry: false };
    if (code === "plugin_cancel_requested") return { status: "cancelled", retry: false };
    if (code === "mutation.digest_conflict" || code === "plugin_plan_stale") return { status: "conflict", code };
    return { status: "error", code: code || error_name(error) };
  }
  return { status: "error", code: error_name(error) };
}

function error_name(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function describeSubmitOutcome(result: { readonly status: "succeeded" | "rejected" }): MutationOutcomeView {
  return result.status === "succeeded" ? { status: "succeeded" } : { status: "rejected" };
}

export type { ComputedCell, DataDescription, DataField, JsonObject, JsonValue, SurfaceMutationPlan };
