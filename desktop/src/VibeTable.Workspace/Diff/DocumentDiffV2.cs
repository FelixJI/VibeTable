using System.Text.Json.Serialization;
using System.Text.RegularExpressions;

namespace VibeTable.Workspace.Diff;

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffFormat>))]
public enum DocumentDiffFormat
{
    [JsonStringEnumMemberName("docx")] Docx,
    [JsonStringEnumMemberName("xlsx")] Xlsx,
    [JsonStringEnumMemberName("text")] Text,
    [JsonStringEnumMemberName("binary")] Binary,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffProvider>))]
public enum DocumentDiffProvider
{
    [JsonStringEnumMemberName("builtIn")] BuiltIn,
    [JsonStringEnumMemberName("wordNative")] WordNative,
    [JsonStringEnumMemberName("xlsxBuiltIn")] XlsxBuiltIn,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffFidelity>))]
public enum DocumentDiffFidelity
{
    [JsonStringEnumMemberName("structural")] Structural,
    [JsonStringEnumMemberName("officeNative")] OfficeNative,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffCoverageArea>))]
public enum DocumentDiffCoverageArea
{
    [JsonStringEnumMemberName("visibleText")] VisibleText,
    [JsonStringEnumMemberName("structure")] Structure,
    [JsonStringEnumMemberName("formatting")] Formatting,
    [JsonStringEnumMemberName("tables")] Tables,
    [JsonStringEnumMemberName("headersFooters")] HeadersFooters,
    [JsonStringEnumMemberName("notes")] Notes,
    [JsonStringEnumMemberName("textBoxes")] TextBoxes,
    [JsonStringEnumMemberName("comments")] Comments,
    [JsonStringEnumMemberName("images")] Images,
    [JsonStringEnumMemberName("fields")] Fields,
    [JsonStringEnumMemberName("pagination")] Pagination,
    [JsonStringEnumMemberName("worksheetValues")] WorksheetValues,
    [JsonStringEnumMemberName("worksheetFormulas")] WorksheetFormulas,
    [JsonStringEnumMemberName("worksheetStyles")] WorksheetStyles,
    [JsonStringEnumMemberName("worksheetMerges")] WorksheetMerges,
    [JsonStringEnumMemberName("worksheetVisibility")] WorksheetVisibility,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffCoverageStatus>))]
public enum DocumentDiffCoverageStatus
{
    [JsonStringEnumMemberName("covered")] Covered,
    [JsonStringEnumMemberName("partial")] Partial,
    [JsonStringEnumMemberName("notCovered")] NotCovered,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffWarning>))]
public enum DocumentDiffWarning
{
    [JsonStringEnumMemberName("resultTruncated")] ResultTruncated,
    [JsonStringEnumMemberName("partialCoverage")] PartialCoverage,
    [JsonStringEnumMemberName("existingRevisionsNormalized")] ExistingRevisionsNormalized,
    [JsonStringEnumMemberName("cachedValuesNotRecalculated")] CachedValuesNotRecalculated,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffSessionFailure>))]
public enum DocumentDiffSessionFailure
{
    [JsonStringEnumMemberName("unsupported")] Unsupported,
    [JsonStringEnumMemberName("invalidContent")] InvalidContent,
    [JsonStringEnumMemberName("io")] Io,
    [JsonStringEnumMemberName("cancelled")] Cancelled,
    [JsonStringEnumMemberName("stale")] Stale,
    [JsonStringEnumMemberName("providerUnavailable")] ProviderUnavailable,
    [JsonStringEnumMemberName("timeout")] Timeout,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffPageFailure>))]
public enum DocumentDiffPageFailure
{
    [JsonStringEnumMemberName("sessionExpired")] SessionExpired,
    [JsonStringEnumMemberName("invalidCursor")] InvalidCursor,
    [JsonStringEnumMemberName("cancelled")] Cancelled,
    [JsonStringEnumMemberName("stale")] Stale,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffResultOutcome>))]
public enum DocumentDiffResultOutcome
{
    [JsonStringEnumMemberName("ready")] Ready,
    [JsonStringEnumMemberName("failure")] Failure,
}

public sealed record DocumentDiffSummary
{
    public DocumentDiffSummary(
        int totalChangeGroups,
        int rawRevisionCount,
        int insertions,
        int deletions,
        int replacements,
        int moves,
        int formattingChanges,
        int tableChanges,
        int commentChanges,
        int otherChanges)
    {
        int[] counts =
        [
            totalChangeGroups,
            rawRevisionCount,
            insertions,
            deletions,
            replacements,
            moves,
            formattingChanges,
            tableChanges,
            commentChanges,
            otherChanges,
        ];
        if (counts.Any(count => count < 0))
        {
            throw new ArgumentOutOfRangeException(nameof(totalChangeGroups));
        }

        long classifiedTotal = (long)insertions + deletions + replacements + moves
            + formattingChanges + tableChanges + commentChanges + otherChanges;
        if (classifiedTotal != totalChangeGroups)
        {
            throw new ArgumentException(
                "Total change groups must equal the classified change counts.",
                nameof(totalChangeGroups));
        }

        TotalChangeGroups = totalChangeGroups;
        RawRevisionCount = rawRevisionCount;
        Insertions = insertions;
        Deletions = deletions;
        Replacements = replacements;
        Moves = moves;
        FormattingChanges = formattingChanges;
        TableChanges = tableChanges;
        CommentChanges = commentChanges;
        OtherChanges = otherChanges;
    }

    [JsonPropertyName("totalChangeGroups")] public int TotalChangeGroups { get; }
    [JsonPropertyName("rawRevisionCount")] public int RawRevisionCount { get; }
    [JsonPropertyName("insertions")] public int Insertions { get; }
    [JsonPropertyName("deletions")] public int Deletions { get; }
    [JsonPropertyName("replacements")] public int Replacements { get; }
    [JsonPropertyName("moves")] public int Moves { get; }
    [JsonPropertyName("formattingChanges")] public int FormattingChanges { get; }
    [JsonPropertyName("tableChanges")] public int TableChanges { get; }
    [JsonPropertyName("commentChanges")] public int CommentChanges { get; }
    [JsonPropertyName("otherChanges")] public int OtherChanges { get; }
}

public sealed record DocumentDiffCoverageEntry
{
    public DocumentDiffCoverageEntry(
        DocumentDiffCoverageArea area,
        DocumentDiffCoverageStatus status)
    {
        if (!Enum.IsDefined(area))
        {
            throw new ArgumentOutOfRangeException(nameof(area));
        }
        if (!Enum.IsDefined(status))
        {
            throw new ArgumentOutOfRangeException(nameof(status));
        }
        Area = area;
        Status = status;
    }

    [JsonPropertyName("area")] public DocumentDiffCoverageArea Area { get; }
    [JsonPropertyName("status")] public DocumentDiffCoverageStatus Status { get; }
}

public sealed record DocumentDiffCoverage
{
    public DocumentDiffCoverage(
        IEnumerable<DocumentDiffCoverageEntry> areas,
        bool truncated)
    {
        ArgumentNullException.ThrowIfNull(areas);
        DocumentDiffCoverageEntry[] snapshot = areas.ToArray();
        if (snapshot.Length == 0 || snapshot.Any(area => area is null))
        {
            throw new ArgumentException("Coverage requires non-null areas.", nameof(areas));
        }
        if (snapshot.Select(area => area.Area).Distinct().Count() != snapshot.Length)
        {
            throw new ArgumentException("Coverage areas must be unique.", nameof(areas));
        }
        Areas = Array.AsReadOnly(snapshot);
        Truncated = truncated;
    }

    [JsonPropertyName("areas")] public IReadOnlyList<DocumentDiffCoverageEntry> Areas { get; }
    [JsonPropertyName("truncated")] public bool Truncated { get; }
}

public sealed record DocumentDiffSession
{
    public const string CurrentContractVersion = "2.0";

    public DocumentDiffSession(
        Guid sessionId,
        string entryHandle,
        Guid historicalRevisionId,
        Guid effectiveRevisionId,
        DocumentDiffFormat format,
        DocumentDiffProvider provider,
        DocumentDiffFidelity fidelity,
        DocumentDiffSummary summary,
        DocumentDiffCoverage coverage,
        IEnumerable<DocumentDiffWarning> warnings,
        bool canOpenComparisonArtifact,
        bool canExportComparisonArtifact)
    {
        DocumentDiffV2Guards.RequireId(sessionId, nameof(sessionId));
        ArgumentException.ThrowIfNullOrWhiteSpace(entryHandle);
        DocumentDiffV2Guards.RequireId(historicalRevisionId, nameof(historicalRevisionId));
        DocumentDiffV2Guards.RequireId(effectiveRevisionId, nameof(effectiveRevisionId));
        ValidateProvider(format, provider, fidelity);
        ArgumentNullException.ThrowIfNull(summary);
        ArgumentNullException.ThrowIfNull(coverage);
        ArgumentNullException.ThrowIfNull(warnings);
        DocumentDiffWarning[] warningSnapshot = warnings.ToArray();
        if (warningSnapshot.Any(warning => !Enum.IsDefined(warning)))
        {
            throw new ArgumentOutOfRangeException(nameof(warnings));
        }
        if (warningSnapshot.Distinct().Count() != warningSnapshot.Length)
        {
            throw new ArgumentException("Warnings must be unique.", nameof(warnings));
        }
        bool warnsAboutTruncation = warningSnapshot.Contains(DocumentDiffWarning.ResultTruncated);
        if (coverage.Truncated != warnsAboutTruncation)
        {
            throw new ArgumentException(
                "Truncated coverage and the result-truncated warning must agree.",
                nameof(warnings));
        }
        bool warnsAboutPartialCoverage = warningSnapshot.Contains(DocumentDiffWarning.PartialCoverage);
        bool hasPartialCoverage = coverage.Areas.Any(
            area => area.Status != DocumentDiffCoverageStatus.Covered);
        if (hasPartialCoverage != warnsAboutPartialCoverage)
        {
            throw new ArgumentException(
                "Partial coverage and the partial-coverage warning must agree.",
                nameof(warnings));
        }

        SessionId = sessionId;
        EntryHandle = entryHandle;
        HistoricalRevisionId = historicalRevisionId;
        EffectiveRevisionId = effectiveRevisionId;
        Format = format;
        Provider = provider;
        Fidelity = fidelity;
        Summary = summary;
        Coverage = coverage;
        Warnings = Array.AsReadOnly(warningSnapshot);
        CanOpenComparisonArtifact = canOpenComparisonArtifact;
        CanExportComparisonArtifact = canExportComparisonArtifact;
    }

    [JsonPropertyName("contractVersion")] public string ContractVersion => CurrentContractVersion;
    [JsonPropertyName("sessionId")] public Guid SessionId { get; }
    [JsonPropertyName("entryHandle")] public string EntryHandle { get; }
    [JsonPropertyName("historicalRevisionId")] public Guid HistoricalRevisionId { get; }
    [JsonPropertyName("effectiveRevisionId")] public Guid EffectiveRevisionId { get; }
    [JsonPropertyName("format")] public DocumentDiffFormat Format { get; }
    [JsonPropertyName("provider")] public DocumentDiffProvider Provider { get; }
    [JsonPropertyName("fidelity")] public DocumentDiffFidelity Fidelity { get; }
    [JsonPropertyName("summary")] public DocumentDiffSummary Summary { get; }
    [JsonPropertyName("coverage")] public DocumentDiffCoverage Coverage { get; }
    [JsonPropertyName("warnings")] public IReadOnlyList<DocumentDiffWarning> Warnings { get; }
    [JsonPropertyName("canOpenComparisonArtifact")] public bool CanOpenComparisonArtifact { get; }
    [JsonPropertyName("canExportComparisonArtifact")] public bool CanExportComparisonArtifact { get; }

    private static void ValidateProvider(
        DocumentDiffFormat format,
        DocumentDiffProvider provider,
        DocumentDiffFidelity fidelity)
    {
        bool valid = (format, provider, fidelity) switch
        {
            (DocumentDiffFormat.Docx, DocumentDiffProvider.BuiltIn,
                DocumentDiffFidelity.Structural) => true,
            (DocumentDiffFormat.Docx, DocumentDiffProvider.WordNative,
                DocumentDiffFidelity.OfficeNative) => true,
            (DocumentDiffFormat.Xlsx, DocumentDiffProvider.XlsxBuiltIn,
                DocumentDiffFidelity.Structural) => true,
            (DocumentDiffFormat.Text or DocumentDiffFormat.Binary,
                DocumentDiffProvider.BuiltIn,
                DocumentDiffFidelity.Structural) => true,
            _ => false,
        };
        if (!valid)
        {
            throw new ArgumentException("Format, provider, and fidelity are incompatible.");
        }
    }

}

public sealed record DocumentDiffSessionResult
{
    private DocumentDiffSessionResult(
        DocumentDiffResultOutcome outcome,
        DocumentDiffSession? session,
        DocumentDiffSessionFailure? failure)
    {
        Outcome = outcome;
        Session = session;
        Failure = failure;
    }

    [JsonPropertyName("outcome")] public DocumentDiffResultOutcome Outcome { get; }
    [JsonPropertyName("session")] public DocumentDiffSession? Session { get; }
    [JsonPropertyName("failure")] public DocumentDiffSessionFailure? Failure { get; }

    public static DocumentDiffSessionResult Ready(DocumentDiffSession session)
    {
        ArgumentNullException.ThrowIfNull(session);
        return new DocumentDiffSessionResult(DocumentDiffResultOutcome.Ready, session, null);
    }

    public static DocumentDiffSessionResult Failed(DocumentDiffSessionFailure failure)
    {
        if (!Enum.IsDefined(failure))
        {
            throw new ArgumentOutOfRangeException(nameof(failure));
        }
        return new DocumentDiffSessionResult(DocumentDiffResultOutcome.Failure, null, failure);
    }
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffChangeKind>))]
public enum DocumentDiffChangeKind
{
    [JsonStringEnumMemberName("insert")] Insert,
    [JsonStringEnumMemberName("delete")] Delete,
    [JsonStringEnumMemberName("replace")] Replace,
    [JsonStringEnumMemberName("move")] Move,
    [JsonStringEnumMemberName("format")] Format,
    [JsonStringEnumMemberName("table")] Table,
    [JsonStringEnumMemberName("comment")] Comment,
    [JsonStringEnumMemberName("other")] Other,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffConfidence>))]
public enum DocumentDiffConfidence
{
    [JsonStringEnumMemberName("exact")] Exact,
    [JsonStringEnumMemberName("normalized")] Normalized,
    [JsonStringEnumMemberName("heuristic")] Heuristic,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffPart>))]
public enum DocumentDiffPart
{
    [JsonStringEnumMemberName("body")] Body,
    [JsonStringEnumMemberName("header")] Header,
    [JsonStringEnumMemberName("footer")] Footer,
    [JsonStringEnumMemberName("footnote")] Footnote,
    [JsonStringEnumMemberName("endnote")] Endnote,
    [JsonStringEnumMemberName("textBox")] TextBox,
    [JsonStringEnumMemberName("worksheet")] Worksheet,
}

[JsonConverter(typeof(JsonStringEnumConverter<DocumentDiffRichRunRole>))]
public enum DocumentDiffRichRunRole
{
    [JsonStringEnumMemberName("context")] Context,
    [JsonStringEnumMemberName("inserted")] Inserted,
    [JsonStringEnumMemberName("deleted")] Deleted,
    [JsonStringEnumMemberName("changed")] Changed,
}

public sealed record DocumentDiffRichRun
{
    public DocumentDiffRichRun(
        string text,
        DocumentDiffRichRunRole role,
        bool? bold = null,
        bool? italic = null,
        bool? underline = null,
        bool? strike = null,
        double? fontSizePt = null,
        string? fontFamily = null,
        string? foreground = null,
        string? background = null,
        string? styleName = null)
    {
        ArgumentNullException.ThrowIfNull(text);
        if (text.Length == 0)
        {
            throw new ArgumentException("Rich run text cannot be empty.", nameof(text));
        }
        if (!Enum.IsDefined(role))
        {
            throw new ArgumentOutOfRangeException(nameof(role));
        }
        if (fontSizePt is not null
            && (!double.IsFinite(fontSizePt.Value) || fontSizePt.Value <= 0))
        {
            throw new ArgumentOutOfRangeException(nameof(fontSizePt));
        }
        ValidateOptionalText(fontFamily, nameof(fontFamily));
        ValidateOptionalText(foreground, nameof(foreground));
        ValidateOptionalText(background, nameof(background));
        ValidateOptionalText(styleName, nameof(styleName));
        Text = text;
        Role = role;
        Bold = bold;
        Italic = italic;
        Underline = underline;
        Strike = strike;
        FontSizePt = fontSizePt;
        FontFamily = fontFamily;
        Foreground = foreground;
        Background = background;
        StyleName = styleName;
    }

    [JsonPropertyName("text")] public string Text { get; }
    [JsonPropertyName("role")] public DocumentDiffRichRunRole Role { get; }
    [JsonPropertyName("bold")] public bool? Bold { get; }
    [JsonPropertyName("italic")] public bool? Italic { get; }
    [JsonPropertyName("underline")] public bool? Underline { get; }
    [JsonPropertyName("strike")] public bool? Strike { get; }
    [JsonPropertyName("fontSizePt")] public double? FontSizePt { get; }
    [JsonPropertyName("fontFamily")] public string? FontFamily { get; }
    [JsonPropertyName("foreground")] public string? Foreground { get; }
    [JsonPropertyName("background")] public string? Background { get; }
    [JsonPropertyName("styleName")] public string? StyleName { get; }

    private static void ValidateOptionalText(string? value, string parameterName)
    {
        if (value is not null && string.IsNullOrWhiteSpace(value))
        {
            throw new ArgumentException("Optional rich run text cannot be blank.", parameterName);
        }
    }
}

public sealed record DocumentDiffRichSnippet
{
    public DocumentDiffRichSnippet(IEnumerable<DocumentDiffRichRun> runs)
    {
        ArgumentNullException.ThrowIfNull(runs);
        DocumentDiffRichRun[] snapshot = runs.ToArray();
        if (snapshot.Length == 0 || snapshot.Any(run => run is null))
        {
            throw new ArgumentException("A rich snippet requires non-null runs.", nameof(runs));
        }
        Runs = Array.AsReadOnly(snapshot);
    }

    [JsonPropertyName("runs")] public IReadOnlyList<DocumentDiffRichRun> Runs { get; }
}

public sealed record DocumentDiffLocation
{
    public DocumentDiffLocation(
        DocumentDiffPart part,
        int? sectionIndex = null,
        int? paragraphIndex = null,
        string? nearestHeading = null,
        int? tableIndex = null,
        int? rowIndex = null,
        int? columnIndex = null,
        string? sheetName = null,
        string? cellAddress = null)
    {
        if (!Enum.IsDefined(part))
        {
            throw new ArgumentOutOfRangeException(nameof(part));
        }
        ValidateIndex(sectionIndex, nameof(sectionIndex));
        ValidateIndex(paragraphIndex, nameof(paragraphIndex));
        ValidateIndex(tableIndex, nameof(tableIndex));
        ValidateIndex(rowIndex, nameof(rowIndex));
        ValidateIndex(columnIndex, nameof(columnIndex));
        ValidateOptionalText(nearestHeading, nameof(nearestHeading));
        ValidateOptionalText(sheetName, nameof(sheetName));
        ValidateOptionalText(cellAddress, nameof(cellAddress));
        if (part == DocumentDiffPart.Worksheet)
        {
            if (string.IsNullOrWhiteSpace(sheetName)
                || sectionIndex is not null
                || paragraphIndex is not null
                || nearestHeading is not null
                || tableIndex is not null)
            {
                throw new ArgumentException("Worksheet locations require worksheet coordinates.");
            }
        }
        else if (sheetName is not null || cellAddress is not null)
        {
            throw new ArgumentException("Document locations cannot contain worksheet coordinates.");
        }
        if (part is DocumentDiffPart.Header or DocumentDiffPart.Footer
            && sectionIndex is null)
        {
            throw new ArgumentException("Header and footer locations require a section index.");
        }
        if (part != DocumentDiffPart.Worksheet
            && (rowIndex is not null || columnIndex is not null)
            && tableIndex is null)
        {
            throw new ArgumentException("Document rows and columns require a table index.");
        }

        Part = part;
        SectionIndex = sectionIndex;
        ParagraphIndex = paragraphIndex;
        NearestHeading = nearestHeading;
        TableIndex = tableIndex;
        RowIndex = rowIndex;
        ColumnIndex = columnIndex;
        SheetName = sheetName;
        CellAddress = cellAddress;
    }

    [JsonPropertyName("part")] public DocumentDiffPart Part { get; }
    [JsonPropertyName("sectionIndex")] public int? SectionIndex { get; }
    [JsonPropertyName("paragraphIndex")] public int? ParagraphIndex { get; }
    [JsonPropertyName("nearestHeading")] public string? NearestHeading { get; }
    [JsonPropertyName("tableIndex")] public int? TableIndex { get; }
    [JsonPropertyName("rowIndex")] public int? RowIndex { get; }
    [JsonPropertyName("columnIndex")] public int? ColumnIndex { get; }
    [JsonPropertyName("sheetName")] public string? SheetName { get; }
    [JsonPropertyName("cellAddress")] public string? CellAddress { get; }

    private static void ValidateIndex(int? value, string parameterName)
    {
        if (value < 0)
        {
            throw new ArgumentOutOfRangeException(parameterName);
        }
    }

    private static void ValidateOptionalText(string? value, string parameterName)
    {
        if (value is not null && string.IsNullOrWhiteSpace(value))
        {
            throw new ArgumentException("Optional location text cannot be blank.", parameterName);
        }
    }
}

public sealed record DocumentDiffChange
{
    public DocumentDiffChange(
        Guid changeId,
        DocumentDiffChangeKind kind,
        DocumentDiffLocation location,
        DocumentDiffRichSnippet? before,
        DocumentDiffRichSnippet? after,
        DocumentDiffConfidence confidence)
    {
        DocumentDiffV2Guards.RequireId(changeId, nameof(changeId));
        if (!Enum.IsDefined(kind))
        {
            throw new ArgumentOutOfRangeException(nameof(kind));
        }
        if (!Enum.IsDefined(confidence))
        {
            throw new ArgumentOutOfRangeException(nameof(confidence));
        }
        ArgumentNullException.ThrowIfNull(location);
        bool validSnippets = kind switch
        {
            DocumentDiffChangeKind.Insert => before is null && after is not null,
            DocumentDiffChangeKind.Delete => before is not null && after is null,
            DocumentDiffChangeKind.Replace
                or DocumentDiffChangeKind.Move
                or DocumentDiffChangeKind.Format => before is not null && after is not null,
            DocumentDiffChangeKind.Other => true,
            _ => before is not null || after is not null,
        };
        if (!validSnippets)
        {
            throw new ArgumentException("Change kind and snippets are incompatible.");
        }

        ChangeId = changeId;
        Kind = kind;
        Location = location;
        Before = before;
        After = after;
        Confidence = confidence;
    }

    [JsonPropertyName("changeId")] public Guid ChangeId { get; }
    [JsonPropertyName("kind")] public DocumentDiffChangeKind Kind { get; }
    [JsonPropertyName("location")] public DocumentDiffLocation Location { get; }
    [JsonPropertyName("before")] public DocumentDiffRichSnippet? Before { get; }
    [JsonPropertyName("after")] public DocumentDiffRichSnippet? After { get; }
    [JsonPropertyName("confidence")] public DocumentDiffConfidence Confidence { get; }
}

public sealed record DocumentDiffChangePageRequest
{
    public DocumentDiffChangePageRequest(Guid sessionId, string? cursor, int limit)
    {
        DocumentDiffV2Guards.RequireId(sessionId, nameof(sessionId));
        if (cursor is not null && string.IsNullOrWhiteSpace(cursor))
        {
            throw new ArgumentException("Cursor cannot be blank.", nameof(cursor));
        }
        if (limit is < 1 or > 200)
        {
            throw new ArgumentOutOfRangeException(nameof(limit));
        }
        SessionId = sessionId;
        Cursor = cursor;
        Limit = limit;
    }

    [JsonPropertyName("sessionId")] public Guid SessionId { get; }
    [JsonPropertyName("cursor")] public string? Cursor { get; }
    [JsonPropertyName("limit")] public int Limit { get; }
}

public sealed record DocumentDiffChangePage
{
    internal DocumentDiffChangePage(
        Guid sessionId,
        DocumentDiffChange[] changes,
        string? nextCursor)
    {
        SessionId = sessionId;
        Changes = Array.AsReadOnly(changes);
        NextCursor = nextCursor;
    }

    [JsonPropertyName("sessionId")] public Guid SessionId { get; }
    [JsonPropertyName("changes")] public IReadOnlyList<DocumentDiffChange> Changes { get; }
    [JsonPropertyName("nextCursor")] public string? NextCursor { get; }
}

public sealed record DocumentDiffChangePageResult
{
    private DocumentDiffChangePageResult(
        DocumentDiffResultOutcome outcome,
        DocumentDiffChangePage? page,
        DocumentDiffPageFailure? failure)
    {
        Outcome = outcome;
        Page = page;
        Failure = failure;
    }

    [JsonPropertyName("outcome")] public DocumentDiffResultOutcome Outcome { get; }
    [JsonPropertyName("page")] public DocumentDiffChangePage? Page { get; }
    [JsonPropertyName("failure")] public DocumentDiffPageFailure? Failure { get; }

    public static DocumentDiffChangePageResult Ready(
        DocumentDiffChangePageRequest request,
        Guid producedSessionId,
        IEnumerable<DocumentDiffChange> changes,
        string? nextCursor)
    {
        ArgumentNullException.ThrowIfNull(request);
        ArgumentNullException.ThrowIfNull(changes);
        DocumentDiffV2Guards.RequireId(producedSessionId, nameof(producedSessionId));
        if (producedSessionId != request.SessionId)
        {
            throw new ArgumentException("Page session does not match its request.", nameof(producedSessionId));
        }
        if (nextCursor is not null && string.IsNullOrWhiteSpace(nextCursor))
        {
            throw new ArgumentException("Next cursor cannot be blank.", nameof(nextCursor));
        }
        if (nextCursor is not null
            && string.Equals(nextCursor, request.Cursor, StringComparison.Ordinal))
        {
            throw new ArgumentException("Next cursor must advance.", nameof(nextCursor));
        }

        DocumentDiffChange[] snapshot = changes.ToArray();
        if (snapshot.Any(change => change is null))
        {
            throw new ArgumentException("Page changes cannot be null.", nameof(changes));
        }
        if (snapshot.Length > request.Limit)
        {
            throw new ArgumentException("Page exceeds the requested limit.", nameof(changes));
        }
        if (snapshot.Select(change => change.ChangeId).Distinct().Count() != snapshot.Length)
        {
            throw new ArgumentException("Page change ids must be unique.", nameof(changes));
        }
        if (nextCursor is not null && snapshot.Length == 0)
        {
            throw new ArgumentException("A non-terminal page cannot be empty.", nameof(changes));
        }

        var page = new DocumentDiffChangePage(producedSessionId, snapshot, nextCursor);
        return new DocumentDiffChangePageResult(DocumentDiffResultOutcome.Ready, page, null);
    }

    public static DocumentDiffChangePageResult Failed(DocumentDiffPageFailure failure)
    {
        if (!Enum.IsDefined(failure))
        {
            throw new ArgumentOutOfRangeException(nameof(failure));
        }
        return new DocumentDiffChangePageResult(DocumentDiffResultOutcome.Failure, null, failure);
    }
}

internal static class DocumentDiffV2Guards
{
    private static readonly Regex CanonicalUuidPattern = new(
        "^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
        RegexOptions.CultureInvariant);

    public static void RequireId(Guid value, string parameterName)
    {
        if (!CanonicalUuidPattern.IsMatch(value.ToString("D")))
        {
            throw new ArgumentException("Identity must be a canonical UUID.", parameterName);
        }
    }
}
