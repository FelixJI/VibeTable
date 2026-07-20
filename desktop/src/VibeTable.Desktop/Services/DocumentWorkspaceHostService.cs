using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Joins Directus document metadata with the machine-local workspace mount.
/// It returns opaque handles only; absolute paths remain inside the host.
/// </summary>
public sealed class DocumentWorkspaceHostService : IDisposable
{
    private static readonly HashSet<string> WebPreviewExtensions = new(
        StringComparer.OrdinalIgnoreCase)
    {
        ".bmp", ".csv", ".gif", ".jpeg", ".jpg", ".json", ".md",
        ".pdf", ".png", ".txt", ".webp",
    };

    private static readonly HashSet<string> OfficePreviewExtensions = new(
        StringComparer.OrdinalIgnoreCase)
    {
        ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
    };

    private static readonly HashSet<string> UnsafeOpenExtensions = new(
        StringComparer.OrdinalIgnoreCase)
    {
        ".bat", ".cmd", ".com", ".exe", ".lnk", ".msi", ".ps1", ".scr", ".url",
    };

    private readonly IDocumentWorkspaceRpcGateway _gateway;
    private readonly WorkspaceMountStore _mounts;
    private readonly DocumentCapabilityStore _capabilities;
    private readonly ILocalDocumentActions _actions;
    private readonly ILocalDocumentPreview _preview;

    public DocumentWorkspaceHostService(
        IDocumentWorkspaceRpcGateway gateway,
        WorkspaceMountStore mounts,
        DocumentCapabilityStore capabilities,
        ILocalDocumentActions actions,
        ILocalDocumentPreview? preview = null)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _mounts = mounts ?? throw new ArgumentNullException(nameof(mounts));
        _capabilities = capabilities ?? throw new ArgumentNullException(nameof(capabilities));
        _actions = actions ?? throw new ArgumentNullException(nameof(actions));
        _preview = preview ?? new ShellDocumentPreview();
    }

    public async Task<DocumentListPayload> ListAsync(
        string collection,
        string itemId,
        CancellationToken token)
    {
        var result = await _gateway.ReadFolderAsync(collection, itemId, token)
            .ConfigureAwait(false);
        var entries = new List<DocumentEntryPayload>(result.Documents.Count);

        foreach (var document in result.Documents)
        {
            entries.Add(BuildEntry(document));
        }

        return new DocumentListPayload(collection, itemId, entries);
    }

    public async Task<DocumentListPayload> ListGlobalAsync(CancellationToken token)
    {
        var result = await _gateway.ReadDocumentsAsync(500, 0, token)
            .ConfigureAwait(false);
        var entries = new List<DocumentEntryPayload>(result.Documents.Count);
        foreach (var document in result.Documents)
        {
            entries.Add(BuildEntry(document));
        }
        return new DocumentListPayload(null, null, entries);
    }

    public async Task<DocumentHistoryPayload> ReadHistoryAsync(
        string entryHandle,
        int limit,
        int offset,
        CancellationToken token)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "history");
        var result = await _gateway.ReadHistoryAsync(
            descriptor.DocumentId,
            Math.Clamp(limit, 1, 100),
            Math.Max(0, offset),
            token).ConfigureAwait(false);
        var revisions = result.Revisions.Select(revision => new DocumentRevisionPayload(
            _capabilities.IssueRevision(
                descriptor.WorkspaceId,
                descriptor.DocumentId,
                revision.RevisionId,
                ["preview"]),
            string.IsNullOrWhiteSpace(revision.VersionLabel)
                ? $"v{revision.Sequence}"
                : revision.VersionLabel,
            revision.CreatedAt,
            revision.Size,
            revision.CreatedBy)).ToArray();
        return new DocumentHistoryPayload(entryHandle, revisions, result.Total);
    }

    public void Open(string entryHandle)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "open");
        _actions.Open(ResolveExistingPath(descriptor));
    }

    public void Reveal(string entryHandle)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "reveal");
        _actions.Reveal(ResolveExistingPath(descriptor));
    }

    public void Preview(string entryHandle)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "preview");
        _preview.Show(ResolveExistingPath(descriptor));
    }

    public async Task UnlinkAsync(string entryHandle, CancellationToken token)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "unlink");
        if (string.IsNullOrWhiteSpace(descriptor.LinkId))
            throw new DocumentCapabilityException(
                "此全局文档没有可解除的记录关联。",
                "DOCUMENT_LINK_UNAVAILABLE");
        await _gateway.UnlinkAsync(descriptor.LinkId, token).ConfigureAwait(false);
    }

    public void RevokeAll() => _capabilities.RevokeAll();

    /// <summary>
    /// Starts a new renderer/authentication capability generation. Handles
    /// from every earlier generation become invalid immediately.
    /// </summary>
    public long RotateCapabilityEpoch() => _capabilities.RotateEpoch();

    public void Dispose()
    {
        RevokeAll();
        _preview.Dispose();
    }

    private DocumentEntryPayload BuildEntry(DocumentSummary document)
    {
        string? root = _mounts.ResolveRoot(document.WorkspaceId);
        if (string.IsNullOrWhiteSpace(root))
            return BuildUnavailableEntry(document, "unmounted", string.Empty);

        DocumentManifest localDocument;
        string relativePath;
        try
        {
            var catalog = new DocumentCatalogStore(
                Path.Combine(root, ".backup"), new AtomicJsonStore());
            localDocument = catalog.ReadDocument(document.DocumentId)
                ?? throw new DocumentCatalogMissingException();
            if (localDocument.FormatVersion != 1
                || !string.Equals(
                    localDocument.DocumentId, document.DocumentId, StringComparison.Ordinal)
                || !string.Equals(
                    localDocument.WorkspaceId, document.WorkspaceId, StringComparison.Ordinal)
                || !string.Equals(localDocument.Status, "active", StringComparison.OrdinalIgnoreCase))
            {
                return BuildUnavailableEntry(document, "unsafe", string.Empty);
            }
            relativePath = catalog.ResolveWorkingRelativePath(localDocument);
        }
        catch (DocumentCatalogMissingException)
        {
            return BuildUnavailableEntry(document, "unmanaged", string.Empty);
        }
        catch (Exception)
        {
            return BuildUnavailableEntry(document, "unsafe", string.Empty);
        }

        string fullPath;
        try
        {
            fullPath = WorkspacePathGuard.ResolveAndCheck(root, relativePath);
        }
        catch (Exception)
        {
            return BuildUnavailableEntry(document, "unsafe", relativePath);
        }

        bool exists = File.Exists(fullPath);
        var capabilities = new List<string> { "history" };
        if (!string.IsNullOrWhiteSpace(document.LinkId)) capabilities.Add("unlink");
        if (exists)
        {
            if (!UnsafeOpenExtensions.Contains(Path.GetExtension(localDocument.FileName)))
                capabilities.Add("open");
            if (_preview.CanPreview(fullPath))
                capabilities.Add("preview");
            capabilities.Add("reveal");
        }
        else
        {
            capabilities.Add("relocate");
        }

        string handle = _capabilities.Issue(
            document.WorkspaceId,
            document.DocumentId,
            document.LinkId,
            relativePath,
            capabilities);
        return new DocumentEntryPayload(
            handle,
            document.DocumentId,
            localDocument.FileName,
            localDocument.MimeType,
            exists ? "available" : "missing",
            capabilities.Contains("preview") ? "system" : "none",
            document.MainHead,
            document.LinkType ?? "primary",
            capabilities);
    }

    private DocumentEntryPayload BuildUnavailableEntry(
        DocumentSummary document,
        string availability,
        string relativePath)
    {
        var capabilities = new List<string> { "history" };
        if (!string.IsNullOrWhiteSpace(document.LinkId)) capabilities.Add("unlink");
        if (availability is "missing" or "unmounted" or "unmanaged")
            capabilities.Add("relocate");

        string handle = _capabilities.Issue(
            document.WorkspaceId,
            document.DocumentId,
            document.LinkId,
            string.IsNullOrWhiteSpace(relativePath) ? document.FileName : relativePath,
            capabilities);
        return new DocumentEntryPayload(
            handle,
            document.DocumentId,
            document.FileName,
            document.MimeType,
            availability,
            "none",
            document.MainHead,
            document.LinkType ?? "primary",
            capabilities);
    }

    private string ResolveExistingPath(DocumentCapabilityDescriptor descriptor)
    {
        string? root = _mounts.ResolveRoot(descriptor.WorkspaceId);
        if (string.IsNullOrWhiteSpace(root))
            throw new DocumentCapabilityException(
                "此工作区尚未挂载到本机。",
                "WORKSPACE_UNMOUNTED");
        string fullPath = WorkspacePathGuard.ResolveAndCheck(root, descriptor.RelativePath);
        if (!File.Exists(fullPath))
            throw new DocumentCapabilityException(
                "文件已移动或删除，请重新定位。",
                "DOCUMENT_MISSING");
        return fullPath;
    }

    private sealed class DocumentCatalogMissingException : Exception;
}

public sealed record DocumentListPayload(
    string? Collection,
    string? ItemId,
    IReadOnlyList<DocumentEntryPayload> Entries);

public sealed record DocumentEntryPayload(
    string EntryHandle,
    string DocumentId,
    string DisplayName,
    string? MimeType,
    string Availability,
    string PreviewKind,
    string? CurrentRevision,
    string LinkType,
    IReadOnlyList<string> Capabilities);

public sealed record DocumentHistoryPayload(
    string EntryHandle,
    IReadOnlyList<DocumentRevisionPayload> Revisions,
    int Total);

public sealed record DocumentRevisionPayload(
    string RevisionHandle,
    string Label,
    string CreatedAt,
    long Size,
    string? Author);
