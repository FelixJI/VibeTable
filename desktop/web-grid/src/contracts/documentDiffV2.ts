const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

type JsonRecord = Record<string, unknown>;

export type DocumentDiffFormat = "docx" | "xlsx" | "text" | "binary";
export type DocumentDiffProvider = "builtIn" | "wordNative" | "xlsxBuiltIn";
export type DocumentDiffFidelity = "structural" | "officeNative";
export type DocumentDiffSessionFailure =
  | "unsupported"
  | "invalidContent"
  | "io"
  | "cancelled"
  | "stale"
  | "providerUnavailable"
  | "timeout";

export type DocumentDiffWarning =
  | "resultTruncated"
  | "partialCoverage"
  | "existingRevisionsNormalized"
  | "cachedValuesNotRecalculated";

export type DocumentDiffChangeKind =
  | "insert"
  | "delete"
  | "replace"
  | "move"
  | "format"
  | "table"
  | "comment"
  | "other";

export type DocumentDiffCoverageArea =
  | "visibleText"
  | "structure"
  | "formatting"
  | "tables"
  | "headersFooters"
  | "notes"
  | "textBoxes"
  | "comments"
  | "images"
  | "fields"
  | "pagination"
  | "worksheetValues"
  | "worksheetFormulas"
  | "worksheetStyles"
  | "worksheetMerges"
  | "worksheetVisibility";

export interface DocumentDiffSummary {
  readonly totalChangeGroups: number;
  readonly rawRevisionCount: number;
  readonly insertions: number;
  readonly deletions: number;
  readonly replacements: number;
  readonly moves: number;
  readonly formattingChanges: number;
  readonly tableChanges: number;
  readonly commentChanges: number;
  readonly otherChanges: number;
}

export interface DocumentDiffCoverageEntry {
  readonly area: DocumentDiffCoverageArea;
  readonly status: "covered" | "partial" | "notCovered";
}

export interface DocumentDiffCoverage {
  readonly areas: readonly DocumentDiffCoverageEntry[];
  readonly truncated: boolean;
}

export interface DocumentDiffRichRun {
  readonly text: string;
  readonly role: "context" | "inserted" | "deleted" | "changed";
  readonly bold: boolean | null;
  readonly italic: boolean | null;
  readonly underline: boolean | null;
  readonly strike: boolean | null;
  readonly fontSizePt: number | null;
  readonly fontFamily: string | null;
  readonly foreground: string | null;
  readonly background: string | null;
  readonly styleName: string | null;
}

export interface DocumentDiffRichSnippet {
  readonly runs: readonly DocumentDiffRichRun[];
}

export interface DocumentDiffLocation {
  readonly part: "body" | "header" | "footer" | "footnote" | "endnote" | "textBox" | "worksheet";
  readonly sectionIndex: number | null;
  readonly paragraphIndex: number | null;
  readonly nearestHeading: string | null;
  readonly tableIndex: number | null;
  readonly rowIndex: number | null;
  readonly columnIndex: number | null;
  readonly sheetName: string | null;
  readonly cellAddress: string | null;
}

export interface DocumentDiffChange {
  readonly changeId: string;
  readonly kind: DocumentDiffChangeKind;
  readonly location: DocumentDiffLocation;
  readonly before: DocumentDiffRichSnippet | null;
  readonly after: DocumentDiffRichSnippet | null;
  readonly confidence: "exact" | "normalized" | "heuristic";
}

export interface DocumentDiffSession {
  readonly contractVersion: "2.0";
  readonly sessionId: string;
  readonly entryHandle: string;
  readonly historicalRevisionId: string;
  readonly effectiveRevisionId: string;
  readonly format: DocumentDiffFormat;
  readonly provider: DocumentDiffProvider;
  readonly fidelity: DocumentDiffFidelity;
  readonly summary: DocumentDiffSummary;
  readonly coverage: DocumentDiffCoverage;
  readonly warnings: readonly DocumentDiffWarning[];
  readonly canOpenComparisonArtifact: boolean;
  readonly canExportComparisonArtifact: boolean;
}

export type DocumentDiffSessionResult =
  | { readonly outcome: "ready"; readonly session: DocumentDiffSession; readonly failure: null }
  | {
      readonly outcome: "failure";
      readonly session: null;
      readonly failure: DocumentDiffSessionFailure;
    };

export interface DocumentDiffChangePageRequest {
  readonly sessionId: string;
  readonly cursor: string | null;
  readonly limit: number;
}

export interface DocumentDiffChangePage {
  readonly sessionId: string;
  readonly changes: readonly DocumentDiffChange[];
  readonly nextCursor: string | null;
}

export type DocumentDiffPageFailure = "sessionExpired" | "invalidCursor" | "cancelled" | "stale";

export type DocumentDiffChangePageResult =
  | { readonly outcome: "ready"; readonly page: DocumentDiffChangePage; readonly failure: null }
  | { readonly outcome: "failure"; readonly page: null; readonly failure: DocumentDiffPageFailure };

function record(value: unknown, label: string): JsonRecord {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as JsonRecord;
}

function exact(value: JsonRecord, keys: readonly string[], label: string): void {
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    throw new Error(`${label} has unknown or missing fields`);
  }
}

function nonEmptyString(value: unknown, label: string): string {
  if (typeof value !== "string" || !value) throw new Error(`${label} must be a non-empty string`);
  return value;
}

function nonBlankString(value: unknown, label: string): string {
  const result = nonEmptyString(value, label);
  if (!result.trim()) throw new Error(`${label} must not be blank`);
  return result;
}

function uuid(value: unknown, label: string): string {
  const result = nonEmptyString(value, label);
  if (!UUID_PATTERN.test(result)) throw new Error(`${label} must be a canonical UUID`);
  return result;
}

function safeInteger(value: unknown, label: string): number {
  if (!Number.isSafeInteger(value) || (value as number) < 0 || (value as number) > 2_147_483_647) {
    throw new Error(`${label} must be a non-negative 32-bit integer`);
  }
  return value as number;
}

function bool(value: unknown, label: string): boolean {
  if (typeof value !== "boolean") throw new Error(`${label} must be boolean`);
  return value;
}

function nullableString(value: unknown, label: string): string | null {
  return value === null ? null : nonBlankString(value, label);
}

function nullableIndex(value: unknown, label: string): number | null {
  return value === null ? null : safeInteger(value, label);
}

function nullableBool(value: unknown, label: string): boolean | null {
  return value === null ? null : bool(value, label);
}

function nullablePositiveNumber(value: unknown, label: string): number | null {
  if (value === null) return null;
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    throw new Error(`${label} must be a positive finite number or null`);
  }
  return value;
}

function oneOf<T extends string>(value: unknown, values: readonly T[], label: string): T {
  if (typeof value !== "string" || !values.includes(value as T)) {
    throw new Error(`${label} is invalid`);
  }
  return value as T;
}

function parseSummary(value: unknown): DocumentDiffSummary {
  const source = record(value, "document diff summary");
  const keys = [
    "totalChangeGroups", "rawRevisionCount", "insertions", "deletions", "replacements",
    "moves", "formattingChanges", "tableChanges", "commentChanges", "otherChanges",
  ] as const;
  exact(source, keys, "document diff summary");
  const counts = Object.fromEntries(
    keys.map((key) => [key, safeInteger(source[key], key)]),
  ) as unknown as DocumentDiffSummary;
  const categorized = counts.insertions + counts.deletions + counts.replacements + counts.moves
    + counts.formattingChanges + counts.tableChanges + counts.commentChanges + counts.otherChanges;
  if (categorized !== counts.totalChangeGroups) {
    throw new Error("document diff summary categories must equal totalChangeGroups");
  }
  return counts;
}

function parseCoverage(value: unknown): DocumentDiffCoverage {
  const source = record(value, "document diff coverage");
  exact(source, ["areas", "truncated"], "document diff coverage");
  if (!Array.isArray(source.areas)) throw new Error("document diff coverage areas must be an array");
  if (source.areas.length === 0) throw new Error("document diff coverage areas cannot be empty");
  const seen = new Set<string>();
  const areas = source.areas.map((value, index) => {
    const entry = record(value, `document diff coverage area ${index}`);
    exact(entry, ["area", "status"], `document diff coverage area ${index}`);
    const area = oneOf(entry.area, [
      "visibleText", "structure", "formatting", "tables", "headersFooters", "notes",
      "textBoxes", "comments", "images", "fields", "pagination", "worksheetValues",
      "worksheetFormulas", "worksheetStyles", "worksheetMerges", "worksheetVisibility",
    ] as const, `document diff coverage area ${index}`);
    if (seen.has(area)) throw new Error("document diff coverage areas must be unique");
    seen.add(area);
    return {
      area,
      status: oneOf(entry.status, ["covered", "partial", "notCovered"] as const,
        `document diff coverage status ${index}`),
    };
  });
  return { areas, truncated: bool(source.truncated, "document diff coverage truncated") };
}

function parseSession(value: unknown): DocumentDiffSession {
  const source = record(value, "document diff session");
  exact(source, [
    "contractVersion", "sessionId", "entryHandle", "historicalRevisionId",
    "effectiveRevisionId", "format", "provider", "fidelity", "summary", "coverage",
    "warnings", "canOpenComparisonArtifact", "canExportComparisonArtifact",
  ], "document diff session");
  if (source.contractVersion !== "2.0") throw new Error("document diff contractVersion must be 2.0");
  if (!Array.isArray(source.warnings)) throw new Error("document diff warnings must be an array");
  const format = oneOf(source.format, ["docx", "xlsx", "text", "binary"] as const,
    "document diff format");
  const provider = oneOf(source.provider, ["builtIn", "wordNative", "xlsxBuiltIn"] as const,
    "document diff provider");
  const fidelity = oneOf(source.fidelity, ["structural", "officeNative"] as const,
    "document diff fidelity");
  const coverage = parseCoverage(source.coverage);
  const warningSet = new Set<DocumentDiffWarning>();
  const warnings = source.warnings.map((warning, index) => {
    const parsed = oneOf(warning, [
      "resultTruncated", "partialCoverage", "existingRevisionsNormalized",
      "cachedValuesNotRecalculated",
    ] as const, `document diff warning ${index}`);
    if (warningSet.has(parsed)) throw new Error("document diff warnings must be unique");
    warningSet.add(parsed);
    return parsed;
  });
  if (coverage.truncated !== warningSet.has("resultTruncated")) {
    throw new Error("resultTruncated warning must match coverage.truncated");
  }
  const hasPartialCoverage = coverage.areas.some((area) => area.status !== "covered");
  if (hasPartialCoverage !== warningSet.has("partialCoverage")) {
    throw new Error("partialCoverage warning must match coverage areas");
  }
  if (
    (provider === "builtIn" && (format === "xlsx" || fidelity !== "structural"))
    || (provider === "wordNative" && (format !== "docx" || fidelity !== "officeNative"))
    || (provider === "xlsxBuiltIn" && (format !== "xlsx" || fidelity !== "structural"))
  ) {
    throw new Error("document diff provider is incompatible with format or fidelity");
  }
  return {
    contractVersion: "2.0",
    sessionId: uuid(source.sessionId, "document diff sessionId"),
    entryHandle: nonBlankString(source.entryHandle, "document diff entryHandle"),
    historicalRevisionId: uuid(source.historicalRevisionId, "historicalRevisionId"),
    effectiveRevisionId: uuid(source.effectiveRevisionId, "effectiveRevisionId"),
    format,
    provider,
    fidelity,
    summary: parseSummary(source.summary),
    coverage,
    warnings,
    canOpenComparisonArtifact: bool(source.canOpenComparisonArtifact,
      "canOpenComparisonArtifact"),
    canExportComparisonArtifact: bool(source.canExportComparisonArtifact,
      "canExportComparisonArtifact"),
  };
}

export function parseDocumentDiffSessionResult(value: unknown): DocumentDiffSessionResult {
  const source = record(value, "document diff session result");
  exact(source, ["outcome", "session", "failure"], "document diff session result");
  if (source.outcome === "ready" && source.failure === null) {
    return { outcome: "ready", session: parseSession(source.session), failure: null };
  }
  if (source.outcome === "failure" && source.session === null) {
    return {
      outcome: "failure",
      session: null,
      failure: oneOf(source.failure, [
        "unsupported", "invalidContent", "io", "cancelled", "stale",
        "providerUnavailable", "timeout",
      ] as const, "document diff failure"),
    };
  }
  throw new Error("document diff session result is invalid");
}

function parseRichRun(value: unknown, index: number): DocumentDiffRichRun {
  const label = `document diff rich run ${index}`;
  const source = record(value, label);
  exact(source, [
    "text", "role", "bold", "italic", "underline", "strike", "fontSizePt", "fontFamily",
    "foreground", "background", "styleName",
  ], label);
  return {
    text: nonEmptyString(source.text, `${label} text`),
    role: oneOf(source.role, ["context", "inserted", "deleted", "changed"] as const,
      `${label} role`),
    bold: nullableBool(source.bold, `${label} bold`),
    italic: nullableBool(source.italic, `${label} italic`),
    underline: nullableBool(source.underline, `${label} underline`),
    strike: nullableBool(source.strike, `${label} strike`),
    fontSizePt: nullablePositiveNumber(source.fontSizePt, `${label} fontSizePt`),
    fontFamily: nullableString(source.fontFamily, `${label} fontFamily`),
    foreground: nullableString(source.foreground, `${label} foreground`),
    background: nullableString(source.background, `${label} background`),
    styleName: nullableString(source.styleName, `${label} styleName`),
  };
}

function parseSnippet(value: unknown, label: string): DocumentDiffRichSnippet {
  const source = record(value, label);
  exact(source, ["runs"], label);
  if (!Array.isArray(source.runs) || source.runs.length === 0) {
    throw new Error(`${label} runs must be a non-empty array`);
  }
  return { runs: source.runs.map(parseRichRun) };
}

function parseLocation(value: unknown): DocumentDiffLocation {
  const source = record(value, "document diff location");
  exact(source, [
    "part", "sectionIndex", "paragraphIndex", "nearestHeading", "tableIndex", "rowIndex",
    "columnIndex", "sheetName", "cellAddress",
  ], "document diff location");
  const part = oneOf(source.part, [
    "body", "header", "footer", "footnote", "endnote", "textBox", "worksheet",
  ] as const, "document diff location part");
  const sectionIndex = nullableIndex(source.sectionIndex, "sectionIndex");
  const paragraphIndex = nullableIndex(source.paragraphIndex, "paragraphIndex");
  const nearestHeading = nullableString(source.nearestHeading, "nearestHeading");
  const tableIndex = nullableIndex(source.tableIndex, "tableIndex");
  const rowIndex = nullableIndex(source.rowIndex, "rowIndex");
  const columnIndex = nullableIndex(source.columnIndex, "columnIndex");
  const sheetName = nullableString(source.sheetName, "sheetName");
  const cellAddress = nullableString(source.cellAddress, "cellAddress");
  if (part === "worksheet") {
    if (!sheetName || sectionIndex !== null || paragraphIndex !== null
      || nearestHeading !== null || tableIndex !== null) {
      throw new Error("worksheet locations require only worksheet coordinates");
    }
  } else {
    if (sheetName !== null || cellAddress !== null) {
      throw new Error("document locations cannot contain worksheet coordinates");
    }
    if ((part === "header" || part === "footer") && sectionIndex === null) {
      throw new Error("header and footer locations require sectionIndex");
    }
    if ((rowIndex !== null || columnIndex !== null) && tableIndex === null) {
      throw new Error("document row and column coordinates require tableIndex");
    }
  }
  return {
    part, sectionIndex, paragraphIndex, nearestHeading, tableIndex, rowIndex, columnIndex,
    sheetName, cellAddress,
  };
}

function parseChange(value: unknown): DocumentDiffChange {
  const source = record(value, "document diff change");
  exact(source, [
    "changeId", "kind", "location", "before", "after", "confidence",
  ], "document diff change");
  const kind = oneOf(source.kind, [
    "insert", "delete", "replace", "move", "format", "table", "comment", "other",
  ] as const, "document diff change kind");
  const before = source.before === null ? null : parseSnippet(source.before, "before snippet");
  const after = source.after === null ? null : parseSnippet(source.after, "after snippet");
  const validSnippets = kind === "insert" ? before === null && after !== null
    : kind === "delete" ? before !== null && after === null
    : kind === "replace" || kind === "move" || kind === "format"
      ? before !== null && after !== null
      : kind === "other" ? true
      : before !== null || after !== null;
  if (!validSnippets) throw new Error("document diff change kind and snippets are incompatible");
  return {
    changeId: uuid(source.changeId, "document diff changeId"),
    kind,
    location: parseLocation(source.location),
    before,
    after,
    confidence: oneOf(source.confidence, ["exact", "normalized", "heuristic"] as const,
      "document diff confidence"),
  };
}

export function createDocumentDiffChangePageRequest(value: unknown): DocumentDiffChangePageRequest {
  const source = record(value, "document diff page request");
  exact(source, ["sessionId", "cursor", "limit"], "document diff page request");
  const limit = safeInteger(source.limit, "document diff page limit");
  if (limit < 1 || limit > 200) throw new Error("document diff page limit must be between 1 and 200");
  return {
    sessionId: uuid(source.sessionId, "document diff page sessionId"),
    cursor: nullableString(source.cursor, "document diff page cursor"),
    limit,
  };
}

export function parseDocumentDiffChangePageResult(
  value: unknown,
  requestValue: DocumentDiffChangePageRequest,
): DocumentDiffChangePageResult {
  const request = createDocumentDiffChangePageRequest(requestValue);
  const source = record(value, "document diff page result");
  exact(source, ["outcome", "page", "failure"], "document diff page result");
  if (source.outcome === "failure" && source.page === null) {
    return {
      outcome: "failure",
      page: null,
      failure: oneOf(source.failure, [
        "sessionExpired", "invalidCursor", "cancelled", "stale",
      ] as const, "document diff page failure"),
    };
  }
  if (source.outcome !== "ready" || source.failure !== null) {
    throw new Error("document diff page result is invalid");
  }
  const page = record(source.page, "document diff page");
  exact(page, ["sessionId", "changes", "nextCursor"], "document diff page");
  const producedSessionId = uuid(page.sessionId, "document diff page sessionId");
  if (producedSessionId !== request.sessionId) {
    throw new Error("document diff page session does not match its request");
  }
  if (!Array.isArray(page.changes)) throw new Error("document diff page changes must be an array");
  if (page.changes.length > request.limit) throw new Error("document diff page exceeds request limit");
  const changes = page.changes.map(parseChange);
  if (new Set(changes.map((change) => change.changeId)).size !== changes.length) {
    throw new Error("document diff page changeIds must be unique");
  }
  const nextCursor = nullableString(page.nextCursor, "document diff page nextCursor");
  if (nextCursor !== null && nextCursor === request.cursor) {
    throw new Error("document diff page nextCursor must advance");
  }
  if (nextCursor !== null && changes.length === 0) {
    throw new Error("a non-terminal document diff page cannot be empty");
  }
  return {
    outcome: "ready",
    page: { sessionId: producedSessionId, changes, nextCursor },
    failure: null,
  };
}
