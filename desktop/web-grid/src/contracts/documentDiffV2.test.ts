import { describe, expect, it } from "vitest";
import {
  createDocumentDiffChangePageRequest,
  parseDocumentDiffChangePageResult,
  parseDocumentDiffSessionResult,
} from "./documentDiffV2";

const historicalRevisionId = "11111111-1111-4111-8111-111111111111";
const effectiveRevisionId = "22222222-2222-4222-8222-222222222222";
const sessionId = "33333333-3333-4333-8333-333333333333";

function readySession(): unknown {
  return {
    outcome: "ready",
    session: {
      contractVersion: "2.0",
      sessionId,
      entryHandle: "entry-handle",
      historicalRevisionId,
      effectiveRevisionId,
      format: "docx",
      provider: "builtIn",
      fidelity: "structural",
      summary: {
        totalChangeGroups: 1,
        rawRevisionCount: 2,
        insertions: 0,
        deletions: 0,
        replacements: 1,
        moves: 0,
        formattingChanges: 0,
        tableChanges: 0,
        commentChanges: 0,
        otherChanges: 0,
      },
      coverage: {
        truncated: false,
        areas: [
          { area: "visibleText", status: "covered" },
          { area: "formatting", status: "partial" },
        ],
      },
      warnings: ["partialCoverage"],
      canOpenComparisonArtifact: true,
      canExportComparisonArtifact: false,
    },
    failure: null,
  };
}

describe("document diff v2 contracts", () => {
  it("parses a ready session through the public contract seam", () => {
    const value = readySession();
    expect(parseDocumentDiffSessionResult(value)).toEqual(value);
  });

  it("represents failure without a partially usable session", () => {
    const value = { outcome: "failure", session: null, failure: "stale" };
    expect(parseDocumentDiffSessionResult(value)).toEqual(value);
  });

  it("rejects unknown fields, invalid identities, inconsistent counts, and duplicate coverage", () => {
    const extra = readySession() as Record<string, unknown>;
    expect(() => parseDocumentDiffSessionResult({ ...extra, providerType: "private" })).toThrow();

    const badIdentity = structuredClone(readySession()) as {
      session: Record<string, unknown>;
    };
    badIdentity.session.sessionId = "not-a-uuid";
    expect(() => parseDocumentDiffSessionResult(badIdentity)).toThrow(/UUID/);

    const badSummary = structuredClone(readySession()) as {
      session: { summary: Record<string, unknown> };
    };
    badSummary.session.summary.totalChangeGroups = 2;
    expect(() => parseDocumentDiffSessionResult(badSummary)).toThrow(/categories/);

    const duplicateCoverage = structuredClone(readySession()) as {
      session: { coverage: { areas: unknown[] } };
    };
    duplicateCoverage.session.coverage.areas.push({ area: "visibleText", status: "covered" });
    expect(() => parseDocumentDiffSessionResult(duplicateCoverage)).toThrow(/unique/);

    const blankHandle = structuredClone(readySession()) as {
      session: Record<string, unknown>;
    };
    blankHandle.session.entryHandle = " ";
    expect(() => parseDocumentDiffSessionResult(blankHandle)).toThrow(/blank/);

    const outOfRangeCount = structuredClone(readySession()) as {
      session: { summary: Record<string, unknown> };
    };
    outOfRangeCount.session.summary.rawRevisionCount = 2_147_483_648;
    expect(() => parseDocumentDiffSessionResult(outOfRangeCount)).toThrow(/32-bit/);
  });

  it("rejects provider, format, and fidelity combinations that misstate coverage", () => {
    const word = structuredClone(readySession()) as {
      session: Record<string, unknown>;
    };
    word.session.provider = "wordNative";
    expect(() => parseDocumentDiffSessionResult(word)).toThrow(/provider/);

    const xlsx = structuredClone(readySession()) as {
      session: Record<string, unknown>;
    };
    xlsx.session.provider = "xlsxBuiltIn";
    expect(() => parseDocumentDiffSessionResult(xlsx)).toThrow(/provider/);
  });

  it("rejects mixed ready and failure states", () => {
    expect(() => parseDocumentDiffSessionResult({
      outcome: "ready",
      session: (readySession() as { session: unknown }).session,
      failure: "io",
    })).toThrow();
    expect(() => parseDocumentDiffSessionResult({
      outcome: "failure",
      session: (readySession() as { session: unknown }).session,
      failure: "io",
    })).toThrow();
  });

  it("keeps coverage truncation and its closed warning synchronized", () => {
    const missingWarning = structuredClone(readySession()) as {
      session: { coverage: { truncated: boolean } };
    };
    missingWarning.session.coverage.truncated = true;
    expect(() => parseDocumentDiffSessionResult(missingWarning)).toThrow(/resultTruncated/);

    const staleWarning = structuredClone(readySession()) as {
      session: { warnings: string[] };
    };
    staleWarning.session.warnings.push("resultTruncated");
    expect(() => parseDocumentDiffSessionResult(staleWarning)).toThrow(/resultTruncated/);

    const unknownWarning = structuredClone(readySession()) as {
      session: { warnings: string[] };
    };
    unknownWarning.session.warnings.push("provider-private-warning");
    expect(() => parseDocumentDiffSessionResult(unknownWarning)).toThrow(/warning/);

    const missingPartialWarning = structuredClone(readySession()) as {
      session: { warnings: string[] };
    };
    missingPartialWarning.session.warnings = [];
    expect(() => parseDocumentDiffSessionResult(missingPartialWarning)).toThrow(/partialCoverage/);
  });

  it("creates a bounded page request and parses an advancing page for the same session", () => {
    const requestValue = { sessionId, cursor: "cursor-1", limit: 2 };
    const request = createDocumentDiffChangePageRequest(requestValue);
    expect(request).toEqual(requestValue);

    const firstChange = change(
      "44444444-4444-4444-8444-444444444444",
      "replace",
    );
    const secondChange = change(
      "55555555-5555-4555-8555-555555555555",
      "format",
    );
    const value = {
      outcome: "ready",
      page: {
        sessionId,
        changes: [firstChange, secondChange],
        nextCursor: "cursor-2",
      },
      failure: null,
    };
    expect(parseDocumentDiffChangePageResult(value, request)).toEqual(value);
  });

  it("rejects invalid page requests and cross-session or non-advancing pages", () => {
    expect(() => createDocumentDiffChangePageRequest({ sessionId, cursor: null, limit: 0 }))
      .toThrow(/between 1 and 200/);
    expect(() => createDocumentDiffChangePageRequest({ sessionId, cursor: null, limit: 201 }))
      .toThrow(/between 1 and 200/);
    expect(() => createDocumentDiffChangePageRequest({
      sessionId, cursor: null, limit: 1, unexpected: true,
    })).toThrow(/unknown or missing/);

    const request = createDocumentDiffChangePageRequest({ sessionId, cursor: "cursor-1", limit: 1 });
    expect(() => parseDocumentDiffChangePageResult({
      outcome: "ready",
      page: {
        sessionId: historicalRevisionId,
        changes: [change("44444444-4444-4444-8444-444444444444", "replace")],
        nextCursor: "cursor-2",
      },
      failure: null,
    }, request)).toThrow(/session/);
    expect(() => parseDocumentDiffChangePageResult({
      outcome: "ready",
      page: { sessionId, changes: [], nextCursor: "cursor-1" },
      failure: null,
    }, request)).toThrow(/advance/);
    expect(() => parseDocumentDiffChangePageResult({
      outcome: "ready",
      page: { sessionId, changes: [], nextCursor: "cursor-2" },
      failure: null,
    }, request)).toThrow(/non-terminal/);
  });

  it("rejects duplicate changes, excess pages, and unknown nested content", () => {
    const request = createDocumentDiffChangePageRequest({ sessionId, cursor: null, limit: 1 });
    const item = change("44444444-4444-4444-8444-444444444444", "replace");
    expect(() => parseDocumentDiffChangePageResult({
      outcome: "ready",
      page: { sessionId, changes: [item, item], nextCursor: null },
      failure: null,
    }, request)).toThrow(/limit/);

    const twoItemRequest = createDocumentDiffChangePageRequest({ sessionId, cursor: null, limit: 2 });
    expect(() => parseDocumentDiffChangePageResult({
      outcome: "ready",
      page: { sessionId, changes: [item, item], nextCursor: null },
      failure: null,
    }, twoItemRequest)).toThrow(/unique/);

    const unknownRun = structuredClone(item) as {
      before: { runs: Array<Record<string, unknown>> };
    };
    unknownRun.before.runs[0]!.html = "<script>bad()</script>";
    expect(() => parseDocumentDiffChangePageResult({
      outcome: "ready",
      page: { sessionId, changes: [unknownRun], nextCursor: null },
      failure: null,
    }, request)).toThrow(/unknown or missing/);
  });

  it("parses only the closed page failure set", () => {
    const request = createDocumentDiffChangePageRequest({ sessionId, cursor: null, limit: 20 });
    const value = { outcome: "failure", page: null, failure: "sessionExpired" };
    expect(parseDocumentDiffChangePageResult(value, request)).toEqual(value);
    expect(() => parseDocumentDiffChangePageResult({
      outcome: "failure", page: null, failure: "io",
    }, request)).toThrow(/failure/);
  });

  it("allows an opaque binary change without inventing textual snippets", () => {
    const request = createDocumentDiffChangePageRequest({ sessionId, cursor: null, limit: 1 });
    const value = {
      outcome: "ready",
      page: {
        sessionId,
        changes: [{
          ...change("44444444-4444-4444-8444-444444444444", "replace"),
          kind: "other",
          before: null,
          after: null,
        }],
        nextCursor: null,
      },
      failure: null,
    };
    expect(parseDocumentDiffChangePageResult(value, request)).toEqual(value);
  });
});

function richSnippet(text: string) {
  return {
    runs: [{
      text,
      role: "changed",
      bold: null,
      italic: null,
      underline: null,
      strike: null,
      fontSizePt: null,
      fontFamily: null,
      foreground: null,
      background: null,
      styleName: null,
    }],
  };
}

function change(changeId: string, kind: "replace" | "format") {
  return {
    changeId,
    kind,
    location: {
      part: "body",
      sectionIndex: null,
      paragraphIndex: 3,
      nearestHeading: "合同金额",
      tableIndex: null,
      rowIndex: null,
      columnIndex: null,
      sheetName: null,
      cellAddress: null,
    },
    before: richSnippet("100"),
    after: richSnippet("120"),
    confidence: "exact",
  };
}
