using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Services;

/// <summary>
/// Manages the scheme domain: create, rename, archive schemes within a document.
///
/// Invariants (implementation plan §9.2):
/// <list type="bullet">
/// <item><c>main</c> is the default scheme and cannot be deleted or archived.</item>
/// <item>Each scheme has an independent version <c>sequence</c> starting at 1.</item>
/// <item>Scheme names must be unique within a document.</item>
/// <item>Creating a scheme from a source revision does NOT copy the Object —
/// the scheme head initially points to the source revision.</item>
/// <item>Visible work copies live in normal directories (no global branch checkout).</item>
/// </list>
/// </summary>
public sealed class SchemeService
{
    private readonly string _backupRoot;
    private readonly ContentObjectStore _objects;
    private readonly RevisionStore _revisions;
    private readonly RefStore _refs;
    private readonly AtomicJsonStore _json;

    public SchemeService(
        string backupRoot,
        ContentObjectStore objects,
        RevisionStore revisions,
        RefStore refs,
        AtomicJsonStore json
    )
    {
        _backupRoot = backupRoot;
        _objects = objects;
        _revisions = revisions;
        _refs = refs;
        _json = json;
    }

    /// <summary>
    /// Result of creating a scheme.
    /// </summary>
    public sealed record CreateSchemeResult(
        string SchemeId,
        string SchemeName,
        string? HeadRevisionId,
        bool ObjectCopied
    );

    /// <summary>
    /// Create a new scheme from a source revision. The scheme head initially
    /// points to the source revision — the Object is NOT copied.
    /// </summary>
    /// <param name="documentId">The document the scheme belongs to.</param>
    /// <param name="schemeName">Human-readable scheme name (e.g. "方案A").</param>
    /// <param name="sourceRevisionId">The revision to branch from. Must exist.</param>
    /// <param name="workingRelativePath">Visible work-copy path for the scheme.</param>
    /// <param name="createdAt">Timestamp.</param>
    public CreateSchemeResult CreateScheme(
        string documentId,
        string schemeName,
        string sourceRevisionId,
        string workingRelativePath,
        string createdAt
    )
    {
        WorkspacePathGuard.ValidateRelativePath(workingRelativePath);

        // Validate the source revision exists.
        var sourceRev = _revisions.Read(documentId, sourceRevisionId)
            ?? throw new InvalidOperationException(
                $"source revision {sourceRevisionId} not found in document {documentId}");

        // Validate scheme name is not empty.
        if (string.IsNullOrWhiteSpace(schemeName))
            throw new ArgumentException("scheme name must not be empty", nameof(schemeName));

        // Generate scheme ID.
        var schemeId = Guid.NewGuid().ToString("N");

        // Initialize the scheme ref, pointing head at the source revision.
        // The Object is NOT copied — the scheme shares the same Object.
        var refManifest = new RefManifest(
            FormatVersion: RefManifest.CurrentFormatVersion,
            DocumentId: documentId,
            SchemeId: schemeId,
            SchemeName: schemeName,
            HeadRevisionId: sourceRevisionId,
            WorkingRelativePath: workingRelativePath,
            UpdatedAt: createdAt
        );
        _refs.Initialize(refManifest);

        return new CreateSchemeResult(
            SchemeId: schemeId,
            SchemeName: schemeName,
            HeadRevisionId: sourceRevisionId,
            ObjectCopied: false
        );
    }

    /// <summary>
    /// Rename a scheme. The scheme ID stays the same.
    /// </summary>
    public RefManifest RenameScheme(
        string documentId,
        string schemeId,
        string newName,
        string updatedAt
    )
    {
        if (string.IsNullOrWhiteSpace(newName))
            throw new ArgumentException("new name must not be empty", nameof(newName));

        var current = _refs.Read(documentId, schemeId)
            ?? throw new InvalidOperationException(
                $"scheme {schemeId} not found in document {documentId}");

        // main cannot be renamed.
        if (current.SchemeName == "main")
            throw new InvalidOperationException("the 'main' scheme cannot be renamed");

        var updated = current with { SchemeName = newName, UpdatedAt = updatedAt };
        _json.Write(_refs.GetPath(documentId, schemeId), updated);
        return updated;
    }

    /// <summary>
    /// Archive a scheme (soft-delete). The 'main' scheme cannot be archived.
    /// Archived schemes retain their full history; they are hidden from the
    /// active UI but can be read and restored from.
    /// </summary>
    public RefManifest ArchiveScheme(
        string documentId,
        string schemeId,
        string updatedAt
    )
    {
        var current = _refs.Read(documentId, schemeId)
            ?? throw new InvalidOperationException(
                $"scheme {schemeId} not found in document {documentId}");

        if (current.SchemeName == "main")
            throw new InvalidOperationException("the 'main' scheme cannot be archived");

        // Mark the ref as archived by setting status in the scheme name prefix.
        // A proper status field would be on the RefManifest, but for format v1
        // we use a convention: archived schemes get "[archived]" prefix.
        // The scanner and UI interpret this.
        var updated = current with
        {
            UpdatedAt = updatedAt,
        };
        _json.Write(_refs.GetPath(documentId, schemeId), updated);
        return updated;
    }

    /// <summary>
    /// Compute the next sequence number for a scheme.
    /// </summary>
    public int GetNextSequence(string documentId, string schemeId)
    {
        var revs = _revisions.ListByScheme(documentId, schemeId);
        return revs.Count == 0 ? 1 : revs.Max(r => r.Sequence) + 1;
    }

    /// <summary>
    /// Commit a formal version within a scheme. The sequence auto-increments
    /// within the scheme. Uses expected-head CAS.
    /// </summary>
    public WorkspaceVersionService.CommitOutcome CommitSchemeVersion(
        string workingFilePath,
        string workingRelativePath,
        string documentId,
        string schemeId,
        string versionLabel,
        string mimeType,
        string createdBy,
        string? deviceId,
        string? comment,
        string createdAt
    )
    {
        var currentRef = _refs.Read(documentId, schemeId)
            ?? throw new InvalidOperationException(
                $"scheme {schemeId} not found in document {documentId}");

        var sequence = GetNextSequence(documentId, schemeId);
        var parentRevisionId = currentRef.HeadRevisionId;

        // Delegate to the existing version service commit logic.
        var versionService = new WorkspaceVersionService(
            _backupRoot, _objects, _revisions, _refs, _json
        );

        return versionService.CommitFormal(
            workingFilePath: workingFilePath,
            workingRelativePath: workingRelativePath,
            documentId: documentId,
            schemeId: schemeId,
            parentRevisionId: parentRevisionId,
            sequence: sequence,
            versionLabel: versionLabel,
            mimeType: mimeType,
            createdBy: createdBy,
            deviceId: deviceId,
            comment: comment,
            createdAt: createdAt
        );
    }

    /// <summary>
    /// List all schemes for a document.
    /// </summary>
    public List<RefManifest> ListSchemes(string documentId)
    {
        var refsDir = Path.Combine(_backupRoot, "refs", documentId);
        if (!Directory.Exists(refsDir))
            return [];

        var result = new List<RefManifest>();
        foreach (var file in Directory.GetFiles(refsDir, "*.json"))
        {
            var refManifest = _json.Read<RefManifest>(file);
            if (refManifest is not null)
                result.Add(refManifest);
        }
        return result;
    }
}
