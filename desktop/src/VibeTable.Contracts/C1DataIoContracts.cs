using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// C1 task + path-grant + data-IO contracts. Mirrors
/// <c>backend.contracts.task</c>, <c>backend.contracts.data_io</c> and
/// <c>backend.contracts.relation</c>. The C# records deserialize the exact
/// camelCase bytes the Python Pydantic service emits (cross-language pin).
/// </summary>

// --- Task runtime ---

public static class TaskStates
{
    public const string Queued = "queued";
    public const string Running = "running";
    public const string Succeeded = "succeeded";
    public const string Failed = "failed";
    public const string Cancelled = "cancelled";
    public const string Aborted = "aborted";
}

public sealed record TaskProgress(int Done, int Total, string Message);

public sealed record TaskStatus(
    string TaskId,
    string Kind,
    string State,
    TaskProgress Progress,
    object? Result,
    string? Error);

public sealed record CreateTaskParams(string Kind, IReadOnlyDictionary<string, object?> Params);

public sealed record TaskIdParams(string TaskId);

// --- Session path grants ---

public sealed record SessionPathGrant(
    string GrantId,
    string Purpose,
    string Direction,
    string DisplayName,
    long? SizeBytes,
    string? MimeType,
    double ExpiresAt);

public sealed record RequestImportSourceGrantParams(IReadOnlyList<string> Accept);

public sealed record RequestExportTargetGrantParams(string DefaultName, string Format);

public sealed record ResolveGrantParams(string GrantId);

// --- Import ---

public static class ImportModes
{
    public const string CreateOnly = "create_only";
    public const string Upsert = "upsert";
}

public sealed record ImportColumnMapping(string SourceColumn, string TargetField, bool RelationLookup);

public sealed record PreviewImportParams(
    string GrantId,
    string Collection,
    string SchemaRevision,
    string Mode,
    string? UpsertKey,
    IReadOnlyList<ImportColumnMapping> ColumnMapping);

public sealed record ImportCellDiagnostic(
    string Sheet,
    int Row,
    int Column,
    string Severity,
    string Code,
    string Message,
    string OriginalValue);

public sealed record ImportPlanRow(
    int SourceRow,
    IReadOnlyDictionary<string, object?> Values,
    IReadOnlyList<ImportCellDiagnostic> Diagnostics);

public sealed record ImportSummary(
    int TotalRows,
    int ValidRows,
    int ErrorRows,
    int WarningRows,
    int ErrorCount,
    int WarningCount);

public sealed record ImportPreviewToken(string Token, double ExpiresAt, bool Consumed);

public sealed record ImportPlan(
    string Collection,
    string SchemaRevision,
    string CapabilityHash,
    string SourceHash,
    ImportSummary Summary,
    IReadOnlyList<ImportPlanRow> Rows,
    IReadOnlyList<string> UnmatchedColumns,
    IReadOnlyList<ImportCellDiagnostic> Diagnostics,
    ImportPreviewToken Token);

public sealed record ApplyImportParams(
    string GrantId,
    string Collection,
    string Token,
    string Mode,
    string IdempotencyPrefix);

public sealed record ImportChunkResult(
    int ChunkIndex,
    IReadOnlyList<string> CreatedRowKeys,
    IReadOnlyList<string> UpdatedRowKeys,
    IReadOnlyList<int> FailedRows,
    string IdempotencyKey);

public sealed record ApplyImportResult(
    string Collection,
    int CreatedCount,
    int UpdatedCount,
    IReadOnlyList<int> FailedRows,
    IReadOnlyList<ImportChunkResult> Chunks,
    IReadOnlyList<string> RequestIds);

// --- Export ---

public static class ExportFormats
{
    public const string Csv = "csv";
    public const string Xlsx = "xlsx";
}

public sealed record ExportParams(
    string GrantId,
    string Collection,
    IReadOnlyDictionary<string, object?> Query,
    string Format,
    bool IncludeRelations);

public sealed record ExportResult(
    string Collection,
    string Format,
    int RowsWritten,
    string SchemaRevision,
    string CapabilityHash,
    string OutputDisplayName);

public sealed record GenerateTemplateParams(string Collection, string GrantId, string Format);

public sealed record TemplateResult(string Collection, string GrantId, string DisplayName);
