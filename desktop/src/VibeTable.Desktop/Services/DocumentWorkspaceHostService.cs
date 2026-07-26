using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Services;
using VibeTable.Workspace.Storage;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Joins provider-neutral document metadata with the machine-local workspace mount.
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

    private const int MaximumNameAttempts = 10_000;
    private const int MaximumRegistrationRetriesPerRefresh = 20;
    private const long MaximumImportBytes = 2L * 1024 * 1024 * 1024;
    private const long MinimumFreeSpaceReserveBytes = 256L * 1024 * 1024;

    private static readonly HashSet<string> DangerousFileExtensions = new(
        StringComparer.OrdinalIgnoreCase)
    {
        ".appref-ms", ".application", ".bat", ".chm", ".cmd", ".com", ".cpl",
        ".dll", ".docm", ".dotm", ".exe", ".gadget", ".hta", ".inf", ".iso",
        ".jar", ".js", ".jse", ".lnk", ".msc", ".msh", ".msi", ".msp", ".mst",
        ".pif", ".potm", ".ppam", ".pptm", ".ps1", ".reg", ".scf", ".scr",
        ".sct", ".shb", ".shs", ".sldm", ".url", ".vb", ".vbe", ".vbs",
        ".vxd", ".website", ".ws", ".wsc", ".wsf", ".wsh", ".xll", ".xlsm",
        ".xltm",
    };

    private static readonly IReadOnlyDictionary<string, string> MimeTypes =
        new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
        {
            [".bmp"] = "image/bmp",
            [".csv"] = "text/csv",
            [".doc"] = "application/msword",
            [".docx"] = "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            [".gif"] = "image/gif",
            [".jpeg"] = "image/jpeg",
            [".jpg"] = "image/jpeg",
            [".json"] = "application/json",
            [".md"] = "text/markdown",
            [".pdf"] = "application/pdf",
            [".png"] = "image/png",
            [".ppt"] = "application/vnd.ms-powerpoint",
            [".pptx"] = "application/vnd.openxmlformats-officedocument.presentationml.presentation",
            [".txt"] = "text/plain",
            [".webp"] = "image/webp",
            [".xls"] = "application/vnd.ms-excel",
            [".xlsx"] = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        };

    private readonly IDocumentWorkspaceRpcGateway _gateway;
    private readonly WorkspaceMountStore _mounts;
    private readonly DocumentCapabilityStore _capabilities;
    private readonly ILocalDocumentActions _actions;
    private readonly ILocalDocumentPreview _preview;
    private readonly ILocalDocumentFilePicker? _filePicker;
    private readonly string? _partitionKey;
    private readonly Func<string?>? _partitionKeyProvider;
    private readonly SemaphoreSlim _fileMutationGate = new(1, 1);
    private readonly SemaphoreSlim _publishGate = new(1, 1);
    private readonly object _restoreReconciliationGate = new();
    private readonly HashSet<string> _reconciledWorkspaceRoots =
        new(StringComparer.OrdinalIgnoreCase);
    private readonly string _revisionPreviewRoot = Path.Combine(
        Path.GetTempPath(),
        "VibeTable",
        "document-revision-preview",
        $"p{Environment.ProcessId}-{Guid.NewGuid():N}");
    private int _disposed;

    public DocumentWorkspaceHostService(
        IDocumentWorkspaceRpcGateway gateway,
        WorkspaceMountStore mounts,
        DocumentCapabilityStore capabilities,
        ILocalDocumentActions actions,
        ILocalDocumentPreview? preview = null,
        ILocalDocumentFilePicker? filePicker = null,
        string? partitionKey = null,
        Func<string?>? partitionKeyProvider = null)
    {
        _gateway = gateway ?? throw new ArgumentNullException(nameof(gateway));
        _mounts = mounts ?? throw new ArgumentNullException(nameof(mounts));
        _capabilities = capabilities ?? throw new ArgumentNullException(nameof(capabilities));
        _actions = actions ?? throw new ArgumentNullException(nameof(actions));
        _preview = preview ?? new ShellDocumentPreview();
        _filePicker = filePicker;
        _partitionKey = string.IsNullOrWhiteSpace(partitionKey) ? null : partitionKey;
        _partitionKeyProvider = partitionKeyProvider;
    }

    public async Task<DocumentListPayload> ListAsync(
        string collection,
        string itemId,
        CancellationToken token)
    {
        await RetryPendingRegistrationsAsync(token).ConfigureAwait(false);
        var result = await _gateway.ReadFolderAsync(collection, itemId, token)
            .ConfigureAwait(false);
        EnsureDocumentWorkspacesReconciled(result.Documents);
        if (await PublishChangedLocalHeadsAsync(result.Documents, token)
            .ConfigureAwait(false))
        {
            result = await _gateway.ReadFolderAsync(collection, itemId, token)
                .ConfigureAwait(false);
        }
        var entries = new List<DocumentEntryPayload>(result.Documents.Count);

        foreach (var document in result.Documents)
        {
            entries.Add(BuildEntry(document));
        }

        return new DocumentListPayload(collection, itemId, entries);
    }

    public async Task<DocumentListPayload> ListGlobalAsync(CancellationToken token)
    {
        await RetryPendingRegistrationsAsync(token).ConfigureAwait(false);
        var result = await _gateway.ReadDocumentsAsync(500, 0, token)
            .ConfigureAwait(false);
        EnsureDocumentWorkspacesReconciled(result.Documents);
        if (await PublishChangedLocalHeadsAsync(result.Documents, token)
            .ConfigureAwait(false))
        {
            result = await _gateway.ReadDocumentsAsync(500, 0, token)
                .ConfigureAwait(false);
        }
        var entries = new List<DocumentEntryPayload>(result.Documents.Count);
        foreach (var document in result.Documents)
        {
            entries.Add(BuildEntry(document));
        }
        return new DocumentListPayload(null, null, entries);
    }

    private void EnsureDocumentWorkspacesReconciled(
        IEnumerable<DocumentSummary> documents)
    {
        foreach (string workspaceId in documents
            .Select(document => document.WorkspaceId)
            .Distinct(StringComparer.Ordinal))
        {
            string? root = _mounts.ResolveRoot(workspaceId, CurrentPartitionKey());
            if (string.IsNullOrWhiteSpace(root))
            {
                continue;
            }
            string manifestPath = WorkspacePathGuard.ResolveAndCheck(
                root,
                ".backup/workspace.json");
            if (!File.Exists(manifestPath))
            {
                continue;
            }
            _ = RequireManagedWorkspace(workspaceId);
        }
    }

    public async Task<DocumentHistoryPayload> ReadHistoryAsync(
        string entryHandle,
        int limit,
        int offset,
        CancellationToken token)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "history");
        await TryPublishDocumentRevisionsAsync(
            descriptor.WorkspaceId,
            descriptor.DocumentId,
            descriptor.CurrentRevisionId,
            token).ConfigureAwait(false);
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
                ["preview", "restore", "branch"]),
            string.IsNullOrWhiteSpace(revision.VersionLabel)
                ? $"v{revision.Sequence}"
                : revision.VersionLabel,
            revision.CreatedAt,
            revision.Size,
            revision.CreatedBy)).ToArray();
        return new DocumentHistoryPayload(entryHandle, revisions, result.Total);
    }

    public async Task<DocumentVersionCommittedPayload> CommitRevisionAsync(
        string entryHandle,
        string? note,
        string? schemeHandle,
        CancellationToken token)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "version");
        return await CommitVersionAsync(
            entryHandle,
            descriptor,
            versionLabel: null,
            note,
            schemeHandle,
            token).ConfigureAwait(false);
    }

    public async Task<DocumentVersionCommittedPayload> PromoteVersionAsync(
        string entryHandle,
        string versionLabel,
        string? note,
        string? schemeHandle,
        CancellationToken token)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "version");
        ValidateShortText(versionLabel, nameof(versionLabel), 128);
        return await CommitVersionAsync(
            entryHandle,
            descriptor,
            versionLabel.Trim(),
            note,
            schemeHandle,
            token).ConfigureAwait(false);
    }

    public DocumentRevisionPreviewCompletedPayload PreviewRevision(
        string entryHandle,
        string revisionHandle)
    {
        var entry = _capabilities.Resolve(entryHandle, "history");
        var revisionDescriptor = _capabilities.ResolveRevision(
            revisionHandle,
            "preview");
        EnsureSameDocument(entry, revisionDescriptor);

        var context = RequireManagedWorkspace(entry.WorkspaceId);
        var revision = RequireRevision(
            context,
            entry.DocumentId,
            revisionDescriptor.RevisionId);
        string extension = Path.GetExtension(
            context.Catalog.ReadDocument(entry.DocumentId)?.FileName ?? string.Empty);
        string previewPath = Path.Combine(
            _revisionPreviewRoot,
            $"preview-{Guid.NewGuid():N}{extension}");
        MaterializeRevisionAtomically(
            context,
            revision,
            previewPath,
            overwrite: false);
        try
        {
            _preview.Show(previewPath);
        }
        catch
        {
            TryDelete(previewPath);
            throw;
        }
        return new DocumentRevisionPreviewCompletedPayload(
            entryHandle,
            revisionHandle,
            "preview");
    }

    public async Task<DocumentVersionCommittedPayload> RestoreRevisionAsync(
        string entryHandle,
        string revisionHandle,
        CancellationToken token)
    {
        var entry = _capabilities.Resolve(entryHandle, "restore");
        var revisionDescriptor = _capabilities.ResolveRevision(
            revisionHandle,
            "restore");
        EnsureSameDocument(entry, revisionDescriptor);

        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        WorkspaceVersionService.CommitOutcome outcome;
        RevisionManifest restoredFrom;
        string stagedPath = string.Empty;
        string expectedHead = string.Empty;
        string targetSchemeId = string.Empty;
        string? restoreTransactionId = null;
        string? committedRestoreRevisionId = null;
        WorkspaceVersionService? versionService = null;
        bool preserveRecoveryArtifacts = false;
        try
        {
            var context = RequireManagedWorkspace(entry.WorkspaceId);
            var json = new AtomicJsonStore();
            var revisions = new RevisionStore(context.BackupRoot, json);
            var refs = new RefStore(context.BackupRoot, json);
            var main = RequireMainScheme(refs, entry.DocumentId);
            targetSchemeId = main.SchemeId;
            restoredFrom = RequireRevision(
                revisions,
                entry.DocumentId,
                revisionDescriptor.RevisionId);
            expectedHead = entry.CurrentRevisionId ?? string.Empty;
            if (!string.Equals(
                main.HeadRevisionId,
                expectedHead,
                StringComparison.Ordinal))
            {
                throw new DocumentFileOperationException(
                    "The document changed after this view was loaded. Refresh and retry.",
                    "DOCUMENT_VERSION_CONFLICT");
            }

            string targetPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                main.WorkingRelativePath);
            restoreTransactionId = Guid.NewGuid().ToString("N");
            string restoreRevisionId = Guid.NewGuid().ToString("N");
            string stagedRelativePath =
                main.WorkingRelativePath
                + $".restore-{restoreTransactionId}.partial";
            stagedPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                stagedRelativePath);
            MaterializeRevisionAtomically(
                context,
                restoredFrom,
                stagedPath,
                overwrite: false);

            versionService = new WorkspaceVersionService(
                context.BackupRoot,
                new ContentObjectStore(context.BackupRoot),
                revisions,
                refs,
                json);
            versionService.PrepareRestoreTransaction(
                new WorkspaceVersionService.RestoreTransactionJournal(
                    WorkspaceVersionService.RestoreTransactionJournal.CurrentFormatVersion,
                    restoreTransactionId,
                    entry.DocumentId,
                    main.SchemeId,
                    expectedHead,
                    restoreRevisionId,
                    restoredFrom.RevisionId,
                    main.WorkingRelativePath,
                    stagedRelativePath,
                    restoredFrom.ContentHash,
                    restoredFrom.Size,
                    WorkspaceVersionService.RestoreTransactionStage.Prepared,
                    DateTimeOffset.UtcNow.ToString("O")));
            outcome = versionService.RestoreRevisionAsFormal(
                entry.DocumentId,
                main.SchemeId,
                expectedHead,
                restoredFrom.RevisionId,
                BuildRestoreLabel(restoredFrom.VersionLabel),
                "local",
                deviceId: null,
                comment: "Restored from an earlier file revision.",
                createdAt: DateTimeOffset.UtcNow.ToString("O"),
                revisionId: restoreRevisionId);
            if (!outcome.RefUpdated)
            {
                TryDeleteRestoreTransaction(
                    versionService,
                    restoreTransactionId);
                throw new DocumentFileOperationException(
                    "The document changed while the restore was being applied. Refresh and retry.",
                    "DOCUMENT_VERSION_CONFLICT");
            }
            committedRestoreRevisionId = outcome.RevisionId;
            versionService.MarkRestoreRefCommitted(restoreTransactionId);

            File.Move(stagedPath, targetPath, overwrite: true);
            if (!FileMatchesContent(
                targetPath,
                restoredFrom.ContentHash,
                restoredFrom.Size))
            {
                throw new InvalidDataException(
                    "restored working copy failed post-move verification");
            }
            stagedPath = string.Empty;
            committedRestoreRevisionId = null;
            try
            {
                versionService.DeleteRestoreTransaction(restoreTransactionId);
            }
            catch (Exception cleanupException)
            {
                TraceFileFailure(
                    "revision-restore-journal-cleanup",
                    cleanupException);
                throw new DocumentFileOperationException(
                    "The restore completed, but its recovery journal could not be cleaned.",
                    "DOCUMENT_RESTORE_JOURNAL_CLEANUP_FAILED");
            }
        }
        catch (DocumentCapabilityException)
        {
            throw;
        }
        catch (DocumentFileOperationException)
        {
            if (committedRestoreRevisionId is null
                && versionService is not null
                && restoreTransactionId is not null)
            {
                TryDeleteRestoreTransaction(
                    versionService,
                    restoreTransactionId);
            }
            throw;
        }
        catch (Exception ex)
        {
            TraceFileFailure("revision-restore", ex);
            bool compensationFailed = false;
            if (committedRestoreRevisionId is not null && versionService is not null)
            {
                try
                {
                    var compensation = versionService.CompensateRestoreHead(
                        entry.DocumentId,
                        targetSchemeId,
                        committedRestoreRevisionId,
                        expectedHead,
                        DateTimeOffset.UtcNow.ToString("O"));
                    compensationFailed = !compensation.RefRolledBack;
                    if (compensation.RefRolledBack
                        && restoreTransactionId is not null)
                    {
                        versionService.DeleteRestoreTransaction(
                            restoreTransactionId);
                    }
                }
                catch (Exception compensationException)
                {
                    compensationFailed = true;
                    TraceFileFailure(
                        "revision-restore-compensation",
                    compensationException);
                }
            }
            else if (versionService is not null && restoreTransactionId is not null)
            {
                TryDeleteRestoreTransaction(
                    versionService,
                    restoreTransactionId);
            }
            preserveRecoveryArtifacts = compensationFailed;
            throw new DocumentFileOperationException(
                compensationFailed
                    ? "The working copy could not be replaced and the restore ref could not be compensated safely."
                    : "The working copy could not be replaced; the restore ref was rolled back.",
                compensationFailed
                    ? "DOCUMENT_RESTORE_COMPENSATION_FAILED"
                    : "DOCUMENT_RESTORE_MATERIALIZE_FAILED");
        }
        finally
        {
            if (!preserveRecoveryArtifacts)
                TryDelete(stagedPath);
            _fileMutationGate.Release();
        }

        await TryPublishDocumentRevisionsAsync(
            entry.WorkspaceId,
            entry.DocumentId,
            entry.CurrentRevisionId,
            token).ConfigureAwait(false);
        return new DocumentVersionCommittedPayload(
            entryHandle,
            IssueRevisionHandle(
                entry.WorkspaceId,
                entry.DocumentId,
                outcome.RevisionId),
            BuildRestoreLabel(restoredFrom.VersionLabel),
            null);
    }

    public DocumentSchemeListLoadedPayload ListSchemes(string entryHandle)
    {
        var entry = _capabilities.Resolve(entryHandle, "schemes");
        var context = RequireManagedWorkspace(entry.WorkspaceId);
        var json = new AtomicJsonStore();
        var revisions = new RevisionStore(context.BackupRoot, json);
        var schemes = new SchemeService(
            context.BackupRoot,
            new ContentObjectStore(context.BackupRoot),
            revisions,
            new RefStore(context.BackupRoot, json),
            json);
        var payloads = schemes.ListSchemes(entry.DocumentId)
            .OrderBy(scheme => string.Equals(
                scheme.SchemeName,
                "main",
                StringComparison.Ordinal) ? 0 : 1)
            .ThenBy(scheme => scheme.SchemeName, StringComparer.OrdinalIgnoreCase)
            .Select(scheme => BuildSchemePayload(entry, scheme, revisions))
            .ToArray();
        return new DocumentSchemeListLoadedPayload(entryHandle, payloads);
    }

    public async Task<DocumentSchemeMutationCompletedPayload> CreateSchemeAsync(
        string entryHandle,
        string name,
        string? baseRevisionHandle,
        CancellationToken token)
    {
        var entry = _capabilities.Resolve(entryHandle, "schemes");
        ValidateShortText(name, nameof(name), 128);

        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        string? schemeDirectory = null;
        try
        {
            var context = RequireManagedWorkspace(entry.WorkspaceId);
            var json = new AtomicJsonStore();
            var revisions = new RevisionStore(context.BackupRoot, json);
            var refs = new RefStore(context.BackupRoot, json);
            var service = new SchemeService(
                context.BackupRoot,
                new ContentObjectStore(context.BackupRoot),
                revisions,
                refs,
                json);
            string normalizedName = name.Trim();
            if (service.ListSchemes(entry.DocumentId).Any(scheme =>
                string.Equals(
                    scheme.SchemeName,
                    normalizedName,
                    StringComparison.OrdinalIgnoreCase)))
            {
                throw new DocumentFileOperationException(
                    "A scheme with this name already exists.",
                    "DOCUMENT_SCHEME_NAME_CONFLICT");
            }

            string sourceRevisionId = entry.CurrentRevisionId
                ?? throw new DocumentFileOperationException(
                    "This document does not have a local base revision.",
                    "DOCUMENT_REVISION_UNAVAILABLE");
            if (!string.IsNullOrWhiteSpace(baseRevisionHandle))
            {
                var sourceHandle = _capabilities.ResolveRevision(
                    baseRevisionHandle,
                    "branch");
                EnsureSameDocument(entry, sourceHandle);
                sourceRevisionId = sourceHandle.RevisionId;
            }
            var sourceRevision = RequireRevision(
                revisions,
                entry.DocumentId,
                sourceRevisionId);
            var localDocument = context.Catalog.ReadDocument(entry.DocumentId)
                ?? throw new DocumentFileOperationException(
                    "The managed document manifest is unavailable.",
                    "DOCUMENT_MANIFEST_INVALID");
            string relativePath = $".schemes/{Guid.NewGuid():N}/{localDocument.FileName}";
            string workingPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                relativePath);
            schemeDirectory = Path.GetDirectoryName(workingPath);
            MaterializeRevisionAtomically(
                context,
                sourceRevision,
                workingPath,
                overwrite: false);
            var created = service.CreateScheme(
                entry.DocumentId,
                normalizedName,
                sourceRevisionId,
                relativePath,
                DateTimeOffset.UtcNow.ToString("O"));
            var scheme = refs.Read(entry.DocumentId, created.SchemeId)
                ?? throw new DocumentFileOperationException(
                    "The scheme reference was not persisted.",
                    "DOCUMENT_SCHEME_CREATE_FAILED");
            return new DocumentSchemeMutationCompletedPayload(
                entryHandle,
                BuildSchemePayload(entry, scheme, revisions));
        }
        catch
        {
            if (!string.IsNullOrWhiteSpace(schemeDirectory))
                TryDeleteDirectory(schemeDirectory);
            throw;
        }
        finally
        {
            _fileMutationGate.Release();
        }
    }

    public async Task<DocumentSchemeMutationCompletedPayload> RenameSchemeAsync(
        string entryHandle,
        string schemeHandle,
        string name,
        CancellationToken token)
    {
        var entry = _capabilities.Resolve(entryHandle, "schemes");
        var schemeDescriptor = _capabilities.ResolveScheme(schemeHandle, "rename");
        EnsureSameDocument(entry, schemeDescriptor);
        ValidateShortText(name, nameof(name), 128);

        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        try
        {
            var context = RequireManagedWorkspace(entry.WorkspaceId);
            var json = new AtomicJsonStore();
            var revisions = new RevisionStore(context.BackupRoot, json);
            var service = new SchemeService(
                context.BackupRoot,
                new ContentObjectStore(context.BackupRoot),
                revisions,
                new RefStore(context.BackupRoot, json),
                json);
            string normalizedName = name.Trim();
            if (service.ListSchemes(entry.DocumentId).Any(scheme =>
                !string.Equals(
                    scheme.SchemeId,
                    schemeDescriptor.SchemeId,
                    StringComparison.Ordinal)
                && string.Equals(
                    scheme.SchemeName,
                    normalizedName,
                    StringComparison.OrdinalIgnoreCase)))
            {
                throw new DocumentFileOperationException(
                    "A scheme with this name already exists.",
                    "DOCUMENT_SCHEME_NAME_CONFLICT");
            }
            var renamed = service.RenameScheme(
                entry.DocumentId,
                schemeDescriptor.SchemeId,
                schemeDescriptor.ObservedHeadRevisionId,
                normalizedName,
                DateTimeOffset.UtcNow.ToString("O"));
            return new DocumentSchemeMutationCompletedPayload(
                entryHandle,
                BuildSchemePayload(entry, renamed, revisions));
        }
        catch (RefCasConflictException)
        {
            throw new DocumentFileOperationException(
                "The scheme changed after this view was loaded. Refresh and retry.",
                "DOCUMENT_VERSION_CONFLICT");
        }
        finally
        {
            _fileMutationGate.Release();
        }
    }

    public async Task<DocumentSchemeMutationCompletedPayload> ArchiveSchemeAsync(
        string entryHandle,
        string schemeHandle,
        CancellationToken token)
    {
        var entry = _capabilities.Resolve(entryHandle, "schemes");
        var schemeDescriptor = _capabilities.ResolveScheme(schemeHandle, "archive");
        EnsureSameDocument(entry, schemeDescriptor);

        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        try
        {
            var context = RequireManagedWorkspace(entry.WorkspaceId);
            var json = new AtomicJsonStore();
            var revisions = new RevisionStore(context.BackupRoot, json);
            var service = new SchemeService(
                context.BackupRoot,
                new ContentObjectStore(context.BackupRoot),
                revisions,
                new RefStore(context.BackupRoot, json),
                json);
            var archived = service.ArchiveScheme(
                entry.DocumentId,
                schemeDescriptor.SchemeId,
                schemeDescriptor.ObservedHeadRevisionId,
                DateTimeOffset.UtcNow.ToString("O"));
            return new DocumentSchemeMutationCompletedPayload(
                entryHandle,
                BuildSchemePayload(entry, archived, revisions));
        }
        catch (RefCasConflictException)
        {
            throw new DocumentFileOperationException(
                "The scheme changed after this view was loaded. Refresh and retry.",
                "DOCUMENT_VERSION_CONFLICT");
        }
        finally
        {
            _fileMutationGate.Release();
        }
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

    /// <summary>
    /// Resolves a drag-out capability to a native-only absolute path. The path
    /// must be consumed by the host's data-object API and must never be posted
    /// to the renderer.
    /// </summary>
    public string ResolveDragOutPath(string entryHandle)
    {
        var descriptor = _capabilities.Resolve(entryHandle, "dragOut");
        string fullPath = ResolveExistingPath(descriptor);
        FileAttributes attributes;
        try
        {
            attributes = File.GetAttributes(fullPath);
        }
        catch (Exception)
        {
            throw new DocumentCapabilityException(
                "文件不可用于拖出操作。",
                "DOCUMENT_DRAG_OUT_UNSAFE");
        }
        if (attributes.HasFlag(FileAttributes.Directory)
            || attributes.HasFlag(FileAttributes.ReparsePoint)
            || DangerousFileExtensions.Contains(Path.GetExtension(fullPath)))
        {
            throw new DocumentCapabilityException(
                "文件不可用于拖出操作。",
                "DOCUMENT_DRAG_OUT_UNSAFE");
        }
        return fullPath;
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

    /// <summary>
    /// Copies a native-picker-selected regular file into a locally authoritative
    /// managed workspace and commits its document manifest only after the copy
    /// has been atomically moved into place. No source or destination path is
    /// accepted from the renderer or metadata index.
    /// </summary>
    public async Task<DocumentImportResult?> ImportFromPickerAsync(
        DocumentImportRequest request,
        CancellationToken token)
    {
        ArgumentNullException.ThrowIfNull(request);
        ValidateImportRequest(request);
        var context = RequireManagedWorkspace(request.WorkspaceId);
        ValidateDestinationFolder(context, request.FolderId);
        var picker = RequireFilePicker();
        string? selectedPath = await picker.PickFileAsync(
            DocumentFilePickPurpose.Import,
            suggestedFileName: null,
            token).ConfigureAwait(false);
        if (string.IsNullOrWhiteSpace(selectedPath)) return null;
        return await ImportFromHostPathAsync(request, selectedPath, token)
            .ConfigureAwait(false);
    }

    /// <summary>
    /// Imports a path obtained through a native-only boundary such as
    /// CoreWebView2File.AdditionalObjects. This method must never be exposed to
    /// renderer JSON or metadata-index results.
    /// </summary>
    internal async Task<DocumentImportResult> ImportFromHostPathAsync(
        DocumentImportRequest request,
        string selectedPath,
        CancellationToken token)
    {
        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        try
        {
            return await ImportFromHostPathCoreAsync(request, selectedPath, token)
                .ConfigureAwait(false);
        }
        finally
        {
            _fileMutationGate.Release();
        }
    }

    private async Task<DocumentImportResult> ImportFromHostPathCoreAsync(
        DocumentImportRequest request,
        string selectedPath,
        CancellationToken token)
    {
        ArgumentNullException.ThrowIfNull(request);
        bool pendingRegistration = false;
        try
        {
            ValidateImportRequest(request);
            var context = RequireManagedWorkspace(request.WorkspaceId);
            ValidateDestinationFolder(context, request.FolderId);
            var source = ValidateSelectedSource(selectedPath);
            string documentId = Guid.NewGuid().ToString("D");
            string schemeId = Guid.NewGuid().ToString("D");
            string revisionId = Guid.NewGuid().ToString("D");
            string createdAt = DateTimeOffset.UtcNow.ToString("O");
            string stagingPath = await StageCopyAsync(context, source, token)
                .ConfigureAwait(false);
            string? destinationPath = null;
            string manifestPath = context.Catalog.GetDocumentPath(documentId);
            var revisions = new RevisionStore(context.BackupRoot, new AtomicJsonStore());
            var refs = new RefStore(context.BackupRoot, new AtomicJsonStore());
            string revisionPath = revisions.GetPath(documentId, revisionId);
            string refPath = refs.GetPath(documentId, schemeId);
            string journalPath = GetImportJournalPath(context, documentId);
            try
            {
                var objectCommit = new ContentObjectStore(context.BackupRoot)
                    .Commit(stagingPath);
                var (manifest, committedPath) = MoveImportIntoPlace(
                    context,
                    documentId,
                    request.FolderId,
                    source.FileName,
                    GetMimeType(source.Extension),
                    stagingPath,
                    createdAt);
                destinationPath = committedPath;
                string workingRelativePath =
                    context.Catalog.ResolveWorkingRelativePath(manifest);
                revisions.Write(new RevisionManifest(
                    RevisionManifest.CurrentFormatVersion,
                    revisionId,
                    documentId,
                    schemeId,
                    ParentRevisionId: null,
                    SourceRevisionId: null,
                    RestoredFromRevisionId: null,
                    Sequence: 1,
                    VersionLabel: "V1",
                    Kind: RevisionKind.Formal,
                    ContentHash: objectCommit.ContentHash,
                    Size: objectCommit.Size,
                    MimeType: manifest.MimeType,
                    WorkingRelativePath: workingRelativePath,
                    CreatedAt: createdAt,
                    CreatedBy: null,
                    DeviceId: null,
                    Comment: null));
                refs.Initialize(new RefManifest(
                    RefManifest.CurrentFormatVersion,
                    documentId,
                    schemeId,
                    "main",
                    revisionId,
                    workingRelativePath,
                    createdAt));
                context.Catalog.WriteDocument(manifest);
                var registrationRequest = new RegisterDocumentParams(
                        context.WorkspaceId,
                        context.WorkspaceName,
                        documentId,
                        manifest.FileName,
                        manifest.MimeType,
                        schemeId,
                        revisionId,
                        objectCommit.ContentHash,
                        objectCommit.Size,
                        createdAt,
                        request.ItemCollection,
                        request.ItemId,
                        request.LinkType);
                new AtomicJsonStore().Write(
                    journalPath,
                    new DocumentImportJournal(
                        DocumentImportJournal.CurrentFormatVersion,
                        registrationRequest,
                        createdAt));
                pendingRegistration = true;
                var registration = await _gateway.RegisterDocumentAsync(
                    registrationRequest,
                    token).ConfigureAwait(false);
                if (!string.Equals(
                    registration.DocumentId,
                    documentId,
                    StringComparison.Ordinal))
                {
                    throw new DocumentFileOperationException(
                        "远端文档索引返回了无效标识。",
                        "DOCUMENT_REGISTER_INVALID");
                }
                TryDelete(journalPath);
                pendingRegistration = false;
                return new DocumentImportResult(
                    manifest.DocumentId,
                    manifest.WorkspaceId,
                    manifest.FolderId,
                    manifest.FileName,
                    manifest.MimeType,
                    schemeId,
                    revisionId,
                    registration.LinkId);
            }
            catch
            {
                if (!pendingRegistration)
                {
                    TryDelete(manifestPath);
                    TryDelete(manifestPath + ".tmp");
                    TryDelete(refPath);
                    TryDelete(refPath + ".tmp");
                    TryDelete(revisionPath);
                    TryDelete(revisionPath + ".tmp");
                    TryDelete(destinationPath);
                }
                throw;
            }
            finally
            {
                TryDelete(stagingPath);
            }
        }
        catch (OperationCanceledException)
        {
            throw;
        }
        catch (DocumentFileOperationException)
        {
            throw;
        }
        catch (Exception ex)
        {
            TraceFileFailure("import", ex);
            throw new DocumentFileOperationException(
                pendingRegistration
                    ? "文件已安全保存，将在连接恢复后继续同步索引。"
                    : "文件导入失败，未更改工作区索引。",
                pendingRegistration
                    ? "DOCUMENT_REGISTER_PENDING"
                    : "DOCUMENT_IMPORT_FAILED");
        }
    }

    /// <summary>
    /// Repairs a missing managed document by copying a native-picker-selected
    /// replacement into the path resolved again from the local manifest. The
    /// capability's cached relative path and all remote path metadata are ignored.
    /// </summary>
    public async Task<DocumentRelinkResult?> RelinkMissingFromPickerAsync(
        string entryHandle,
        CancellationToken token)
    {
        string? selectedPath;
        try
        {
            var descriptor = _capabilities.Resolve(entryHandle, "relocate");
            var context = RequireManagedWorkspace(descriptor.WorkspaceId);
            var manifest = context.Catalog.ReadDocument(descriptor.DocumentId)
                ?? throw new DocumentFileOperationException(
                    "本地文档索引缺失，无法重新关联。",
                    "DOCUMENT_MANIFEST_MISSING");
            ValidateActiveDocumentManifest(manifest, descriptor);
            string relativePath = context.Catalog.ResolveWorkingRelativePath(manifest);
            string destinationPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                relativePath);
            if (File.Exists(destinationPath) || Directory.Exists(destinationPath))
                throw new DocumentFileOperationException(
                    "文档目标位置已存在文件，请刷新文件列表。",
                    "DOCUMENT_RELINK_TARGET_EXISTS");
            selectedPath = await RequireFilePicker().PickFileAsync(
                DocumentFilePickPurpose.RelinkMissing,
                manifest.FileName,
                token).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            throw;
        }
        catch (DocumentCapabilityException)
        {
            throw;
        }
        catch (DocumentFileOperationException)
        {
            throw;
        }
        catch (Exception ex)
        {
            TraceFileFailure("relink-picker", ex);
            throw new DocumentFileOperationException(
                "无法打开本机文件选择器。",
                "DOCUMENT_PICKER_UNAVAILABLE");
        }
        if (string.IsNullOrWhiteSpace(selectedPath)) return null;

        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        try
        {
            return await RelinkMissingFromPickerCoreAsync(
                    entryHandle,
                    selectedPath,
                    token)
                .ConfigureAwait(false);
        }
        finally
        {
            _fileMutationGate.Release();
        }
    }

    private async Task<DocumentRelinkResult?> RelinkMissingFromPickerCoreAsync(
        string entryHandle,
        string selectedPath,
        CancellationToken token)
    {
        try
        {
            var descriptor = _capabilities.Resolve(entryHandle, "relocate");
            var context = RequireManagedWorkspace(descriptor.WorkspaceId);
            var manifest = context.Catalog.ReadDocument(descriptor.DocumentId)
                ?? throw new DocumentFileOperationException(
                    "本地文档索引缺失，无法重新关联。",
                    "DOCUMENT_MANIFEST_MISSING");
            ValidateActiveDocumentManifest(manifest, descriptor);

            string authoritativeRelativePath =
                context.Catalog.ResolveWorkingRelativePath(manifest);
            string destinationPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                authoritativeRelativePath);
            if (File.Exists(destinationPath) || Directory.Exists(destinationPath))
                throw new DocumentFileOperationException(
                    "文档目标位置已存在文件，请刷新文件列表。",
                    "DOCUMENT_RELINK_TARGET_EXISTS");

            var source = ValidateSelectedSource(selectedPath);
            if (!string.Equals(
                source.Extension,
                Path.GetExtension(manifest.FileName),
                StringComparison.OrdinalIgnoreCase))
            {
                throw new DocumentFileOperationException(
                    "所选文件类型与缺失文档不一致。",
                    "DOCUMENT_RELINK_TYPE_MISMATCH");
            }

            string stagingPath = await StageCopyAsync(context, source, token)
                .ConfigureAwait(false);
            try
            {
                if (string.IsNullOrWhiteSpace(descriptor.CurrentRevisionId))
                    throw new DocumentFileOperationException(
                        "当前版本信息缺失，无法安全恢复文件。",
                        "DOCUMENT_RELINK_REVISION_MISSING");
                var currentRevision = new RevisionStore(
                    context.BackupRoot,
                    new AtomicJsonStore()).Read(
                        manifest.DocumentId,
                        descriptor.CurrentRevisionId)
                    ?? throw new DocumentFileOperationException(
                        "当前版本索引缺失，无法安全恢复文件。",
                        "DOCUMENT_RELINK_REVISION_MISSING");
                var currentRef = new RefStore(
                    context.BackupRoot,
                    new AtomicJsonStore()).Read(
                        manifest.DocumentId,
                        currentRevision.SchemeId);
                if (currentRef is null
                    || !string.Equals(
                        currentRef.SchemeName,
                        "main",
                        StringComparison.Ordinal)
                    || !string.Equals(
                        currentRef.HeadRevisionId,
                        descriptor.CurrentRevisionId,
                        StringComparison.Ordinal)
                    || !string.Equals(
                        currentRef.WorkingRelativePath,
                        authoritativeRelativePath,
                        StringComparison.Ordinal)
                    || !string.Equals(
                        currentRevision.WorkingRelativePath,
                        authoritativeRelativePath,
                        StringComparison.Ordinal))
                {
                    throw new DocumentFileOperationException(
                        "本地与远端当前版本不一致，请刷新或修复索引后重试。",
                        "DOCUMENT_RELINK_HEAD_MISMATCH");
                }
                string stagedHash = ContentObjectStore.ComputeHash(stagingPath);
                long stagedSize = new FileInfo(stagingPath).Length;
                if (!string.Equals(
                        stagedHash,
                        currentRevision.ContentHash,
                        StringComparison.OrdinalIgnoreCase)
                    || stagedSize != currentRevision.Size)
                {
                    throw new DocumentFileOperationException(
                        "所选文件不是当前版本的原始内容。如需替换内容，请提交新版本。",
                        "DOCUMENT_RELINK_CONTENT_MISMATCH");
                }
                EnsureDestinationDirectory(context.Root, authoritativeRelativePath);
                destinationPath = WorkspacePathGuard.ResolveAndCheck(
                    context.Root,
                    authoritativeRelativePath);
                if (File.Exists(destinationPath) || Directory.Exists(destinationPath))
                    throw new DocumentFileOperationException(
                        "文档目标位置已存在文件，请刷新文件列表。",
                        "DOCUMENT_RELINK_TARGET_EXISTS");
                File.Move(stagingPath, destinationPath, overwrite: false);
                return new DocumentRelinkResult(
                    manifest.DocumentId,
                    manifest.WorkspaceId,
                    manifest.FileName);
            }
            finally
            {
                TryDelete(stagingPath);
            }
        }
        catch (OperationCanceledException)
        {
            throw;
        }
        catch (DocumentCapabilityException)
        {
            throw;
        }
        catch (DocumentFileOperationException)
        {
            throw;
        }
        catch (Exception ex)
        {
            TraceFileFailure("relink", ex);
            throw new DocumentFileOperationException(
                "文件重新关联失败，未覆盖工作区文件。",
                "DOCUMENT_RELINK_FAILED");
        }
    }

    public void RevokeAll() => _capabilities.RevokeAll();

    /// <summary>
    /// Starts a new renderer/authentication capability generation. Handles
    /// from every earlier generation become invalid immediately.
    /// </summary>
    public long RotateCapabilityEpoch() => _capabilities.RotateEpoch();

    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0) return;
        RevokeAll();
        _preview.Dispose();
        _fileMutationGate.Dispose();
        _publishGate.Dispose();
        TryDeleteDirectory(_revisionPreviewRoot);
    }

    private async Task<DocumentVersionCommittedPayload> CommitVersionAsync(
        string entryHandle,
        DocumentCapabilityDescriptor entry,
        string? versionLabel,
        string? note,
        string? schemeHandle,
        CancellationToken token)
    {
        if (note is not null && note.Length > 2000)
            throw new DocumentFileOperationException(
                "The revision note is too long.",
                "BAD_PAYLOAD");

        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        WorkspaceVersionService.CommitOutcome outcome;
        string? nextSchemeHandle = null;
        string label;
        try
        {
            var context = RequireManagedWorkspace(entry.WorkspaceId);
            var json = new AtomicJsonStore();
            var revisions = new RevisionStore(context.BackupRoot, json);
            var refs = new RefStore(context.BackupRoot, json);
            DocumentSchemeCapabilityDescriptor? selectedScheme = null;
            RefManifest scheme;
            if (!string.IsNullOrWhiteSpace(schemeHandle))
            {
                selectedScheme = _capabilities.ResolveScheme(
                    schemeHandle,
                    "commit");
                EnsureSameDocument(entry, selectedScheme);
                scheme = refs.Read(entry.DocumentId, selectedScheme.SchemeId)
                    ?? throw new DocumentFileOperationException(
                        "The selected scheme no longer exists.",
                        "DOCUMENT_SCHEME_UNAVAILABLE");
            }
            else
            {
                scheme = RequireMainScheme(refs, entry.DocumentId);
            }
            if (scheme.Status != SchemeStatus.Active)
                throw new DocumentFileOperationException(
                    "Archived schemes cannot receive new revisions.",
                    "DOCUMENT_SCHEME_ARCHIVED");

            string expectedHead = selectedScheme?.ObservedHeadRevisionId
                ?? entry.CurrentRevisionId
                ?? string.Empty;
            string workingPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                scheme.WorkingRelativePath);
            if (!File.Exists(workingPath))
                throw new DocumentFileOperationException(
                    "The scheme working copy is missing.",
                    "DOCUMENT_MISSING");
            var localDocument = context.Catalog.ReadDocument(entry.DocumentId)
                ?? throw new DocumentFileOperationException(
                    "The managed document manifest is unavailable.",
                    "DOCUMENT_MANIFEST_INVALID");
            var service = new SchemeService(
                context.BackupRoot,
                new ContentObjectStore(context.BackupRoot),
                revisions,
                refs,
                json);
            label = versionLabel
                ?? $"R{service.GetNextSequence(entry.DocumentId, scheme.SchemeId)}";
            outcome = service.CommitSchemeVersion(
                workingPath,
                scheme.WorkingRelativePath,
                entry.DocumentId,
                scheme.SchemeId,
                expectedHead,
                label,
                localDocument.MimeType,
                "local",
                deviceId: null,
                comment: string.IsNullOrWhiteSpace(note) ? null : note.Trim(),
                createdAt: DateTimeOffset.UtcNow.ToString("O"));
            if (!outcome.RefUpdated)
                throw new DocumentFileOperationException(
                    "The document changed while this revision was being committed. Refresh and retry.",
                    "DOCUMENT_VERSION_CONFLICT");
            if (selectedScheme is not null)
            {
                var updatedScheme = refs.Read(entry.DocumentId, scheme.SchemeId)
                    ?? throw new DocumentFileOperationException(
                        "The updated scheme reference is unavailable.",
                        "DOCUMENT_SCHEME_UNAVAILABLE");
                nextSchemeHandle = IssueSchemeHandle(entry, updatedScheme);
            }
        }
        finally
        {
            _fileMutationGate.Release();
        }

        await TryPublishDocumentRevisionsAsync(
            entry.WorkspaceId,
            entry.DocumentId,
            entry.CurrentRevisionId,
            token).ConfigureAwait(false);
        return new DocumentVersionCommittedPayload(
            entryHandle,
            IssueRevisionHandle(
                entry.WorkspaceId,
                entry.DocumentId,
                outcome.RevisionId),
            label,
            nextSchemeHandle);
    }

    private DocumentSchemeBridgeEntry BuildSchemePayload(
        DocumentCapabilityDescriptor entry,
        RefManifest scheme,
        RevisionStore revisions)
    {
        RevisionManifest? head = string.IsNullOrWhiteSpace(scheme.HeadRevisionId)
            ? null
            : revisions.Read(entry.DocumentId, scheme.HeadRevisionId);
        string? revisionHandle = head is null
            ? null
            : IssueRevisionHandle(
                entry.WorkspaceId,
                entry.DocumentId,
                head.RevisionId);
        return new DocumentSchemeBridgeEntry(
            IssueSchemeHandle(entry, scheme),
            scheme.SchemeName,
            revisionHandle,
            head?.VersionLabel,
            scheme.Status == SchemeStatus.Archived,
            string.Equals(scheme.SchemeName, "main", StringComparison.Ordinal));
    }

    private string IssueSchemeHandle(
        DocumentCapabilityDescriptor entry,
        RefManifest scheme)
    {
        var capabilities = new List<string> { "view" };
        if (scheme.Status == SchemeStatus.Active)
        {
            capabilities.Add("commit");
            if (!string.Equals(scheme.SchemeName, "main", StringComparison.Ordinal))
            {
                capabilities.Add("rename");
                capabilities.Add("archive");
            }
        }
        return _capabilities.IssueScheme(
            entry.WorkspaceId,
            entry.DocumentId,
            scheme.SchemeId,
            scheme.HeadRevisionId,
            capabilities);
    }

    private string IssueRevisionHandle(
        string workspaceId,
        string documentId,
        string revisionId)
        => _capabilities.IssueRevision(
            workspaceId,
            documentId,
            revisionId,
            ["preview", "restore", "branch"]);

    private static RefManifest RequireMainScheme(
        RefStore refs,
        string documentId)
    {
        var main = refs.ListByDocument(documentId)
            .Where(reference => string.Equals(
                reference.SchemeName,
                "main",
                StringComparison.Ordinal))
            .ToArray();
        if (main.Length != 1)
            throw new DocumentFileOperationException(
                "The document must have exactly one main scheme.",
                "DOCUMENT_MAIN_SCHEME_INVALID");
        return main[0];
    }

    private static RevisionManifest RequireRevision(
        ManagedWorkspaceContext context,
        string documentId,
        string revisionId)
        => RequireRevision(
            new RevisionStore(context.BackupRoot, new AtomicJsonStore()),
            documentId,
            revisionId);

    private static RevisionManifest RequireRevision(
        RevisionStore revisions,
        string documentId,
        string revisionId)
        => revisions.Read(documentId, revisionId)
            ?? throw new DocumentFileOperationException(
                "The selected revision is not available in this local workspace.",
                "DOCUMENT_REVISION_UNAVAILABLE");

    private static void EnsureSameDocument(
        DocumentCapabilityDescriptor entry,
        DocumentRevisionCapabilityDescriptor revision)
    {
        if (!string.Equals(entry.WorkspaceId, revision.WorkspaceId, StringComparison.Ordinal)
            || !string.Equals(entry.DocumentId, revision.DocumentId, StringComparison.Ordinal))
        {
            throw new DocumentCapabilityException(
                "The revision handle does not belong to this document.",
                "REVISION_HANDLE_MISMATCH");
        }
    }

    private static void EnsureSameDocument(
        DocumentCapabilityDescriptor entry,
        DocumentSchemeCapabilityDescriptor scheme)
    {
        if (!string.Equals(entry.WorkspaceId, scheme.WorkspaceId, StringComparison.Ordinal)
            || !string.Equals(entry.DocumentId, scheme.DocumentId, StringComparison.Ordinal))
        {
            throw new DocumentCapabilityException(
                "The scheme handle does not belong to this document.",
                "SCHEME_HANDLE_MISMATCH");
        }
    }

    private static void MaterializeRevisionAtomically(
        ManagedWorkspaceContext context,
        RevisionManifest revision,
        string targetPath,
        bool overwrite)
    {
        var objects = new ContentObjectStore(context.BackupRoot);
        string objectPath = objects.GetObjectPath(revision.ContentHash);
        if (!File.Exists(objectPath)
            || new FileInfo(objectPath).Length != revision.Size
            || !string.Equals(
                ContentObjectStore.ComputeHash(objectPath),
                revision.ContentHash,
                StringComparison.Ordinal))
        {
            throw new DocumentFileOperationException(
                "The revision content object is missing or failed integrity verification.",
                "DOCUMENT_REVISION_OBJECT_INVALID");
        }

        string directory = Path.GetDirectoryName(targetPath)
            ?? throw new DocumentFileOperationException(
                "The revision target directory is invalid.",
                "DOCUMENT_REVISION_TARGET_INVALID");
        Directory.CreateDirectory(directory);
        string temporaryPath = Path.Combine(
            directory,
            $".{Path.GetFileName(targetPath)}.{Guid.NewGuid():N}.partial");
        try
        {
            using (var source = new FileStream(
                objectPath,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read,
                128 * 1024,
                FileOptions.SequentialScan))
            using (var temporary = new FileStream(
                temporaryPath,
                FileMode.CreateNew,
                FileAccess.Write,
                FileShare.None,
                128 * 1024,
                FileOptions.SequentialScan))
            {
                source.CopyTo(temporary, 128 * 1024);
                temporary.Flush(flushToDisk: true);
            }
            if (new FileInfo(temporaryPath).Length != revision.Size
                || !string.Equals(
                    ContentObjectStore.ComputeHash(temporaryPath),
                    revision.ContentHash,
                    StringComparison.Ordinal))
            {
                throw new DocumentFileOperationException(
                    "The staged revision failed integrity verification.",
                    "DOCUMENT_REVISION_STAGE_INVALID");
            }
            File.Move(temporaryPath, targetPath, overwrite);
        }
        finally
        {
            TryDelete(temporaryPath);
        }
    }

    private static void ValidateShortText(
        string value,
        string parameterName,
        int maximumLength)
    {
        if (string.IsNullOrWhiteSpace(value) || value.Trim().Length > maximumLength)
            throw new DocumentFileOperationException(
                $"{parameterName} is missing or too long.",
                "BAD_PAYLOAD");
    }

    private static string BuildRestoreLabel(string sourceLabel)
    {
        string label = $"Restore {sourceLabel}";
        return label.Length <= 128 ? label : label[..128];
    }

    private async Task<bool> PublishChangedLocalHeadsAsync(
        IEnumerable<DocumentSummary> documents,
        CancellationToken token)
    {
        bool published = false;
        foreach (var document in documents)
        {
            published = await TryPublishDocumentRevisionsAsync(
                document.WorkspaceId,
                document.DocumentId,
                document.MainHead,
                token).ConfigureAwait(false) || published;
        }
        return published;
    }

    private async Task<bool> TryPublishDocumentRevisionsAsync(
        string workspaceId,
        string documentId,
        string? indexedHead,
        CancellationToken token)
    {
        try
        {
            return await PublishDocumentRevisionsAsync(
                workspaceId,
                documentId,
                indexedHead,
                token).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            throw;
        }
        catch (Exception ex)
        {
            // Publishing is an eventually-consistent metadata projection.
            // Never make the locally authoritative document unavailable when
            // the projection is temporarily stale or incomplete.
            TraceFileFailure("revision-publish", ex);
            return false;
        }
    }

    private async Task<bool> PublishDocumentRevisionsAsync(
        string workspaceId,
        string documentId,
        string? indexedHead,
        CancellationToken token)
    {
        string? root = _mounts.ResolveRoot(workspaceId, CurrentPartitionKey());
        if (string.IsNullOrWhiteSpace(root))
            return false;

        await _publishGate.WaitAsync(token).ConfigureAwait(false);
        try
        {
            var context = RequireManagedWorkspace(workspaceId);
            var json = new AtomicJsonStore();
            var revisionStore = new RevisionStore(context.BackupRoot, json);
            var publishOutbox = new RevisionPublishOutboxStore(
                context.BackupRoot,
                json);
            var conflictedRevisionIds = publishOutbox
                .ListConflictedByDocument(documentId)
                .Select(issue => issue.Revision.RevisionId)
                .ToHashSet(StringComparer.Ordinal);
            var references = new RefStore(context.BackupRoot, json)
                .ListByDocument(documentId);
            if (references.Count == 0)
                return false;

            var allRevisions = revisionStore.ListByDocument(documentId)
                .ToDictionary(revision => revision.RevisionId, StringComparer.Ordinal);
            var pending = new Dictionary<string, RevisionManifest>(StringComparer.Ordinal);
            foreach (var queued in publishOutbox.ListByDocument(documentId))
            {
                if (!allRevisions.TryGetValue(queued.RevisionId, out var stored)
                    || !Equals(stored, queued))
                {
                    throw new DocumentFileOperationException(
                        "本地发布队列与版本清单不一致。",
                        "DOCUMENT_REVISION_OUTBOX_INVALID");
                }
                pending[stored.RevisionId] = stored;
            }

            var mainReferences = references
                .Where(reference => string.Equals(
                    reference.SchemeName,
                    "main",
                    StringComparison.Ordinal))
                .ToArray();
            if (mainReferences.Length != 1)
            {
                throw new DocumentFileOperationException(
                    "本地工作区必须且只能有一个 main 版本引用。",
                    "DOCUMENT_MAIN_REF_INVALID");
            }
            RefManifest mainReference = mainReferences[0];
            string? cursor = mainReference.HeadRevisionId;
            bool reachedIndexedHead = string.IsNullOrWhiteSpace(indexedHead);
            var visited = new HashSet<string>(StringComparer.Ordinal);
            while (!string.IsNullOrWhiteSpace(cursor))
            {
                if (string.Equals(cursor, indexedHead, StringComparison.Ordinal))
                {
                    reachedIndexedHead = true;
                    break;
                }
                if (conflictedRevisionIds.Contains(cursor))
                    return false;
                if (!visited.Add(cursor)
                    || !allRevisions.TryGetValue(cursor, out var revision)
                    || !string.Equals(
                        revision.SchemeId,
                        mainReference.SchemeId,
                        StringComparison.Ordinal))
                {
                    throw new DocumentFileOperationException(
                        "本地版本链无效，无法发布工作区索引。",
                        "DOCUMENT_REVISION_CHAIN_INVALID");
                }
                pending[revision.RevisionId] = revision;
                cursor = revision.ParentRevisionId;
            }
            if (!reachedIndexedHead)
            {
                throw new DocumentFileOperationException(
                    "本地 main 版本不是已发布版本的后代。",
                    "DOCUMENT_REVISION_DIVERGED");
            }
            if (pending.Count == 0)
                return false;

            var entries = pending.Values
                .OrderBy(revision => revision.Sequence)
                .ThenBy(revision => revision.RevisionId, StringComparer.Ordinal)
                .Select(revision => new RevisionIndexEntry(
                    revision.RevisionId,
                    revision.DocumentId,
                    revision.SchemeId,
                    revision.ParentRevisionId,
                    revision.Sequence,
                    revision.VersionLabel,
                    revision.Kind.ToString().ToLowerInvariant(),
                    revision.ContentHash,
                    revision.Size,
                    revision.MimeType,
                    UtcRfc3339Timestamp.Canonicalize(
                        revision.CreatedAt,
                        nameof(revision.CreatedAt)),
                    revision.CreatedBy,
                    revision.DeviceId,
                    revision.Comment))
                .ToArray();

            var batches = entries.Chunk(100).ToArray();
            bool shouldAdvanceHead = !string.Equals(
                mainReference.HeadRevisionId,
                indexedHead,
                StringComparison.Ordinal);
            for (int batchIndex = 0; batchIndex < batches.Length; batchIndex++)
            {
                var revisions = batches[batchIndex].ToList();
                PublishHeadAdvance? headAdvance =
                    shouldAdvanceHead && batchIndex == batches.Length - 1
                        ? new PublishHeadAdvance(
                            documentId,
                            mainReference.SchemeId,
                            indexedHead,
                            mainReference.HeadRevisionId)
                        : null;
                string canonical = JsonSerializer.Serialize(new
                {
                    revisions,
                    headAdvance,
                });
                string digest = Convert.ToHexString(
                    SHA256.HashData(Encoding.UTF8.GetBytes(canonical)))
                    .ToLowerInvariant();
                var result = await _gateway.PublishIndexBatchAsync(
                    new PublishIndexBatchParams(
                        revisions,
                        headAdvance,
                        $"workspace-{digest}"),
                    token).ConfigureAwait(false);
                var resultsByRevision = ValidatePublishReceipt(
                    revisions,
                    result);
                if (result.Conflicts.Count != 0)
                {
                    foreach (var revision in revisions)
                    {
                        PublishResult receipt =
                            resultsByRevision[revision.RevisionId];
                        if (string.Equals(
                            receipt.Status,
                            "conflict",
                            StringComparison.Ordinal))
                        {
                            publishOutbox.MarkConflicted(
                                allRevisions[revision.RevisionId],
                                "revision_immutable_conflict",
                                "The remote revision ID has different immutable metadata.",
                                UtcRfc3339Timestamp.Canonicalize(
                                    DateTimeOffset.UtcNow.ToString("O"),
                                    "updatedAt"));
                        }
                        else
                        {
                            publishOutbox.Complete(
                                revision.DocumentId,
                                revision.RevisionId);
                        }
                    }
                    return false;
                }
                foreach (var revision in revisions)
                {
                    publishOutbox.Complete(
                        revision.DocumentId,
                        revision.RevisionId);
                }
            }
            return true;
        }
        finally
        {
            _publishGate.Release();
        }
    }

    private static IReadOnlyDictionary<string, PublishResult> ValidatePublishReceipt(
        IReadOnlyList<RevisionIndexEntry> requested,
        PublishIndexBatchResult receipt)
    {
        if (receipt.Results is null || receipt.Conflicts is null)
            throw InvalidReceipt();

        var requestedIds = requested
            .Select(revision => revision.RevisionId)
            .ToHashSet(StringComparer.Ordinal);
        if (requestedIds.Count != requested.Count
            || receipt.Results.Count != requested.Count)
        {
            throw InvalidReceipt();
        }

        var results = new Dictionary<string, PublishResult>(StringComparer.Ordinal);
        foreach (PublishResult result in receipt.Results)
        {
            if (!requestedIds.Contains(result.RevisionId)
                || !results.TryAdd(result.RevisionId, result))
            {
                throw InvalidReceipt();
            }
        }

        var conflicts = new HashSet<string>(StringComparer.Ordinal);
        foreach (string conflict in receipt.Conflicts)
        {
            if (!requestedIds.Contains(conflict) || !conflicts.Add(conflict))
                throw InvalidReceipt();
        }

        foreach (PublishResult result in results.Values)
        {
            bool isConflict = string.Equals(
                result.Status,
                "conflict",
                StringComparison.Ordinal);
            bool isSuccess = result.Status is "created" or "unchanged";
            if ((!isConflict && !isSuccess)
                || isConflict != conflicts.Contains(result.RevisionId))
            {
                throw InvalidReceipt();
            }
        }
        return results;

        static DocumentFileOperationException InvalidReceipt()
            => new(
                "The revision publish receipt is incomplete or inconsistent.",
                "DOCUMENT_REVISION_RECEIPT_INVALID");
    }

    private async Task RetryPendingRegistrationsAsync(CancellationToken token)
    {
        await _fileMutationGate.WaitAsync(token).ConfigureAwait(false);
        try
        {
            string? currentPartitionKey = CurrentPartitionKey();
            int attempted = 0;
            foreach (var mount in _mounts.ReadAll())
            {
                if (attempted >= MaximumRegistrationRetriesPerRefresh) break;
                if (currentPartitionKey is not null
                    && !string.Equals(
                        mount.PartitionKey,
                        currentPartitionKey,
                        StringComparison.Ordinal))
                {
                    continue;
                }

                ManagedWorkspaceContext context;
                string journalDirectory;
                try
                {
                    context = RequireManagedWorkspace(mount.WorkspaceId);
                    journalDirectory = WorkspacePathGuard.ResolveAndCheck(
                        context.Root,
                        ".backup/import-journal");
                }
                catch (Exception ex)
                {
                    TraceFileFailure("registration-journal", ex);
                    continue;
                }
                if (!Directory.Exists(journalDirectory)) continue;

                foreach (string journalPath in Directory.EnumerateFiles(
                    journalDirectory,
                    "*.json",
                    SearchOption.TopDirectoryOnly).Take(
                        MaximumRegistrationRetriesPerRefresh - attempted))
                {
                    token.ThrowIfCancellationRequested();
                    DocumentImportJournal journal;
                    try
                    {
                        journal = new AtomicJsonStore()
                            .Read<DocumentImportJournal>(journalPath)
                            ?? throw new InvalidDataException(
                                "Import registration journal is empty.");
                        ValidatePendingRegistration(context, journal);
                    }
                    catch (Exception ex)
                    {
                        TraceFileFailure("registration-journal-invalid", ex);
                        QuarantineImportJournal(context, journalPath);
                        continue;
                    }

                    attempted++;
                    try
                    {
                        var result = await _gateway.RegisterDocumentAsync(
                            journal.Request,
                            token).ConfigureAwait(false);
                        if (!string.Equals(
                            result.DocumentId,
                            journal.Request.DocumentId,
                            StringComparison.Ordinal))
                        {
                            var error = new InvalidDataException(
                                "Pending registration returned a different document id.");
                            TraceFileFailure("registration-response-invalid", error);
                            QuarantineImportJournal(context, journalPath);
                            continue;
                        }
                        File.Delete(journalPath);
                    }
                    catch (OperationCanceledException)
                    {
                        throw;
                    }
                    catch (Exception ex)
                    {
                        // A transport/server failure likely affects the remaining batch;
                        // keep this and later journals for the next authenticated refresh.
                        TraceFileFailure("registration-retry", ex);
                        return;
                    }
                }
            }
        }
        finally
        {
            _fileMutationGate.Release();
        }
    }

    private static void QuarantineImportJournal(
        ManagedWorkspaceContext context,
        string journalPath)
    {
        try
        {
            string fileName = Path.GetFileName(journalPath);
            string relativePath = $".backup/import-journal-invalid/{fileName}";
            string quarantinePath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                relativePath);
            Directory.CreateDirectory(Path.GetDirectoryName(quarantinePath)!);
            quarantinePath = WorkspacePathGuard.ResolveAndCheck(context.Root, relativePath);
            File.Move(journalPath, quarantinePath, overwrite: true);
        }
        catch (Exception ex)
        {
            TraceFileFailure("registration-journal-quarantine", ex);
        }
    }

    private static void ValidatePendingRegistration(
        ManagedWorkspaceContext context,
        DocumentImportJournal journal)
    {
        if (journal.FormatVersion != DocumentImportJournal.CurrentFormatVersion
            || !string.Equals(
                journal.Request.WorkspaceId,
                context.WorkspaceId,
                StringComparison.Ordinal))
        {
            throw new InvalidDataException("Import registration journal has an invalid workspace.");
        }

        var document = context.Catalog.ReadDocument(journal.Request.DocumentId)
            ?? throw new InvalidDataException("Pending document manifest is missing.");
        var revision = new RevisionStore(
            context.BackupRoot,
            new AtomicJsonStore()).Read(
                journal.Request.DocumentId,
                journal.Request.RevisionId)
            ?? throw new InvalidDataException("Pending revision manifest is missing.");
        var schemeRef = new RefStore(
            context.BackupRoot,
            new AtomicJsonStore()).Read(
                journal.Request.DocumentId,
                journal.Request.SchemeId)
            ?? throw new InvalidDataException("Pending scheme ref is missing.");

        if (!string.Equals(document.FileName, journal.Request.FileName, StringComparison.Ordinal)
            || !string.Equals(document.MimeType, journal.Request.MimeType, StringComparison.Ordinal)
            || !string.Equals(revision.SchemeId, journal.Request.SchemeId, StringComparison.Ordinal)
            || !string.Equals(revision.ContentHash, journal.Request.Hash, StringComparison.Ordinal)
            || revision.Size != journal.Request.Size
            || !string.Equals(
                schemeRef.HeadRevisionId,
                journal.Request.RevisionId,
                StringComparison.Ordinal))
        {
            throw new InvalidDataException("Import registration journal does not match local indexes.");
        }
    }

    private static string GetImportJournalPath(
        ManagedWorkspaceContext context,
        string documentId)
        => WorkspacePathGuard.ResolveAndCheck(
            context.Root,
            $".backup/import-journal/{documentId}.json");

    private ManagedWorkspaceContext RequireManagedWorkspace(string workspaceId)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(workspaceId);
        string? root = _mounts.ResolveRoot(workspaceId, CurrentPartitionKey());
        if (string.IsNullOrWhiteSpace(root))
            throw new DocumentFileOperationException(
                "此工作区尚未挂载到本机。",
                "WORKSPACE_UNMOUNTED");

        var json = new AtomicJsonStore();
        string workspaceManifestPath;
        try
        {
            workspaceManifestPath = WorkspacePathGuard.ResolveAndCheck(
                root,
                ".backup/workspace.json");
        }
        catch (Exception ex)
        {
            TraceFileFailure("workspace", ex);
            throw new DocumentFileOperationException(
                "本地工作区结构无效。",
                "WORKSPACE_MANIFEST_INVALID");
        }
        var workspace = json.Read<WorkspaceManifest>(workspaceManifestPath);
        if (workspace is null
            || workspace.FormatVersion != WorkspaceManifest.CurrentFormatVersion
            || !string.Equals(workspace.WorkspaceId, workspaceId, StringComparison.Ordinal))
        {
            throw new DocumentFileOperationException(
                "本地工作区标识无效。",
                "WORKSPACE_MANIFEST_INVALID");
        }

        string normalizedRoot = Path.GetFullPath(root);
        var context = new ManagedWorkspaceContext(
            normalizedRoot,
            workspaceId,
            workspace.Name,
            Path.Combine(normalizedRoot, ".backup"),
            new DocumentCatalogStore(Path.Combine(normalizedRoot, ".backup"), json));
        EnsureRestoreTransactionsReconciled(context);
        return context;
    }

    private void EnsureRestoreTransactionsReconciled(
        ManagedWorkspaceContext context)
    {
        lock (_restoreReconciliationGate)
        {
            if (_reconciledWorkspaceRoots.Contains(context.Root))
                return;
            try
            {
                ReconcileRestoreTransactions(context);
                _reconciledWorkspaceRoots.Add(context.Root);
            }
            catch (DocumentFileOperationException)
            {
                throw;
            }
            catch (Exception ex)
            {
                TraceFileFailure("restore-reconciliation", ex);
                throw new DocumentFileOperationException(
                    "An interrupted file restore could not be reconciled safely.",
                    "DOCUMENT_RESTORE_RECOVERY_REQUIRED");
            }
        }
    }

    private static void ReconcileRestoreTransactions(
        ManagedWorkspaceContext context)
    {
        var json = new AtomicJsonStore();
        var objects = new ContentObjectStore(context.BackupRoot);
        var revisions = new RevisionStore(context.BackupRoot, json);
        var refs = new RefStore(context.BackupRoot, json);
        var outbox = new RevisionPublishOutboxStore(context.BackupRoot, json);
        var versions = new WorkspaceVersionService(
            context.BackupRoot,
            objects,
            revisions,
            refs,
            json);

        foreach (var transaction in versions.ListRestoreTransactions())
        {
            var reference = refs.Read(
                transaction.DocumentId,
                transaction.SchemeId)
                ?? throw new DocumentFileOperationException(
                    "An interrupted restore references a missing scheme.",
                    "DOCUMENT_RESTORE_RECOVERY_REQUIRED");
            string targetPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                transaction.WorkingRelativePath);
            string stagedPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                transaction.StagedRelativePath);
            RevisionManifest? restoreRevision = revisions.Read(
                transaction.DocumentId,
                transaction.RestoreRevisionId);

            if (string.Equals(
                reference.HeadRevisionId,
                transaction.RestoreRevisionId,
                StringComparison.Ordinal))
            {
                if (restoreRevision is null
                    || restoreRevision.Kind != RevisionKind.Restore
                    || !string.Equals(
                        restoreRevision.SchemeId,
                        transaction.SchemeId,
                        StringComparison.Ordinal)
                    || !string.Equals(
                        restoreRevision.RestoredFromRevisionId,
                        transaction.RestoredFromRevisionId,
                        StringComparison.Ordinal)
                    || !string.Equals(
                        restoreRevision.ContentHash,
                        transaction.ContentHash,
                        StringComparison.Ordinal)
                    || restoreRevision.Size != transaction.Size)
                {
                    throw new DocumentFileOperationException(
                        "An interrupted restore revision does not match its journal.",
                        "DOCUMENT_RESTORE_RECOVERY_REQUIRED");
                }

                outbox.Enqueue(restoreRevision);
                if (!FileMatchesContent(
                    targetPath,
                    transaction.ContentHash,
                    transaction.Size))
                {
                    if (FileMatchesContent(
                        stagedPath,
                        transaction.ContentHash,
                        transaction.Size))
                    {
                        try
                        {
                            Directory.CreateDirectory(
                                Path.GetDirectoryName(targetPath)!);
                            File.Move(stagedPath, targetPath, overwrite: true);
                        }
                        catch (Exception moveException)
                        {
                            TraceFileFailure(
                                "restore-reconciliation-materialize",
                                moveException);
                        }
                    }

                    if (!FileMatchesContent(
                        targetPath,
                        transaction.ContentHash,
                        transaction.Size))
                    {
                        var compensation = versions.CompensateRestoreHead(
                            transaction.DocumentId,
                            transaction.SchemeId,
                            transaction.RestoreRevisionId,
                            transaction.PreviousHeadRevisionId,
                            DateTimeOffset.UtcNow.ToString("O"));
                        if (!compensation.RefRolledBack)
                        {
                            throw new DocumentFileOperationException(
                                "An interrupted restore could neither complete nor roll back.",
                                "DOCUMENT_RESTORE_RECOVERY_REQUIRED");
                        }
                        TryDelete(stagedPath);
                        versions.DeleteRestoreTransaction(
                            transaction.TransactionId);
                        continue;
                    }
                }

                TryDelete(stagedPath);
                versions.DeleteRestoreTransaction(transaction.TransactionId);
                continue;
            }

            if (string.Equals(
                reference.HeadRevisionId,
                transaction.PreviousHeadRevisionId,
                StringComparison.Ordinal))
            {
                if (restoreRevision is not null
                    && FileMatchesContent(
                        targetPath,
                        transaction.ContentHash,
                        transaction.Size))
                {
                    try
                    {
                        refs.UpdateHead(
                            transaction.DocumentId,
                            transaction.SchemeId,
                            transaction.PreviousHeadRevisionId,
                            transaction.RestoreRevisionId,
                            DateTimeOffset.UtcNow.ToString("O"));
                        outbox.Enqueue(restoreRevision);
                    }
                    catch (RefCasConflictException)
                    {
                        throw new DocumentFileOperationException(
                            "An interrupted restore changed during reconciliation.",
                            "DOCUMENT_RESTORE_RECOVERY_REQUIRED");
                    }
                }
                TryDelete(stagedPath);
                versions.DeleteRestoreTransaction(transaction.TransactionId);
                continue;
            }

            throw new DocumentFileOperationException(
                "An interrupted restore has an unexpected current head.",
                "DOCUMENT_RESTORE_RECOVERY_REQUIRED");
        }
    }

    private static bool FileMatchesContent(
        string path,
        string expectedHash,
        long expectedSize)
    {
        try
        {
            return File.Exists(path)
                && new FileInfo(path).Length == expectedSize
                && string.Equals(
                    ContentObjectStore.ComputeHash(path),
                    expectedHash,
                    StringComparison.Ordinal);
        }
        catch
        {
            return false;
        }
    }

    private static void TryDeleteRestoreTransaction(
        WorkspaceVersionService versions,
        string transactionId)
    {
        try
        {
            versions.DeleteRestoreTransaction(transactionId);
        }
        catch (Exception ex)
        {
            TraceFileFailure("restore-journal-cleanup", ex);
        }
    }

    private static void ValidateImportRequest(DocumentImportRequest request)
    {
        bool hasCollection = !string.IsNullOrWhiteSpace(request.ItemCollection);
        bool hasItemId = !string.IsNullOrWhiteSpace(request.ItemId);
        if (hasCollection != hasItemId)
            throw new DocumentFileOperationException(
                "记录关联范围不完整。",
                "DOCUMENT_SCOPE_INVALID");
        if (request.LinkType is not ("attachment" or "primary" or "reference"))
            throw new DocumentFileOperationException(
                "文档关联类型无效。",
                "DOCUMENT_LINK_TYPE_INVALID");
    }

    private string? CurrentPartitionKey()
    {
        string? provided = _partitionKeyProvider?.Invoke();
        return string.IsNullOrWhiteSpace(provided) ? _partitionKey : provided;
    }

    private static void ValidateDestinationFolder(
        ManagedWorkspaceContext context,
        string? folderId)
    {
        if (folderId is null) return;
        FolderManifest folder;
        try
        {
            folder = context.Catalog.ReadFolder(folderId)
                ?? throw new DocumentFileOperationException(
                    "目标文件夹不存在。",
                    "DOCUMENT_FOLDER_INVALID");
        }
        catch (DocumentFileOperationException)
        {
            throw;
        }
        catch (Exception)
        {
            throw new DocumentFileOperationException(
                "目标文件夹标识无效。",
                "DOCUMENT_FOLDER_INVALID");
        }
        if (folder.FormatVersion != 1
            || !string.Equals(folder.WorkspaceId, context.WorkspaceId, StringComparison.Ordinal)
            || !string.Equals(folder.Status, "active", StringComparison.OrdinalIgnoreCase))
        {
            throw new DocumentFileOperationException(
                "目标文件夹不可用。",
                "DOCUMENT_FOLDER_INVALID");
        }
    }

    private ILocalDocumentFilePicker RequireFilePicker()
        => _filePicker ?? throw new DocumentFileOperationException(
            "本机文件选择器尚未连接。",
            "DOCUMENT_PICKER_UNAVAILABLE");

    private static SelectedSource ValidateSelectedSource(string selectedPath)
    {
        try
        {
            if (!Path.IsPathFullyQualified(selectedPath))
                throw new DocumentFileOperationException(
                    "所选文件路径无效。",
                    "DOCUMENT_SOURCE_INVALID");
            string fullPath = Path.GetFullPath(selectedPath);
            if (Directory.Exists(fullPath) || !File.Exists(fullPath))
                throw new DocumentFileOperationException(
                    "请选择一个存在的普通文件。",
                    "DOCUMENT_SOURCE_INVALID");
            var attributes = File.GetAttributes(fullPath);
            if (attributes.HasFlag(FileAttributes.Directory)
                || attributes.HasFlag(FileAttributes.ReparsePoint))
            {
                throw new DocumentFileOperationException(
                    "不支持目录、符号链接或重解析点。",
                    "DOCUMENT_SOURCE_INVALID");
            }
            long length = new FileInfo(fullPath).Length;
            if (length > MaximumImportBytes)
            {
                throw new DocumentFileOperationException(
                    "单个文件不能超过 2 GB。",
                    "DOCUMENT_SOURCE_TOO_LARGE");
            }

            string fileName = Path.GetFileName(fullPath);
            ValidateSafeFileName(fileName);
            string extension = Path.GetExtension(fileName);
            if (DangerousFileExtensions.Contains(extension))
                throw new DocumentFileOperationException(
                    "出于安全原因，不允许导入此文件类型。",
                    "DOCUMENT_SOURCE_TYPE_DENIED");
            return new SelectedSource(fullPath, fileName, extension);
        }
        catch (DocumentFileOperationException)
        {
            throw;
        }
        catch (Exception ex)
        {
            TraceFileFailure("source", ex);
            throw new DocumentFileOperationException(
                "无法读取所选文件。",
                "DOCUMENT_SOURCE_INVALID");
        }
    }

    private static void ValidateSafeFileName(string fileName)
    {
        if (string.IsNullOrWhiteSpace(fileName)
            || fileName.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0
            || WorkspacePathGuard.ShouldIgnore(fileName))
        {
            throw new DocumentFileOperationException(
                "所选文件名不适合工作区。",
                "DOCUMENT_SOURCE_INVALID");
        }
        try
        {
            string validated = WorkspacePathGuard.ValidateRelativePath(fileName);
            if (validated.Contains('/') || validated.Contains('\\')) throw new InvalidOperationException();
        }
        catch (Exception)
        {
            throw new DocumentFileOperationException(
                "所选文件名不适合工作区。",
                "DOCUMENT_SOURCE_INVALID");
        }
    }

    private static async Task<string> StageCopyAsync(
        ManagedWorkspaceContext context,
        SelectedSource source,
        CancellationToken token)
    {
        EnsureImportCapacity(context.Root, new FileInfo(source.FullPath).Length);
        string relativeStagingPath =
            $".backup/.staging/import-{Guid.NewGuid():N}.partial";
        string stagingPath = WorkspacePathGuard.ResolveAndCheck(
            context.Root,
            relativeStagingPath);
        Directory.CreateDirectory(Path.GetDirectoryName(stagingPath)!);
        stagingPath = WorkspacePathGuard.ResolveAndCheck(context.Root, relativeStagingPath);

        try
        {
            var initial = new FileInfo(source.FullPath);
            long initialLength = initial.Length;
            DateTime initialWriteTime = initial.LastWriteTimeUtc;
            await using (var input = new FileStream(
                source.FullPath,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read,
                128 * 1024,
                FileOptions.Asynchronous | FileOptions.SequentialScan))
            await using (var output = new FileStream(
                stagingPath,
                FileMode.CreateNew,
                FileAccess.Write,
                FileShare.None,
                128 * 1024,
                FileOptions.Asynchronous | FileOptions.SequentialScan))
            {
                await input.CopyToAsync(output, 128 * 1024, token).ConfigureAwait(false);
                await output.FlushAsync(token).ConfigureAwait(false);
                output.Flush(flushToDisk: true);
            }

            var final = new FileInfo(source.FullPath);
            final.Refresh();
            if (final.Length != initialLength || final.LastWriteTimeUtc != initialWriteTime)
                throw new DocumentFileOperationException(
                    "所选文件在复制期间发生变化，请重试。",
                    "DOCUMENT_SOURCE_CHANGED");
            return stagingPath;
        }
        catch
        {
            TryDelete(stagingPath);
            throw;
        }
    }

    private static void EnsureImportCapacity(string workspaceRoot, long sourceLength)
    {
        try
        {
            string? driveRoot = Path.GetPathRoot(workspaceRoot);
            if (string.IsNullOrWhiteSpace(driveRoot)) return;
            var drive = new DriveInfo(driveRoot);
            long required = checked(
                sourceLength * 2L + MinimumFreeSpaceReserveBytes);
            if (drive.IsReady && drive.AvailableFreeSpace < required)
            {
                throw new DocumentFileOperationException(
                    "工作区磁盘空间不足，请释放空间后重试。",
                    "WORKSPACE_DISK_FULL");
            }
        }
        catch (DocumentFileOperationException)
        {
            throw;
        }
        catch (Exception ex)
        {
            // Network/removable roots may not expose reliable capacity data.
            TraceFileFailure("capacity", ex);
        }
    }

    private static (DocumentManifest Manifest, string DestinationPath) MoveImportIntoPlace(
        ManagedWorkspaceContext context,
        string documentId,
        string? folderId,
        string requestedFileName,
        string mimeType,
        string stagingPath,
        string createdAt)
    {
        string extension = Path.GetExtension(requestedFileName);
        string stem = Path.GetFileNameWithoutExtension(requestedFileName);
        if (string.IsNullOrEmpty(stem))
        {
            stem = requestedFileName;
            extension = string.Empty;
        }

        for (int attempt = 0; attempt < MaximumNameAttempts; attempt++)
        {
            string candidateName = attempt == 0
                ? requestedFileName
                : $"{stem} ({attempt}){extension}";
            var manifest = new DocumentManifest(
                1,
                documentId,
                context.WorkspaceId,
                folderId,
                candidateName,
                mimeType,
                "active",
                createdAt);
            string relativePath = context.Catalog.ResolveWorkingRelativePath(manifest);
            EnsureDestinationDirectory(context.Root, relativePath);
            string destinationPath = WorkspacePathGuard.ResolveAndCheck(
                context.Root,
                relativePath);
            if (File.Exists(destinationPath) || Directory.Exists(destinationPath)) continue;
            try
            {
                File.Move(stagingPath, destinationPath, overwrite: false);
                return (manifest, destinationPath);
            }
            catch (IOException) when (
                File.Exists(destinationPath) || Directory.Exists(destinationPath))
            {
                // Another importer won this name after the check; try a suffix.
            }
        }
        throw new DocumentFileOperationException(
            "目标文件夹中存在过多同名文件。",
            "DOCUMENT_NAME_CONFLICT");
    }

    private static void EnsureDestinationDirectory(string root, string relativePath)
    {
        string destinationPath = WorkspacePathGuard.ResolveAndCheck(root, relativePath);
        string destinationDirectory = Path.GetDirectoryName(destinationPath)!;
        Directory.CreateDirectory(destinationDirectory);
        WorkspacePathGuard.ResolveAndCheck(root, relativePath);
    }

    private static void ValidateActiveDocumentManifest(
        DocumentManifest manifest,
        DocumentCapabilityDescriptor descriptor)
    {
        if (manifest.FormatVersion != 1
            || !string.Equals(manifest.DocumentId, descriptor.DocumentId, StringComparison.Ordinal)
            || !string.Equals(manifest.WorkspaceId, descriptor.WorkspaceId, StringComparison.Ordinal)
            || !string.Equals(manifest.Status, "active", StringComparison.OrdinalIgnoreCase))
        {
            throw new DocumentFileOperationException(
                "本地文档索引无效，无法重新关联。",
                "DOCUMENT_MANIFEST_INVALID");
        }
    }

    private static string GetMimeType(string extension)
        => MimeTypes.TryGetValue(extension, out string? mimeType)
            ? mimeType
            : "application/octet-stream";

    private static void TryDelete(string? path)
    {
        try
        {
            if (!string.IsNullOrWhiteSpace(path) && File.Exists(path)) File.Delete(path);
        }
        catch
        {
            // Best-effort rollback. No catalog entry is committed before the file move.
        }
    }

    private static void TryDeleteDirectory(string? path)
    {
        try
        {
            if (!string.IsNullOrWhiteSpace(path) && Directory.Exists(path))
                Directory.Delete(path, recursive: true);
        }
        catch
        {
            // Best effort for host-owned temporary and abandoned scheme paths.
        }
    }

    private static void TraceFileFailure(string operation, Exception exception)
        => Trace.TraceError(
            $"Document file {operation} failed ({exception.GetType().Name}, "
            + $"0x{exception.HResult:X8}).");

    private DocumentEntryPayload BuildEntry(DocumentSummary document)
    {
        string? root = _mounts.ResolveRoot(document.WorkspaceId, CurrentPartitionKey());
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
        var capabilities = new List<string> { "history", "schemes" };
        if (!string.IsNullOrWhiteSpace(document.LinkId)) capabilities.Add("unlink");
        if (exists)
        {
            if (!DangerousFileExtensions.Contains(Path.GetExtension(localDocument.FileName)))
            {
                capabilities.Add("open");
                capabilities.Add("dragOut");
            }
            if (_preview.CanPreview(fullPath))
                capabilities.Add("preview");
            capabilities.Add("reveal");
            capabilities.Add("version");
            capabilities.Add("restore");
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
            capabilities,
            document.MainHead);
        return new DocumentEntryPayload(
            handle,
            localDocument.FileName,
            localDocument.MimeType,
            exists ? "available" : "missing",
            capabilities.Contains("preview") ? "system" : "none",
            ResolveRevisionLabel(root, document.DocumentId, document.MainHead),
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
            capabilities,
            document.MainHead);
        return new DocumentEntryPayload(
            handle,
            document.FileName,
            document.MimeType,
            availability,
            "none",
            null,
            document.LinkType ?? "primary",
            capabilities);
    }

    private static string? ResolveRevisionLabel(
        string workspaceRoot,
        string documentId,
        string? revisionId)
    {
        if (string.IsNullOrWhiteSpace(revisionId)) return null;
        try
        {
            return new RevisionStore(
                Path.Combine(workspaceRoot, ".backup"),
                new AtomicJsonStore())
                .Read(documentId, revisionId)
                ?.VersionLabel;
        }
        catch
        {
            return null;
        }
    }

    private string ResolveExistingPath(DocumentCapabilityDescriptor descriptor)
    {
        string? root = _mounts.ResolveRoot(descriptor.WorkspaceId, CurrentPartitionKey());
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

    private sealed record ManagedWorkspaceContext(
        string Root,
        string WorkspaceId,
        string WorkspaceName,
        string BackupRoot,
        DocumentCatalogStore Catalog);

    private sealed record SelectedSource(string FullPath, string FileName, string Extension);
}

public sealed record DocumentListPayload(
    string? Collection,
    string? ItemId,
    IReadOnlyList<DocumentEntryPayload> Entries);

public sealed record DocumentEntryPayload(
    string EntryHandle,
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

internal sealed record DocumentImportJournal(
    int FormatVersion,
    RegisterDocumentParams Request,
    string CreatedAt)
{
    public const int CurrentFormatVersion = 1;
}
