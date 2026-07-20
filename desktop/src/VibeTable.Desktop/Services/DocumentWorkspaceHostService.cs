using System.Collections.Generic;
using System.Diagnostics;
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
    /// accepted from the renderer or Directus.
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
    /// renderer JSON or Directus metadata.
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
        RevokeAll();
        _preview.Dispose();
        _fileMutationGate.Dispose();
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
        return new ManagedWorkspaceContext(
            normalizedRoot,
            workspaceId,
            workspace.Name,
            Path.Combine(normalizedRoot, ".backup"),
            new DocumentCatalogStore(Path.Combine(normalizedRoot, ".backup"), json));
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
        var capabilities = new List<string> { "history" };
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
            capabilities,
            document.MainHead);
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

internal sealed record DocumentImportJournal(
    int FormatVersion,
    RegisterDocumentParams Request,
    string CreatedAt)
{
    public const int CurrentFormatVersion = 1;
}
