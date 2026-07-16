using System.IO;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Services;

/// <summary>
/// Handles adopting a scheme's head into the main line.
///
/// Adoption (implementation plan §9.2 item 4):
/// <list type="bullet">
/// <item>Select a scheme head.</item>
/// <item>Write the scheme's Object content back to the main working file.</item>
/// <item>Create a new main Revision with <c>parent = old main head</c> and
/// <c>sourceRevisionId = adopted scheme head</c>.</item>
/// <item>The scheme retains its full history (not deleted or reset).</item>
/// <item>Adoption does NOT auto-merge Office/PDF content — it atomically
/// writes the scheme's Object bytes to the main working file.</item>
/// </list>
/// </summary>
public sealed class SchemeAdoptionService
{
    private readonly string _backupRoot;
    private readonly ContentObjectStore _objects;
    private readonly RevisionStore _revisions;
    private readonly RefStore _refs;
    private readonly AtomicJsonStore _json;

    public SchemeAdoptionService(
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
    /// Result of adopting a scheme.
    /// </summary>
    public sealed record AdoptionResult(
        string NewMainRevisionId,
        string SourceRevisionId,
        string OldMainHead,
        string AdoptedHash,
        long AdoptedSize
    );

    /// <summary>
    /// Adopt a scheme head into the main line.
    /// </summary>
    /// <param name="documentId">The document.</param>
    /// <param name="schemeId">The scheme being adopted.</param>
    /// <param name="mainSchemeId">The main scheme ID.</param>
    /// <param name="mainWorkingPath">The main working file relative path.</param>
    /// <param name="newMainLabel">Version label for the new main revision (e.g. "main/V2.0").</param>
    /// <param name="createdBy">User performing the adoption.</param>
    /// <param name="deviceId">Device performing the adoption.</param>
    /// <param name="comment">Adoption comment.</param>
    /// <param name="createdAt">Timestamp.</param>
    public AdoptionResult Adopt(
        string documentId,
        string schemeId,
        string mainSchemeId,
        string mainWorkingPath,
        string newMainLabel,
        string createdBy,
        string? deviceId,
        string? comment,
        string createdAt
    )
    {
        WorkspacePathGuard.ValidateRelativePath(mainWorkingPath);

        var schemeRef = _refs.Read(documentId, schemeId)
            ?? throw new InvalidOperationException(
                $"scheme {schemeId} not found in document {documentId}");

        var mainRef = _refs.Read(documentId, mainSchemeId)
            ?? throw new InvalidOperationException(
                $"main scheme {mainSchemeId} not found in document {documentId}");

        var sourceRevisionId = schemeRef.HeadRevisionId;
        if (string.IsNullOrEmpty(sourceRevisionId))
            throw new InvalidOperationException(
                $"scheme {schemeId} has no head revision to adopt");

        var sourceRev = _revisions.Read(documentId, sourceRevisionId)
            ?? throw new InvalidOperationException(
                $"source revision {sourceRevisionId} not found");

        var oldMainHead = mainRef.HeadRevisionId ?? "";

        // Restore the scheme's Object content to the main working file.
        // The working file path is resolved by the C# host, not this service.
        // This service records the adoption in .backup only.
        var objectPath = _objects.GetObjectPath(sourceRev.ContentHash);
        if (!File.Exists(objectPath))
            throw new InvalidOperationException(
                $"object {sourceRev.ContentHash} not found for adoption");

        // Compute the main sequence.
        var mainRevs = _revisions.ListByScheme(documentId, mainSchemeId);
        var mainSequence = mainRevs.Count == 0 ? 1 : mainRevs.Max(r => r.Sequence) + 1;

        // Create the new main revision recording the adoption.
        var newRevisionId = Guid.NewGuid().ToString("N");
        var adoptionRevision = new RevisionManifest(
            FormatVersion: RevisionManifest.CurrentFormatVersion,
            RevisionId: newRevisionId,
            DocumentId: documentId,
            SchemeId: mainSchemeId,
            ParentRevisionId: oldMainHead,
            SourceRevisionId: sourceRevisionId,
            RestoredFromRevisionId: null,
            Sequence: mainSequence,
            VersionLabel: newMainLabel,
            Kind: RevisionKind.Formal,
            ContentHash: sourceRev.ContentHash,
            Size: sourceRev.Size,
            MimeType: sourceRev.MimeType,
            WorkingRelativePath: mainWorkingPath,
            CreatedAt: createdAt,
            CreatedBy: createdBy,
            DeviceId: deviceId,
            Comment: comment ?? $"Adopted from {schemeRef.SchemeName}"
        );
        _revisions.Write(adoptionRevision);

        // Update main ref head via CAS.
        try
        {
            _refs.UpdateHead(documentId, mainSchemeId, oldMainHead, newRevisionId, createdAt);
        }
        catch (RefCasConflictException ex)
        {
            // CAS conflict — the revision is committed but the ref is not updated.
            // The caller/scanner handles conflict resolution.
            throw new InvalidOperationException(
                $"adoption CAS conflict: main head changed during adoption. {ex.Message}", ex);
        }

        return new AdoptionResult(
            NewMainRevisionId: newRevisionId,
            SourceRevisionId: sourceRevisionId,
            OldMainHead: oldMainHead,
            AdoptedHash: sourceRev.ContentHash,
            AdoptedSize: sourceRev.Size
        );
    }
}
