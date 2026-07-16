using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// The paste anchor — the grid cell the parsed clipboard rectangle's top-left
/// corner aligns to. <c>RowKey</c> is null when the user anchors the paste past
/// the last row (an append). Mirrors <c>backend.contracts.paste.PasteStartCell</c>.
/// </summary>
public sealed record PasteStartCell(object? RowKey, string Column);

/// <summary>
/// One cell produced by clipboard parsing. <c>RowIndex</c>/<c>ColumnIndex</c>
/// are 0-based offsets into the parsed rectangle (not the collection row id).
/// Mirrors <c>backend.contracts.paste.PasteCell</c>.
/// </summary>
public sealed record PasteCell(
    int RowIndex,
    int ColumnIndex,
    string? Column,
    string RawValue,
    object? ParsedValue);

/// <summary>
/// Parameters for <c>table.previewPaste</c>. Mirrors
/// <c>backend.contracts.paste.PreviewPasteParams</c>. <c>Selection</c> is the B3
/// selection snapshot the host rendered; it carries the row keys the update rows
/// map onto across remote pages.
/// </summary>
public sealed record PreviewPasteParams(
    string Collection,
    string SchemaRevision,
    IReadOnlyDictionary<string, object?> Selection,
    PasteStartCell StartCell,
    IReadOnlyList<IReadOnlyList<PasteCell>> Cells);

/// <summary>
/// Severity for a paste-cell diagnostic. <c>error</c> blocks apply;
/// <c>warning</c> requires explicit user acknowledgement.
/// </summary>
public static class PasteDiagnosticSeverities
{
    public const string Error = "error";
    public const string Warning = "warning";
}

/// <summary>
/// A localized diagnostic for one paste cell. Mirrors
/// <c>backend.contracts.paste.PasteCellDiagnostic</c>.
/// </summary>
public sealed record PasteCellDiagnostic(
    int RowIndex,
    int ColumnIndex,
    string Severity,
    string Code,
    string Message);

/// <summary>
/// One planned change to a target row. For <c>update</c> rows
/// <c>TargetRowKey</c> is the resolved row and <c>ExpectedDateUpdated</c> is
/// the revision the server must still observe when the plan is applied. For
/// <c>insert</c> rows <c>TargetRowKey</c> is null. Mirrors
/// <c>backend.contracts.paste.PastePlanRow</c>.
/// </summary>
public sealed record PastePlanRow(
    string Kind,
    object? TargetRowKey,
    string? ExpectedDateUpdated,
    IReadOnlyDictionary<string, IReadOnlyDictionary<string, object?>> Changes,
    IReadOnlyList<PasteCellDiagnostic> Diagnostics);

/// <summary>
/// Aggregated counts for a <see cref="PastePlan"/>. Mirrors
/// <c>backend.contracts.paste.PasteSummary</c>.
/// </summary>
public sealed record PasteSummary(
    int UpdateRows,
    int InsertRows,
    int SkipRows,
    int ErrorCount,
    int WarningCount);

/// <summary>
/// An opaque, single-use, server-bound handle authorizing one apply. The host
/// must not interpret its contents. <c>ExpiresAt</c> is a Unix timestamp
/// (seconds). Mirrors <c>backend.contracts.paste.PasteToken</c>.
/// </summary>
public sealed record PasteToken(string Token, double ExpiresAt, bool Consumed);

/// <summary>
/// Result of <c>table.previewPaste</c>. Mirrors
/// <c>backend.contracts.paste.PastePlan</c>.
/// </summary>
public sealed record PastePlan(
    string Collection,
    string SchemaRevision,
    string CapabilityHash,
    PasteSummary Summary,
    IReadOnlyList<PastePlanRow> Rows,
    IReadOnlyList<PasteCellDiagnostic> Diagnostics,
    PasteToken Token,
    bool Overflow);

/// <summary>
/// Parameters for <c>table.applyPaste</c>. <c>IdempotencyKey</c> is reused on a
/// retry so the server can return the original result instead of replaying the
/// write. Mirrors <c>backend.contracts.paste.ApplyPasteParams</c>.
/// </summary>
public sealed record ApplyPasteParams(
    string Collection,
    string Token,
    string IdempotencyKey);

/// <summary>
/// Outcome of an apply. <c>committed</c> is the all-or-nothing success case;
/// <c>conflict</c> means one or more target rows changed since preview;
/// <c>pending</c> means the request timed out and the confirmed result is
/// unknown (the host polls by idempotency key).
/// </summary>
public static class ApplyOutcomes
{
    public const string Committed = "committed";
    public const string Conflict = "conflict";
    public const string Pending = "pending";
}

/// <summary>
/// One row that blocked an apply because its revision changed. Mirrors
/// <c>backend.contracts.paste.ApplyPasteConflict</c>.
/// </summary>
public sealed record ApplyPasteConflict(
    object RowKey,
    IReadOnlyDictionary<string, object?> CurrentValue,
    string? ExpectedDateUpdated);

/// <summary>
/// Result of <c>table.applyPaste</c>. <c>committed</c> counts are
/// server-confirmed (never client-optimistic). Mirrors
/// <c>backend.contracts.paste.ApplyPasteResult</c>.
/// </summary>
public sealed record ApplyPasteResult(
    string Collection,
    string Outcome,
    IReadOnlyList<object> CreatedRowKeys,
    IReadOnlyList<object> UpdatedRowKeys,
    IReadOnlyList<object> SkippedRowKeys,
    IReadOnlyList<ApplyPasteConflict> Conflicts,
    string RequestId);
