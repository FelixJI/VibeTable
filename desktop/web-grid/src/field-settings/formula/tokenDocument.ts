import type {
  FormulaAuthorDocument,
  FormulaAuthorToken,
  FormulaTextPosition,
  FormulaTextRange,
} from "@/contracts/generated/workbench";

/**
 * UTF-16 source mapping for the Go author document.
 *
 * The sidecar owns every name decision; this helper only shifts stable token
 * ranges over plain text edits. It never parses identifiers or field names.
 * Positions mirror Go `authorCoordinates`: zero-based line and UTF-16
 * character, with CRLF/CR/LF all terminating a line.
 */

export interface TokenSpan {
  readonly token: FormulaAuthorToken;
  readonly start: number;
  readonly end: number;
}

export interface ReferenceInsertion {
  readonly source: string;
  readonly document: FormulaAuthorDocument;
  /** UTF-16 caret offset inside `source` after the insertion. */
  readonly caret: number;
}

export interface ReferenceSpec {
  /** Full inserted text, including any aggregate/function wrapping. */
  readonly text: string;
  /** Stable token bound to part of `text` (the reference label itself). */
  readonly token: FormulaAuthorToken;
  /** Offset of the label inside `text`. */
  readonly labelStart: number;
  /** Length of the label inside `text`. */
  readonly labelLength: number;
  /** Caret offset inside `text` (defaults to the end). */
  readonly caret?: number;
}

export function emptyFormulaAuthorDocument(): FormulaAuthorDocument {
  return { displaySource: "", tokens: [], documentRevision: 1 };
}

export function isFormulaAuthorDocument(value: unknown): value is FormulaAuthorDocument {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<FormulaAuthorDocument>;
  if (typeof candidate.displaySource !== "string") return false;
  if (typeof candidate.documentRevision !== "number"
    || !Number.isInteger(candidate.documentRevision)
    || candidate.documentRevision <= 0) return false;
  if (!Array.isArray(candidate.tokens)) return false;
  // Every token must carry a closed identity: known fields only, a known
  // kind, a well-formed in-range span, and a kind/fieldId/tableId combination
  // that never mixes the current-row, relation, and cross-table worlds.
  const source = candidate.displaySource;
  return candidate.tokens.every(token => isFormulaAuthorToken(token, source));
}

const TOKEN_PROPERTY_NAMES = new Set([
  "range",
  "kind",
  "fieldId",
  "relationFieldId",
  "targetFieldId",
  "tableId",
]);

function isFormulaAuthorToken(value: unknown, source: string): value is FormulaAuthorToken {
  if (!value || typeof value !== "object") return false;
  const token = value as Record<string, unknown>;
  if (Object.keys(token).some(key => !TOKEN_PROPERTY_NAMES.has(key))) return false;
  if (!isFormulaTextRange(token.range)) return false;
  const start = positionToOffset(source, token.range.start);
  const end = positionToOffset(source, token.range.end);
  if (end <= start || end > source.length) return false;
  for (const [offset, position] of [[start, token.range.start], [end, token.range.end]] as const) {
    const actual = offsetToPosition(source, offset);
    if (actual.line !== position.line || actual.character !== position.character
      || isLowSurrogate(source[offset] ?? "")) return false;
  }
  if (token.relationFieldId !== null && !isNonEmptyString(token.relationFieldId)) return false;
  if (token.targetFieldId !== null && !isNonEmptyString(token.targetFieldId)) return false;
  const hasNoTableId = token.tableId === undefined || token.tableId === null;
  switch (token.kind) {
    case "field":
      return isNonEmptyString(token.fieldId)
        && token.relationFieldId === null
        && token.targetFieldId === null
        && hasNoTableId;
    case "relation":
      return isNonEmptyString(token.fieldId)
        && token.relationFieldId === token.fieldId
        && token.targetFieldId === null
        && hasNoTableId;
    case "relationTarget":
      return isNonEmptyString(token.fieldId)
        && isNonEmptyString(token.relationFieldId)
        && token.targetFieldId === token.fieldId
        && hasNoTableId;
    case "table":
      return token.fieldId === null
        && isNonEmptyString(token.tableId)
        && token.relationFieldId === null
        && token.targetFieldId === null;
    case "sourceField":
      return isNonEmptyString(token.fieldId)
        && isNonEmptyString(token.tableId)
        && token.relationFieldId === null
        && token.targetFieldId === null;
    default:
      return false;
  }
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

export function isFormulaTextRange(value: unknown): value is FormulaTextRange {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<FormulaTextRange>;
  return isFormulaTextPosition(candidate.start) && isFormulaTextPosition(candidate.end);
}

function isFormulaTextPosition(value: unknown): value is FormulaTextPosition {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<FormulaTextPosition>;
  return typeof candidate.line === "number" && Number.isInteger(candidate.line)
    && candidate.line >= 0
    && typeof candidate.character === "number" && Number.isInteger(candidate.character)
    && candidate.character >= 0;
}

export function offsetToPosition(source: string, offset: number): FormulaTextPosition {
  const clamped = Math.max(0, Math.min(offset, source.length));
  let line = 0;
  let character = 0;
  for (let index = 0; index < clamped;) {
    if (source[index] === "\r") {
      const skip = source[index + 1] === "\n" ? 2 : 1;
      if (index + skip > clamped) break;
      line += 1;
      character = 0;
      index += skip;
      continue;
    }
    if (source[index] === "\n") {
      if (index + 1 > clamped) break;
      line += 1;
      character = 0;
      index += 1;
      continue;
    }
    character += 1;
    index += 1;
  }
  return { line, character };
}

export function positionToOffset(source: string, position: FormulaTextPosition): number {
  let line = 0;
  let character = 0;
  let offset = 0;
  while (line < position.line && offset < source.length) {
    if (source[offset] === "\r") {
      offset += source[offset + 1] === "\n" ? 2 : 1;
      line += 1;
      character = 0;
      continue;
    }
    if (source[offset] === "\n") {
      offset += 1;
      line += 1;
      character = 0;
      continue;
    }
    character += 1;
    offset += 1;
  }
  return offset + (line === position.line ? Math.max(0, position.character) : 0);
}

/** Token spans in UTF-16 offsets against the given source text. */
export function tokenSpans(
  document: FormulaAuthorDocument,
  source = document.displaySource,
): readonly TokenSpan[] {
  return document.tokens.map(token => ({
    token,
    start: positionToOffset(source, token.range.start),
    end: positionToOffset(source, token.range.end),
  }));
}

interface SourceDiff {
  /** First replaced UTF-16 offset in the previous source. */
  readonly start: number;
  /** UTF-16 length removed from the previous source. */
  readonly removedLength: number;
  /** Text inserted at `start` in the next source. */
  readonly inserted: string;
}

/** Single minimal replacement between two strings, never splitting a surrogate. */
function diffSources(previous: string, next: string): SourceDiff {
  const maxPrefix = Math.min(previous.length, next.length);
  let prefix = 0;
  while (prefix < maxPrefix && previous[prefix] === next[prefix]) prefix += 1;
  if (prefix > 0 && isLowSurrogate(previous[prefix] ?? "")) prefix -= 1;
  const maxSuffix = Math.min(previous.length - prefix, next.length - prefix);
  let suffix = 0;
  while (
    suffix < maxSuffix
    && previous[previous.length - 1 - suffix] === next[next.length - 1 - suffix]
  ) suffix += 1;
  if (suffix > 0 && isLowSurrogate(previous[previous.length - suffix] ?? "")) suffix -= 1;
  return {
    start: prefix,
    removedLength: previous.length - prefix - suffix,
    inserted: next.slice(prefix, next.length - suffix),
  };
}

function isLowSurrogate(character: string): boolean {
  const code = character.charCodeAt(0);
  return Number.isFinite(code) && code >= 0xdc00 && code <= 0xdfff;
}

/**
 * Applies one user text edit to the working document: tokens intersecting the
 * replaced range are dropped, tokens after it shift by the length delta, and
 * the revision advances. Untouched text (including string literals) is copied
 * verbatim because no parsing happens here.
 */
export function applySourceEdit(
  document: FormulaAuthorDocument,
  previousSource: string,
  nextSource: string,
): FormulaAuthorDocument {
  if (previousSource === nextSource) return document;
  const revision = document.documentRevision + 1;
  if (document.displaySource !== previousSource || !document.tokens.length) {
    return { displaySource: nextSource, tokens: [], documentRevision: revision };
  }
  const diff = diffSources(previousSource, nextSource);
  const removedEnd = diff.start + diff.removedLength;
  const delta = diff.inserted.length - diff.removedLength;
  const kept: FormulaAuthorToken[] = [];
  for (const span of tokenSpans(document, previousSource)) {
    if (span.end <= diff.start || span.start >= removedEnd) {
      const shift = span.start >= removedEnd ? delta : 0;
      kept.push(withShiftedSpan(span, shift, nextSource));
    }
  }
  return {
    displaySource: nextSource,
    documentRevision: revision,
    tokens: kept,
  };
}

function withShiftedSpan(span: TokenSpan, shift: number, source: string): FormulaAuthorToken {
  const start = span.start + shift;
  const end = span.end + shift;
  return {
    ...span.token,
    range: {
      start: offsetToPosition(source, start),
      end: offsetToPosition(source, end),
    },
  };
}

/** Replaces the selection with plain text; existing tokens shift accordingly. */
export function insertPlainText(
  document: FormulaAuthorDocument,
  source: string,
  selectionStart: number,
  selectionEnd: number,
  text: string,
): ReferenceInsertion {
  const start = clampSelection(source, selectionStart);
  const end = Math.max(start, clampSelection(source, selectionEnd));
  const nextSource = source.slice(0, start) + text + source.slice(end);
  const nextDocument = applySourceEdit(document, source, nextSource);
  return {
    source: nextSource,
    document: nextDocument,
    caret: start + text.length,
  };
}

/** Replaces the selection with a stable-ID reference (optionally wrapped). */
export function insertReference(
  document: FormulaAuthorDocument,
  source: string,
  selectionStart: number,
  selectionEnd: number,
  reference: ReferenceSpec,
): ReferenceInsertion {
  const start = clampSelection(source, selectionStart);
  const end = Math.max(start, clampSelection(source, selectionEnd));
  const nextSource = source.slice(0, start) + reference.text + source.slice(end);
  const tokenStart = start + reference.labelStart;
  const tokenEnd = tokenStart + reference.labelLength;
  const removedEnd = end;
  const delta = reference.text.length - (end - start);
  const tokens: FormulaAuthorToken[] = [];
  for (const span of tokenSpans(document, source)) {
    if (span.end <= start || span.start >= removedEnd) {
      const shift = span.start >= removedEnd ? delta : 0;
      tokens.push(withShiftedSpan(span, shift, nextSource));
    }
  }
  tokens.push({
    ...reference.token,
    range: {
      start: offsetToPosition(nextSource, tokenStart),
      end: offsetToPosition(nextSource, tokenEnd),
    },
  });
  tokens.sort((a, b) => positionToOffset(nextSource, a.range.start)
    - positionToOffset(nextSource, b.range.start));
  return {
    source: nextSource,
    document: {
      displaySource: nextSource,
      documentRevision: document.documentRevision + 1,
      tokens,
    },
    caret: start + (reference.caret ?? reference.text.length),
  };
}

function clampSelection(source: string, offset: number): number {
  return Math.max(0, Math.min(Math.trunc(offset), source.length));
}

/** Converts a UTF-16 text range into a textarea selection. */
export function textRangeToSelection(
  source: string,
  range: FormulaTextRange,
): { start: number; end: number } | null {
  if (!isFormulaTextRange(range)) return null;
  const start = positionToOffset(source, range.start);
  const end = positionToOffset(source, range.end);
  if (start > source.length || end > source.length || end < start) return null;
  return { start, end };
}

/** Human-facing one-based rendering of an error range. */
export function formatTextRange(range: FormulaTextRange): string {
  return `第${range.start.line + 1}行 第${range.start.character + 1}列`;
}
