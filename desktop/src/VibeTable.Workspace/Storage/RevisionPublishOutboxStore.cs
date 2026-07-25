using VibeTable.Workspace.Domain;

namespace VibeTable.Workspace.Storage;

/// <summary>
/// Durable metadata-only outbox for revision index publication.
/// Document bytes and object paths never enter this store.
/// </summary>
public sealed class RevisionPublishOutboxStore
{
    private readonly string _root;
    private readonly string _issuesRoot;
    private readonly AtomicJsonStore _json;

    public RevisionPublishOutboxStore(string backupRoot, AtomicJsonStore json)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(backupRoot);
        _json = json ?? throw new ArgumentNullException(nameof(json));
        _root = Path.Combine(backupRoot, "outbox", "revisions");
        _issuesRoot = Path.Combine(backupRoot, "outbox", "revision-issues");
    }

    public void Enqueue(RevisionManifest revision)
    {
        ArgumentNullException.ThrowIfNull(revision);
        if (revision.FormatVersion != RevisionManifest.CurrentFormatVersion)
            throw new InvalidOperationException("unsupported revision manifest format");
        _json.Write(GetPath(revision.DocumentId, revision.RevisionId), revision);
    }

    public IReadOnlyList<RevisionManifest> ListByDocument(string documentId)
    {
        string directory = Path.Combine(
            _root,
            DocumentCatalogStore.ValidateIdentifier(documentId, nameof(documentId)));
        if (!Directory.Exists(directory))
            return [];

        return Directory.GetFiles(directory, "*.json")
            .Select(path => ReadPending(path, documentId))
            .OrderBy(revision => revision.Sequence)
            .ThenBy(revision => revision.RevisionId, StringComparer.Ordinal)
            .ToArray();
    }

    public void Complete(string documentId, string revisionId)
    {
        string path = GetPath(documentId, revisionId);
        if (File.Exists(path))
            File.Delete(path);
    }

    public void MarkConflicted(
        RevisionManifest revision,
        string errorCode,
        string message,
        string updatedAt)
    {
        ArgumentNullException.ThrowIfNull(revision);
        updatedAt = UtcRfc3339Timestamp.Canonicalize(
            updatedAt,
            nameof(updatedAt));
        var issue = new RevisionPublishIssue(
            RevisionPublishIssue.CurrentFormatVersion,
            revision,
            "conflicted",
            errorCode,
            message,
            updatedAt);
        _json.Write(GetIssuePath(revision.DocumentId, revision.RevisionId), issue);
        Complete(revision.DocumentId, revision.RevisionId);
    }

    public IReadOnlyList<RevisionPublishIssue> ListConflictedByDocument(
        string documentId)
    {
        string directory = Path.Combine(
            _issuesRoot,
            DocumentCatalogStore.ValidateIdentifier(documentId, nameof(documentId)));
        if (!Directory.Exists(directory))
            return [];

        return Directory.GetFiles(directory, "*.json")
            .Select(path => ReadIssue(path, documentId))
            .OrderBy(issue => issue.Revision.Sequence)
            .ThenBy(issue => issue.Revision.RevisionId, StringComparer.Ordinal)
            .ToArray();
    }

    public string GetPath(string documentId, string revisionId)
        => Path.Combine(
            _root,
            DocumentCatalogStore.ValidateIdentifier(documentId, nameof(documentId)),
            DocumentCatalogStore.ValidateIdentifier(revisionId, nameof(revisionId)) + ".json");

    private string GetIssuePath(string documentId, string revisionId)
        => Path.Combine(
            _issuesRoot,
            DocumentCatalogStore.ValidateIdentifier(documentId, nameof(documentId)),
            DocumentCatalogStore.ValidateIdentifier(revisionId, nameof(revisionId)) + ".json");

    private RevisionManifest ReadPending(string path, string documentId)
    {
        var revision = _json.Read<RevisionManifest>(path);
        if (revision is null
            || revision.FormatVersion != RevisionManifest.CurrentFormatVersion
            || !string.Equals(
                Path.GetFileNameWithoutExtension(path),
                revision.RevisionId,
                StringComparison.Ordinal)
            || !string.Equals(
                revision.DocumentId,
                documentId,
                StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                $"invalid revision publish outbox entry: {Path.GetFileName(path)}");
        }
        return revision;
    }

    private RevisionPublishIssue ReadIssue(string path, string documentId)
    {
        var issue = _json.Read<RevisionPublishIssue>(path);
        if (issue is null
            || issue.FormatVersion != RevisionPublishIssue.CurrentFormatVersion
            || !string.Equals(issue.Status, "conflicted", StringComparison.Ordinal)
            || issue.Revision is null
            || !string.Equals(
                Path.GetFileNameWithoutExtension(path),
                issue.Revision.RevisionId,
                StringComparison.Ordinal)
            || !string.Equals(
                issue.Revision.DocumentId,
                documentId,
                StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                $"invalid revision publish issue: {Path.GetFileName(path)}");
        }
        return issue;
    }
}

public sealed record RevisionPublishIssue(
    int FormatVersion,
    RevisionManifest Revision,
    string Status,
    string ErrorCode,
    string Message,
    string UpdatedAt)
{
    public const int CurrentFormatVersion = 1;
}
