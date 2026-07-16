namespace VibeTable.Workspace.Domain;

/// <summary>
/// Root manifest for a workspace, stored at <c>.backup/workspace.json</c>.
/// The workspace UUID is locally authoritative; Directus only mirrors it.
/// </summary>
public sealed record WorkspaceManifest(
    int FormatVersion,
    string WorkspaceId,
    string Name,
    string CreatedAt
)
{
    public const int CurrentFormatVersion = 1;
}

/// <summary>
/// Index entry for a physical folder with a stable UUID.
/// The path can change (rename/move); the UUID does not.
/// </summary>
public sealed record FolderManifest(
    int FormatVersion,
    string FolderId,
    string WorkspaceId,
    string? ParentFolderId,
    string RelativePath,
    string Status,
    string UpdatedAt
);

/// <summary>
/// A logical document with a stable UUID. Does not change on rename or version bump.
/// </summary>
public sealed record DocumentManifest(
    int FormatVersion,
    string DocumentId,
    string WorkspaceId,
    string? FolderId,
    string FileName,
    string MimeType,
    string Status,
    string UpdatedAt
);

/// <summary>
/// Reference to a scheme (e.g. <c>main</c>, <c>方案A</c>, <c>方案B</c>).
/// Schemes are independent version chains within a document.
/// </summary>
public sealed record SchemeRef(
    string SchemeId,
    string DocumentId,
    string Name,
    string? SourceRevisionId,
    string? HeadRevisionId,
    string WorkingRelativePath,
    string Status,
    string UpdatedAt
);

/// <summary>
/// Kind of revision: snapshot (auto, rolling retention), formal (user-confirmed
/// version bump), or restore (produces a new formal revision from history).
/// </summary>
public enum RevisionKind
{
    Snapshot,
    Formal,
    Restore,
}

/// <summary>
/// An immutable content commit within a scheme. Written once, never modified.
/// </summary>
public sealed record RevisionManifest(
    int FormatVersion,
    string RevisionId,
    string DocumentId,
    string SchemeId,
    string? ParentRevisionId,
    string? SourceRevisionId,
    string? RestoredFromRevisionId,
    int Sequence,
    string VersionLabel,
    RevisionKind Kind,
    string ContentHash,
    long Size,
    string MimeType,
    string WorkingRelativePath,
    string CreatedAt,
    string? CreatedBy,
    string? DeviceId,
    string? Comment
)
{
    public const int CurrentFormatVersion = 1;
}

/// <summary>
/// A scheme's current head pointer and its visible work-copy path.
/// One of the few mutable files — updated via temp-file + atomic rename.
/// </summary>
public sealed record RefManifest(
    int FormatVersion,
    string DocumentId,
    string SchemeId,
    string SchemeName,
    string HeadRevisionId,
    string WorkingRelativePath,
    string UpdatedAt
)
{
    public const int CurrentFormatVersion = 1;
}
