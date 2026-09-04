using System.Text.Json;
using VibeTable.Workspace.Diff;

namespace VibeTable.Workspace.Tests;

[TestClass]
public sealed class DocumentDiffV2ContractTests
{
    private static readonly Guid SessionId = Guid.Parse("11111111-1111-4111-8111-111111111111");
    private static readonly Guid HistoricalRevisionId =
        Guid.Parse("22222222-2222-4222-8222-222222222222");
    private static readonly Guid EffectiveRevisionId =
        Guid.Parse("33333333-3333-4333-8333-333333333333");
    private static readonly Guid ChangeId = Guid.Parse("44444444-4444-4444-8444-444444444444");

    [TestMethod]
    public void ReadySession_SerializesTheExactVersionedWireContract()
    {
        var coverageAreas = new List<DocumentDiffCoverageEntry>
        {
            new(DocumentDiffCoverageArea.VisibleText, DocumentDiffCoverageStatus.Covered),
            new(DocumentDiffCoverageArea.Formatting, DocumentDiffCoverageStatus.Partial),
        };
        var warnings = new List<DocumentDiffWarning> { DocumentDiffWarning.PartialCoverage };
        DocumentDiffSession session = Session(coverageAreas, warnings);

        DocumentDiffSessionResult result = DocumentDiffSessionResult.Ready(session);
        coverageAreas.Clear();
        warnings.Clear();

        AssertJsonEquals(
            """
            {
              "outcome": "ready",
              "session": {
                "contractVersion": "2.0",
                "sessionId": "11111111-1111-4111-8111-111111111111",
                "entryHandle": "workspace-document://entry-1",
                "historicalRevisionId": "22222222-2222-4222-8222-222222222222",
                "effectiveRevisionId": "33333333-3333-4333-8333-333333333333",
                "format": "docx",
                "provider": "builtIn",
                "fidelity": "structural",
                "summary": {
                  "totalChangeGroups": 3,
                  "rawRevisionCount": 4,
                  "insertions": 1,
                  "deletions": 0,
                  "replacements": 1,
                  "moves": 0,
                  "formattingChanges": 1,
                  "tableChanges": 0,
                  "commentChanges": 0,
                  "otherChanges": 0
                },
                "coverage": {
                  "areas": [
                    { "area": "visibleText", "status": "covered" },
                    { "area": "formatting", "status": "partial" }
                  ],
                  "truncated": false
                },
                "warnings": ["partialCoverage"],
                "canOpenComparisonArtifact": true,
                "canExportComparisonArtifact": false
              },
              "failure": null
            }
            """,
            JsonSerializer.Serialize(result));
        Assert.HasCount(2, session.Coverage.Areas);
        Assert.HasCount(1, session.Warnings);
    }

    [TestMethod]
    public void FailureResults_SerializeWithoutPartiallyUsableValues()
    {
        AssertJsonEquals(
            """{"outcome":"failure","session":null,"failure":"stale"}""",
            JsonSerializer.Serialize(
                DocumentDiffSessionResult.Failed(DocumentDiffSessionFailure.Stale)));
        AssertJsonEquals(
            """{"outcome":"failure","page":null,"failure":"cancelled"}""",
            JsonSerializer.Serialize(
                DocumentDiffChangePageResult.Failed(DocumentDiffPageFailure.Cancelled)));
    }

    [TestMethod]
    public void Session_RejectsInvalidProviderFormatOrFidelityCombinations()
    {
        Assert.ThrowsExactly<ArgumentException>(() => Session(
            format: DocumentDiffFormat.Xlsx,
            provider: DocumentDiffProvider.BuiltIn));
        Assert.ThrowsExactly<ArgumentException>(() => Session(
            provider: DocumentDiffProvider.WordNative,
            fidelity: DocumentDiffFidelity.Structural));
        Assert.ThrowsExactly<ArgumentException>(() => Session(
            format: DocumentDiffFormat.Text,
            provider: DocumentDiffProvider.XlsxBuiltIn,
            fidelity: DocumentDiffFidelity.OfficeNative));
    }

    [TestMethod]
    public void SummaryCoverageAndWarnings_RejectContradictoryStates()
    {
        Assert.ThrowsExactly<ArgumentException>(() =>
            new DocumentDiffSummary(2, 4, 1, 0, 0, 0, 0, 0, 0, 0));
        Assert.ThrowsExactly<ArgumentOutOfRangeException>(() =>
            new DocumentDiffSummary(0, -1, 0, 0, 0, 0, 0, 0, 0, 0));
        Assert.ThrowsExactly<ArgumentException>(() => new DocumentDiffCoverage(
        [
            new(DocumentDiffCoverageArea.TextBoxes, DocumentDiffCoverageStatus.Covered),
            new(DocumentDiffCoverageArea.TextBoxes, DocumentDiffCoverageStatus.NotCovered),
        ], truncated: false));
        Assert.ThrowsExactly<ArgumentException>(() => Session(
            coverage: new DocumentDiffCoverage(
            [
                new(DocumentDiffCoverageArea.VisibleText, DocumentDiffCoverageStatus.Covered),
            ], truncated: true),
            warnings: []));
        Assert.ThrowsExactly<ArgumentException>(() => Session(
            warnings: [DocumentDiffWarning.ResultTruncated]));
        Assert.ThrowsExactly<ArgumentException>(() => Session(
            coverageAreas:
            [
                new(DocumentDiffCoverageArea.Formatting, DocumentDiffCoverageStatus.Partial),
            ],
            warnings: []));
    }

    [TestMethod]
    public void RichChangeAndLocations_RejectImpossibleStates()
    {
        DocumentDiffRichSnippet before = Snippet("100", DocumentDiffRichRunRole.Deleted);
        DocumentDiffRichSnippet after = Snippet("120", DocumentDiffRichRunRole.Inserted);

        Assert.ThrowsExactly<ArgumentException>(() => new DocumentDiffChange(
            ChangeId,
            DocumentDiffChangeKind.Insert,
            new DocumentDiffLocation(DocumentDiffPart.Body),
            before,
            after,
            DocumentDiffConfidence.Exact));
        Assert.ThrowsExactly<ArgumentOutOfRangeException>(() => new DocumentDiffLocation(
            DocumentDiffPart.Body,
            paragraphIndex: -1));
        Assert.ThrowsExactly<ArgumentException>(() => new DocumentDiffLocation(
            DocumentDiffPart.Worksheet,
            sheetName: " "));
        Assert.ThrowsExactly<ArgumentOutOfRangeException>(() => new DocumentDiffRichRun(
            "text",
            DocumentDiffRichRunRole.Context,
            fontSizePt: double.NaN));
        _ = new DocumentDiffChange(
            ChangeId,
            DocumentDiffChangeKind.Other,
            new DocumentDiffLocation(DocumentDiffPart.Body),
            before: null,
            after: null,
            DocumentDiffConfidence.Exact);
    }

    [TestMethod]
    public void ReadyPage_SerializesAndSnapshotsChangesBoundToItsRequest()
    {
        var changes = new List<DocumentDiffChange> { Change() };
        var request = new DocumentDiffChangePageRequest(SessionId, "cursor-1", limit: 50);

        DocumentDiffChangePageResult result = DocumentDiffChangePageResult.Ready(
            request,
            SessionId,
            changes,
            "cursor-2");
        changes.Clear();

        AssertJsonEquals(
            """
            {
              "outcome": "ready",
              "page": {
                "sessionId": "11111111-1111-4111-8111-111111111111",
                "changes": [{
                  "changeId": "44444444-4444-4444-8444-444444444444",
                  "kind": "replace",
                  "location": {
                    "part": "body",
                    "sectionIndex": null,
                    "paragraphIndex": 5,
                    "nearestHeading": "第二章 合同金额",
                    "tableIndex": null,
                    "rowIndex": null,
                    "columnIndex": null,
                    "sheetName": null,
                    "cellAddress": null
                  },
                  "before": {
                    "runs": [{
                      "text": "100",
                      "role": "deleted",
                      "bold": null,
                      "italic": null,
                      "underline": null,
                      "strike": null,
                      "fontSizePt": null,
                      "fontFamily": null,
                      "foreground": null,
                      "background": null,
                      "styleName": null
                    }]
                  },
                  "after": {
                    "runs": [{
                      "text": "120",
                      "role": "inserted",
                      "bold": true,
                      "italic": null,
                      "underline": null,
                      "strike": null,
                      "fontSizePt": null,
                      "fontFamily": null,
                      "foreground": null,
                      "background": null,
                      "styleName": null
                    }]
                  },
                  "confidence": "exact"
                }],
                "nextCursor": "cursor-2"
              },
              "failure": null
            }
            """,
            JsonSerializer.Serialize(result));
        Assert.HasCount(1, result.Page!.Changes);
    }

    [TestMethod]
    public void ReadyPage_RejectsCrossSessionAndNonAdvancingPages()
    {
        var request = new DocumentDiffChangePageRequest(SessionId, "cursor-1", limit: 1);
        Guid otherSession = Guid.Parse("55555555-5555-4555-8555-555555555555");

        Assert.ThrowsExactly<ArgumentException>(() =>
            DocumentDiffChangePageResult.Ready(request, otherSession, [Change()], null));
        Assert.ThrowsExactly<ArgumentException>(() =>
            DocumentDiffChangePageResult.Ready(request, SessionId, [], "cursor-2"));
        Assert.ThrowsExactly<ArgumentException>(() =>
            DocumentDiffChangePageResult.Ready(request, SessionId, [Change()], "cursor-1"));
        Assert.ThrowsExactly<ArgumentException>(() =>
            DocumentDiffChangePageResult.Ready(request, SessionId, [Change(), Change()], null));
        Assert.ThrowsExactly<ArgumentException>(() => DocumentDiffChangePageResult.Ready(
            request,
            SessionId,
            [Change(), Change(Guid.Parse("66666666-6666-4666-8666-666666666666"))],
            null));
    }

    [TestMethod]
    public void PageRequest_EnforcesTheClosedLimitAndCanonicalSessionIdentity()
    {
        Assert.ThrowsExactly<ArgumentException>(() =>
            new DocumentDiffChangePageRequest(Guid.Empty, null, 1));
        Assert.ThrowsExactly<ArgumentOutOfRangeException>(() =>
            new DocumentDiffChangePageRequest(SessionId, null, 0));
        Assert.ThrowsExactly<ArgumentOutOfRangeException>(() =>
            new DocumentDiffChangePageRequest(SessionId, null, 201));
        Assert.ThrowsExactly<ArgumentException>(() =>
            new DocumentDiffChangePageRequest(SessionId, " ", 1));
        Assert.ThrowsExactly<ArgumentException>(() => new DocumentDiffChangePageRequest(
            Guid.Parse("aaaaaaaa-aaaa-0aaa-0aaa-aaaaaaaaaaaa"),
            null,
            1));
    }

    private static DocumentDiffSession Session(
        IEnumerable<DocumentDiffCoverageEntry>? coverageAreas = null,
        IEnumerable<DocumentDiffWarning>? warnings = null,
        DocumentDiffFormat format = DocumentDiffFormat.Docx,
        DocumentDiffProvider provider = DocumentDiffProvider.BuiltIn,
        DocumentDiffFidelity fidelity = DocumentDiffFidelity.Structural,
        DocumentDiffCoverage? coverage = null)
    {
        return new DocumentDiffSession(
            SessionId,
            "workspace-document://entry-1",
            HistoricalRevisionId,
            EffectiveRevisionId,
            format,
            provider,
            fidelity,
            new DocumentDiffSummary(3, 4, 1, 0, 1, 0, 1, 0, 0, 0),
            coverage ?? new DocumentDiffCoverage(coverageAreas ??
            [
                new(DocumentDiffCoverageArea.VisibleText, DocumentDiffCoverageStatus.Covered),
            ], truncated: false),
            warnings ?? [],
            canOpenComparisonArtifact: true,
            canExportComparisonArtifact: false);
    }

    private static DocumentDiffChange Change(Guid? changeId = null)
    {
        return new DocumentDiffChange(
            changeId ?? ChangeId,
            DocumentDiffChangeKind.Replace,
            new DocumentDiffLocation(
                DocumentDiffPart.Body,
                paragraphIndex: 5,
                nearestHeading: "第二章 合同金额"),
            Snippet("100", DocumentDiffRichRunRole.Deleted),
            new DocumentDiffRichSnippet(
            [
                new DocumentDiffRichRun(
                    "120",
                    DocumentDiffRichRunRole.Inserted,
                    bold: true),
            ]),
            DocumentDiffConfidence.Exact);
    }

    private static DocumentDiffRichSnippet Snippet(
        string text,
        DocumentDiffRichRunRole role)
    {
        return new DocumentDiffRichSnippet(
        [
            new DocumentDiffRichRun(text, role),
        ]);
    }

    private static void AssertJsonEquals(string expected, string actual)
    {
        using JsonDocument expectedDocument = JsonDocument.Parse(expected);
        using JsonDocument actualDocument = JsonDocument.Parse(actual);
        Assert.IsTrue(
            JsonElement.DeepEquals(expectedDocument.RootElement, actualDocument.RootElement),
            $"Expected: {expectedDocument.RootElement}{Environment.NewLine}"
                + $"Actual: {actualDocument.RootElement}");
    }
}
