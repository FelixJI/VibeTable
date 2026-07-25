using System.Text.RegularExpressions;
using VibeTable.Workspace.Domain;

namespace VibeTable.Workspace.Storage;

/// <summary>
/// Locally authoritative document/folder catalog stored under a workspace's
/// hidden <c>.backup</c> directory. Metadata projections are never used to
/// authorize a local file path.
/// </summary>
public sealed partial class DocumentCatalogStore
{
    private readonly string _documentsRoot;
    private readonly string _foldersRoot;
    private readonly AtomicJsonStore _json;

    public DocumentCatalogStore(string backupRoot, AtomicJsonStore json)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(backupRoot);
        _json = json ?? throw new ArgumentNullException(nameof(json));
        _documentsRoot = Path.Combine(backupRoot, "documents");
        _foldersRoot = Path.Combine(backupRoot, "folders");
    }

    public DocumentManifest? ReadDocument(string documentId)
        => _json.Read<DocumentManifest>(GetDocumentPath(documentId));

    public FolderManifest? ReadFolder(string folderId)
        => _json.Read<FolderManifest>(GetFolderPath(folderId));

    public void WriteDocument(DocumentManifest manifest)
    {
        ArgumentNullException.ThrowIfNull(manifest);
        ValidateId(manifest.DocumentId, nameof(manifest.DocumentId));
        ValidateId(manifest.WorkspaceId, nameof(manifest.WorkspaceId));
        if (manifest.FolderId is not null)
            ValidateId(manifest.FolderId, nameof(manifest.FolderId));
        ValidateFileName(manifest.FileName);
        if (manifest.FormatVersion != 1)
            throw new InvalidOperationException("unsupported document manifest format");
        _json.Write(GetDocumentPath(manifest.DocumentId), manifest);
    }

    public void WriteFolder(FolderManifest manifest)
    {
        ArgumentNullException.ThrowIfNull(manifest);
        ValidateId(manifest.FolderId, nameof(manifest.FolderId));
        ValidateId(manifest.WorkspaceId, nameof(manifest.WorkspaceId));
        if (manifest.ParentFolderId is not null)
            ValidateId(manifest.ParentFolderId, nameof(manifest.ParentFolderId));
        ValidateWorkingRelativePath(manifest.RelativePath, allowEmpty: true);
        if (manifest.FormatVersion != 1)
            throw new InvalidOperationException("unsupported folder manifest format");
        _json.Write(GetFolderPath(manifest.FolderId), manifest);
    }

    public string ResolveWorkingRelativePath(DocumentManifest document)
    {
        ArgumentNullException.ThrowIfNull(document);
        ValidateFileName(document.FileName);
        string folderPath = string.Empty;
        if (document.FolderId is not null)
        {
            var folder = ReadFolder(document.FolderId)
                ?? throw new InvalidOperationException("document folder manifest is missing");
            if (!string.Equals(folder.WorkspaceId, document.WorkspaceId, StringComparison.Ordinal)
                || !string.Equals(folder.Status, "active", StringComparison.OrdinalIgnoreCase))
            {
                throw new InvalidOperationException("document folder manifest is not active");
            }
            folderPath = folder.RelativePath;
        }

        string relativePath = string.IsNullOrWhiteSpace(folderPath)
            ? document.FileName
            : $"{folderPath.Replace('\\', '/').TrimEnd('/')}/{document.FileName}";
        return ValidateWorkingRelativePath(relativePath, allowEmpty: false);
    }

    public string GetDocumentPath(string documentId)
        => Path.Combine(_documentsRoot, SafeId(documentId, nameof(documentId)) + ".json");

    public string GetFolderPath(string folderId)
        => Path.Combine(_foldersRoot, SafeId(folderId, nameof(folderId)) + ".json");

    private static string ValidateWorkingRelativePath(string path, bool allowEmpty)
    {
        if (allowEmpty && string.IsNullOrWhiteSpace(path))
            return string.Empty;
        string validated = WorkspacePathGuard.ValidateRelativePath(path);
        foreach (string component in validated.Split(
            ['/', '\\'], StringSplitOptions.RemoveEmptyEntries))
        {
            if (WorkspacePathGuard.ShouldIgnore(component))
                throw new InvalidOperationException("working path uses a reserved component");
        }
        return validated;
    }

    private static void ValidateFileName(string fileName)
    {
        string validated = ValidateWorkingRelativePath(fileName, allowEmpty: false);
        if (validated.Contains('/') || validated.Contains('\\'))
            throw new InvalidOperationException("document file name must be a single component");
    }

    private static string SafeId(string value, string paramName)
    {
        ValidateId(value, paramName);
        return value;
    }

    internal static string ValidateIdentifier(string value, string paramName)
    {
        ValidateId(value, paramName);
        return value;
    }

    private static void ValidateId(string value, string paramName)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(value, paramName);
        if (!SafeIdentifier().IsMatch(value))
            throw new ArgumentException("identifier contains unsafe characters", paramName);
    }

    [GeneratedRegex("^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$", RegexOptions.CultureInvariant)]
    private static partial Regex SafeIdentifier();
}
