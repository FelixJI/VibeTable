using System.IO;
using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Native file-system adapter for documents owned by the active workspace-v2
/// Sidecar. It keeps no document topology: every UUID/path pair is obtained
/// from fileHistory.listDocuments and wrapped in an expiring session handle.
/// </summary>
public sealed class WorkspaceDocumentOsAdapter : IDisposable
{
    public const string ListDocumentsMethod = "fileHistory.listDocuments";
    public const string ImportDocumentMethod = "fileHistory.import";
    public const string RelinkDocumentMethod = "fileHistory.relink";

    private readonly Func<WorkspaceDocumentBinding?> _bindingProvider;
    private readonly DocumentCapabilityStore _capabilities;
    private readonly ILocalDocumentActions _actions;
    private readonly ILocalDocumentPreview _preview;
    private readonly ILocalDocumentFilePicker _filePicker;
    private long _sequence;
    private bool _disposed;

    public WorkspaceDocumentOsAdapter(
        Func<WorkspaceDocumentBinding?> bindingProvider,
        DocumentCapabilityStore capabilities,
        ILocalDocumentActions actions,
        ILocalDocumentPreview preview,
        ILocalDocumentFilePicker filePicker)
    {
        _bindingProvider = bindingProvider
            ?? throw new ArgumentNullException(nameof(bindingProvider));
        _capabilities = capabilities
            ?? throw new ArgumentNullException(nameof(capabilities));
        _actions = actions ?? throw new ArgumentNullException(nameof(actions));
        _preview = preview ?? throw new ArgumentNullException(nameof(preview));
        _filePicker = filePicker
            ?? throw new ArgumentNullException(nameof(filePicker));
    }

    public async Task<DocumentListPayload> ListGlobalAsync(
        CancellationToken cancellationToken)
    {
        WorkspaceDocumentBinding binding = RequireBinding();
        FileDocumentV2[] documents = await ReadDocumentsAsync(
            binding,
            cancellationToken).ConfigureAwait(false);
        _capabilities.RevokeAll();
        DocumentBridgeEntry[] entries = documents
            .Select(document => CreateEntry(binding, document))
            .ToArray();
        return new DocumentListPayload(null, null, entries);
    }

    public Task<DocumentListPayload> ListRecordAsync(
        string collection,
        string itemId,
        CancellationToken cancellationToken)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(collection);
        ArgumentException.ThrowIfNullOrWhiteSpace(itemId);
        cancellationToken.ThrowIfCancellationRequested();
        // v2 owns file topology but has no record-link model. Returning no
        // entries is the only non-authoritative representation.
        return Task.FromResult(
            new DocumentListPayload(collection, itemId, []));
    }

    public void Open(string handle)
        => _actions.Open(ResolveExisting(handle, "open"));

    public void Reveal(string handle)
        => _actions.Reveal(ResolveExisting(handle, "reveal"));

    public void Preview(string handle)
        => _preview.Show(ResolveExisting(handle, "preview"));

    public string ResolveDragOutPath(string handle)
        => ResolveExisting(handle, "dragOut");

    public async Task<WorkspaceDocumentImportResult?> ImportFromPickerAsync(
        CancellationToken cancellationToken)
    {
        WorkspaceDocumentBinding binding = RequireWritableBinding();
        string? source = await _filePicker.PickFileAsync(
            DocumentFilePickPurpose.Import,
            suggestedFileName: null,
            cancellationToken).ConfigureAwait(false);
        return string.IsNullOrWhiteSpace(source)
            ? null
            : await ImportFromHostPathAsync(
                binding,
                source,
                cancellationToken).ConfigureAwait(false);
    }

    public Task<WorkspaceDocumentImportResult> ImportFromHostPathAsync(
        string sourcePath,
        CancellationToken cancellationToken)
        => ImportFromHostPathAsync(
            RequireWritableBinding(),
            sourcePath,
            cancellationToken);

    public async Task<WorkspaceDocumentRelinkResult?> RelinkFromPickerAsync(
        string handle,
        CancellationToken cancellationToken)
    {
        WorkspaceDocumentBinding binding = RequireWritableBinding();
        DocumentCapabilityDescriptor descriptor = Resolve(
            handle,
            "relink",
            binding);
        string? source = await _filePicker.PickFileAsync(
            DocumentFilePickPurpose.RelinkMissing,
            Path.GetFileName(descriptor.RelativePath),
            cancellationToken).ConfigureAwait(false);
        if (string.IsNullOrWhiteSpace(source))
            return null;
        if (descriptor.EffectiveRevisionId is null)
            throw new DocumentFileOperationException(
                "文档没有可重关联的有效版本，请刷新文件列表。",
                "DOCUMENT_RELINK_INVALID");
        FileDocumentV2 document = await ForwardFileMutationAsync(
            binding,
            RelinkDocumentMethod,
            source,
            "file-relink",
            new
            {
                documentId = descriptor.DocumentId.ToString("D"),
                expectedEffectiveRevisionId =
                    descriptor.EffectiveRevisionId.Value.ToString("D"),
            },
            cancellationToken).ConfigureAwait(false);
        if (document.DocumentId != descriptor.DocumentId ||
            document.RelativePath != descriptor.RelativePath)
            throw InvalidMutationResult();
        return new WorkspaceDocumentRelinkResult(
            document.DocumentId,
            document.WorkspaceId,
            Path.GetFileName(document.RelativePath));
    }

    public void RotateCapabilityEpoch() => _capabilities.RotateEpoch();

    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;
        _capabilities.RotateEpoch();
        _preview.Dispose();
    }

    private async Task<FileDocumentV2[]> ReadDocumentsAsync(
        WorkspaceDocumentBinding binding,
        CancellationToken cancellationToken)
    {
        if (!binding.RpcMethods.Contains(
                ListDocumentsMethod,
                StringComparer.Ordinal))
            throw new DocumentFileOperationException(
                "当前 Sidecar 未提供文档列表能力。",
                "DOCUMENT_LIST_UNAVAILABLE");

        string requestId = $"desktop-documents-{Guid.NewGuid():N}";
        Guid operationId = Guid.NewGuid();
        ulong sequence = checked((ulong)Interlocked.Increment(ref _sequence));
        JsonElement wire = JsonSerializer.SerializeToElement(
            new
            {
                scope = "workspace",
                operationId = operationId.ToString("D"),
                sequence,
                workspaceId = binding.WorkspaceId.ToString("D"),
                sessionEpoch = binding.SessionEpoch,
            });
        JsonElement parameters = JsonSerializer.SerializeToElement(
            new { includeDeleted = false });
        WorkspaceV2ForwardResult response = await binding.Gateway.ForwardAsync(
            requestId,
            ListDocumentsMethod,
            wire,
            parameters,
            pathGrant: null,
            cancellationToken).ConfigureAwait(false);
        if (response.Error is not null)
            throw new DocumentFileOperationException(
                "Sidecar 未能读取文档列表。",
                response.Error.Code);
        JsonElement result = response.Result
            ?? throw InvalidList();
        RequireExactProperties(result, "documents");
        JsonElement source = result.GetProperty("documents");
        if (source.ValueKind != JsonValueKind.Array ||
            source.GetArrayLength() > 10_000)
            throw InvalidList();

        var documents = new List<FileDocumentV2>(source.GetArrayLength());
        foreach (JsonElement item in source.EnumerateArray())
        {
            FileDocumentV2 document;
            try
            {
                document = WorkspaceV2Json.DeserializeStrict<FileDocumentV2>(
                    item.GetRawText());
            }
            catch (JsonException)
            {
                throw InvalidList();
            }
            if (document.WorkspaceId != binding.WorkspaceId ||
                document.Status != FileDocumentStatus.Active)
                throw InvalidList();
            _ = ResolveWorkspacePath(binding.Root, document.RelativePath);
            documents.Add(document);
        }
        return documents.ToArray();
    }

    private DocumentBridgeEntry CreateEntry(
        WorkspaceDocumentBinding binding,
        FileDocumentV2 document)
    {
        string fullPath = ResolveWorkspacePath(
            binding.Root,
            document.RelativePath);
        bool available = File.Exists(fullPath) &&
            IsSafeMaterializedPath(binding.Root, fullPath);
        var granted = new List<string> { "history" };
        string previewKind = "none";
        if (available)
        {
            granted.AddRange(["open", "reveal", "dragOut"]);
            if (binding.Writable &&
                document.EffectiveRevisionId is not null)
                granted.Add("unlink");
            if (_preview.CanPreview(fullPath))
            {
                granted.Add("preview");
                previewKind = "system";
            }
        }
        else if (binding.Writable)
        {
            granted.Add("relink");
        }
        string handle = _capabilities.Issue(
            binding.WorkspaceId,
            binding.SessionEpoch,
            document.DocumentId,
            document.RelativePath,
            document.EffectiveRevisionId,
            granted);
        string? currentRevision =
            document.EffectiveRevisionId is not null &&
            document.NextFormalVersion > 1
                ? $"V{document.NextFormalVersion - 1}"
                : null;
        return new DocumentBridgeEntry(
            document.DocumentId.ToString("D"),
            handle,
            Path.GetFileName(document.RelativePath),
            MimeTypeFor(document.RelativePath),
            available ? "available" : "missing",
            previewKind,
            currentRevision,
            document.EffectiveRevisionId?.ToString("D"),
            "workspace",
            granted);
    }

    private async Task<WorkspaceDocumentImportResult> ImportFromHostPathAsync(
        WorkspaceDocumentBinding binding,
        string sourcePath,
        CancellationToken cancellationToken)
    {
        string source = ValidateSourceFile(sourcePath);
        string relative = Path.GetFileName(source);
        if (string.IsNullOrWhiteSpace(relative) ||
            relative is "." or "..")
            throw new DocumentFileOperationException(
                "文件名无效。",
                "DOCUMENT_SOURCE_INVALID");
        FileDocumentV2 document = await ForwardFileMutationAsync(
            binding,
            ImportDocumentMethod,
            source,
            "file-import",
            new
            {
                relativePath = relative,
                mimeType = MimeTypeFor(source) ?? "application/octet-stream",
            },
            cancellationToken).ConfigureAwait(false);
        return new WorkspaceDocumentImportResult(
            document.WorkspaceId,
            document.RelativePath);
    }

    private async Task<FileDocumentV2> ForwardFileMutationAsync(
        WorkspaceDocumentBinding binding,
        string method,
        string sourcePath,
        string purpose,
        object parametersWithoutGrant,
        CancellationToken cancellationToken)
    {
        if (!binding.RpcMethods.Contains(method, StringComparer.Ordinal))
            throw new DocumentFileOperationException(
                "当前 Sidecar 未提供所需的文件写入能力。",
                "DOCUMENT_MUTATION_UNAVAILABLE");
        string source = ValidateSourceFile(sourcePath);
        Guid operationId = Guid.NewGuid();
        string grantId = $"host-path-grant://{Guid.NewGuid():D}";
        using JsonDocument parametersDocument = JsonDocument.Parse(
            JsonSerializer.Serialize(parametersWithoutGrant));
        var parameterMap = parametersDocument.RootElement.EnumerateObject()
            .ToDictionary(
                property => property.Name,
                property => property.Value.Clone(),
                StringComparer.Ordinal);
        parameterMap.Add(
            "pathGrant",
            JsonSerializer.SerializeToElement(grantId));
        JsonElement parameters = JsonSerializer.SerializeToElement(parameterMap);
        JsonElement wire = CreateWire(binding, operationId);
        WorkspaceV2ForwardResult response = await binding.Gateway.ForwardAsync(
            $"desktop-file-{operationId:N}",
            method,
            wire,
            parameters,
            new WorkspaceSidecarPathGrant(
                grantId,
                method,
                operationId,
                purpose,
                source),
            cancellationToken).ConfigureAwait(false);
        if (response.Error is not null)
            throw new DocumentFileOperationException(
                "Sidecar 未能完成文件写入。",
                response.Error.Code);
        try
        {
            FileDocumentV2 document =
                WorkspaceV2Json.DeserializeStrict<FileDocumentV2>(
                    response.Result?.GetRawText()
                    ?? throw InvalidMutationResult());
            if (document.WorkspaceId != binding.WorkspaceId ||
                document.Status != FileDocumentStatus.Active)
                throw InvalidMutationResult();
            _ = ResolveWorkspacePath(binding.Root, document.RelativePath);
            return document;
        }
        catch (JsonException)
        {
            throw InvalidMutationResult();
        }
    }

    private JsonElement CreateWire(
        WorkspaceDocumentBinding binding,
        Guid operationId)
        => JsonSerializer.SerializeToElement(
            new
            {
                scope = "workspace",
                operationId = operationId.ToString("D"),
                sequence = checked(
                    (ulong)Interlocked.Increment(ref _sequence)),
                workspaceId = binding.WorkspaceId.ToString("D"),
                sessionEpoch = binding.SessionEpoch,
            });

    private string ResolveExisting(string handle, string capability)
    {
        WorkspaceDocumentBinding binding = RequireBinding();
        DocumentCapabilityDescriptor descriptor = Resolve(
            handle,
            capability,
            binding);
        string path = ResolveWorkspacePath(binding.Root, descriptor.RelativePath);
        if (!File.Exists(path) ||
            !IsSafeMaterializedPath(binding.Root, path))
            throw new DocumentFileOperationException(
                "文件不存在或已不安全，请刷新文件列表。",
                "DOCUMENT_MISSING");
        return path;
    }

    private DocumentCapabilityDescriptor Resolve(
        string handle,
        string capability,
        WorkspaceDocumentBinding binding)
        => _capabilities.Resolve(
            handle,
            capability,
            binding.WorkspaceId,
            binding.SessionEpoch);

    private WorkspaceDocumentBinding RequireBinding()
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
        WorkspaceDocumentBinding? binding = _bindingProvider();
        if (binding is null ||
            binding.WorkspaceId == Guid.Empty ||
            binding.SessionEpoch == 0)
            throw new DocumentFileOperationException(
                "文档工作区尚未连接。",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
        return binding;
    }

    private WorkspaceDocumentBinding RequireWritableBinding()
    {
        WorkspaceDocumentBinding binding = RequireBinding();
        if (!binding.Writable)
            throw new DocumentFileOperationException(
                "当前工作区为只读，不能修改文件。",
                "workspace.read_only");
        return binding;
    }

    internal static string ResolveWorkspacePath(
        string root,
        string relativePath)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(root);
        ArgumentException.ThrowIfNullOrWhiteSpace(relativePath);
        if (Path.IsPathRooted(relativePath))
            throw InvalidList();
        string normalizedRoot = Path.GetFullPath(
            Path.Combine(root, "files"));
        string fullPath = Path.GetFullPath(
            Path.Combine(normalizedRoot, relativePath));
        string prefix = normalizedRoot.TrimEnd(
            Path.DirectorySeparatorChar,
            Path.AltDirectorySeparatorChar) + Path.DirectorySeparatorChar;
        string canonical = relativePath.Replace('\\', '/');
        if (!fullPath.StartsWith(prefix, StringComparison.OrdinalIgnoreCase) ||
            canonical.StartsWith("/", StringComparison.Ordinal) ||
            canonical.Split('/').Any(part =>
                string.IsNullOrWhiteSpace(part) || part is "." or ".."))
            throw InvalidList();
        return fullPath;
    }

    private static string ValidateSourceFile(string sourcePath)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(sourcePath);
        string source = Path.GetFullPath(sourcePath);
        if (!File.Exists(source) || IsReparsePoint(source))
            throw new DocumentFileOperationException(
                "选择的文件不存在或不安全。",
                "DOCUMENT_SOURCE_INVALID");
        return source;
    }

    private static bool IsSafeMaterializedPath(
        string workspaceRoot,
        string fullPath)
    {
        try
        {
            string filesRoot = Path.GetFullPath(
                Path.Combine(workspaceRoot, "files"));
            if (!Directory.Exists(filesRoot))
                return false;
            string candidate = Path.GetFullPath(fullPath);
            if (IsReparsePoint(filesRoot))
                return false;
            string relative = Path.GetRelativePath(filesRoot, candidate);
            string cursor = filesRoot;
            foreach (string part in relative.Split(
                         [Path.DirectorySeparatorChar,
                          Path.AltDirectorySeparatorChar],
                         StringSplitOptions.RemoveEmptyEntries))
            {
                cursor = Path.Combine(cursor, part);
                if ((File.Exists(cursor) || Directory.Exists(cursor))
                    && IsReparsePoint(cursor))
                {
                    return false;
                }
            }
            return true;
        }
        catch (Exception exception) when (
            exception is IOException or
                UnauthorizedAccessException)
        {
            return false;
        }
    }

    private static bool IsReparsePoint(string path)
        => File.GetAttributes(path).HasFlag(FileAttributes.ReparsePoint);

    private static string? MimeTypeFor(string path)
        => Path.GetExtension(path).ToLowerInvariant() switch
        {
            ".pdf" => "application/pdf",
            ".docx" => "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            ".xlsx" => "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
            ".pptx" => "application/vnd.openxmlformats-officedocument.presentationml.presentation",
            ".txt" => "text/plain",
            ".csv" => "text/csv",
            ".png" => "image/png",
            ".jpg" or ".jpeg" => "image/jpeg",
            _ => null,
        };

    private static void RequireExactProperties(
        JsonElement value,
        params string[] expected)
    {
        if (value.ValueKind != JsonValueKind.Object)
            throw InvalidList();
        string[] actual = value.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        if (!actual.SequenceEqual(
                expected.Order(StringComparer.Ordinal),
                StringComparer.Ordinal))
            throw InvalidList();
    }

    private static DocumentFileOperationException InvalidList()
        => new(
            "Sidecar 返回了无效的文档列表。",
            "DOCUMENT_LIST_INVALID");

    private static DocumentFileOperationException InvalidMutationResult()
        => new(
            "Sidecar 返回了无效的文件写入结果。",
            "DOCUMENT_MUTATION_INVALID");
}

public sealed record WorkspaceDocumentBinding(
    Guid WorkspaceId,
    ulong SessionEpoch,
    bool Writable,
    string Root,
    WorkspaceV2HttpGateway Gateway,
    IReadOnlyCollection<string> RpcMethods);

public sealed record WorkspaceDocumentImportResult(
    Guid WorkspaceId,
    string RelativePath);

public sealed record WorkspaceDocumentRelinkResult(
    Guid DocumentId,
    Guid WorkspaceId,
    string DisplayName);

public sealed record DocumentBridgeEntry(
    string DocumentId,
    string EntryHandle,
    string DisplayName,
    string? MimeType,
    string Availability,
    string PreviewKind,
    string? CurrentRevision,
    string? EffectiveRevisionId,
    string LinkType,
    IReadOnlyList<string> Capabilities);

public sealed record DocumentListPayload(
    string? Collection,
    string? ItemId,
    IReadOnlyList<DocumentBridgeEntry> Entries);
