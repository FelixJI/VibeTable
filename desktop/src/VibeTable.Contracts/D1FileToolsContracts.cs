using System.Collections.Generic;

namespace VibeTable.Contracts;

/// <summary>
/// D1 file-tools contracts. Mirrors <c>backend.contracts.file_tools</c>.
/// </summary>

// --- Directus Files ---

public sealed record DirectusFileMetadata(
    string Id,
    string Filename,
    string MimeType,
    long FileSize,
    string UploadedOn,
    string Storage,
    string? Checksum);

public sealed record ReadFilesParams(string Collection, string ItemId, string RelationField);

public sealed record FilesResult(string Collection, string ItemId, IReadOnlyList<DirectusFileMetadata> Files);

public sealed record UploadFileParams(string GrantId, string Collection, string ItemId, string RelationField);

public sealed record UnlinkFileParams(string Collection, string ItemId, string RelationField, string FileId);

public sealed record DeleteFileParams(string FileId);

public sealed record PresetPreviewParams(string FileId, string PresetKey);

public sealed record PresetPreviewResult(string FileId, string PresetKey, string Url, bool Cached);

// --- Operation journal ---

public static class JournalStates
{
    public const string Planned = "planned";
    public const string Running = "running";
    public const string Committed = "committed";
    public const string RollbackRequired = "rollback-required";
    public const string RolledBack = "rolled-back";
    public const string Failed = "failed";
}

public sealed record JournalStep(string Kind, string Source, string Target, string? BackupPath, string? BackupHash);

public sealed record JournalEntry(
    string JournalId,
    string Operation,
    string State,
    IReadOnlyList<JournalStep> Steps,
    string CreatedAt,
    string? Error);

public sealed record ListJournalParams;

public sealed record JournalResult(IReadOnlyList<JournalEntry> Pending);

public sealed record JournalIdParams(string JournalId);

public sealed record ResolveJournalParams(string JournalId, string Action);
