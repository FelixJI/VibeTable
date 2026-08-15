using System.IO;
using System.Text.Json;
using System.Text.Json.Serialization;
using VibeTable.Contracts;
using VibeTable.Workspace.Diff;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Native file-system adapter for documents owned by the active workspace-v2
/// Sidecar. It keeps no document topology: every UUID/path pair is obtained
/// from fileHistory.queryDocuments and wrapped in an expiring session handle.
/// </summary>
public sealed class WorkspaceDocumentOsAdapter : IWorkspaceDocumentCommands, IDisposable
{
    public const string QueryDocumentsMethod = "fileHistory.queryDocuments";
    public const string ImportDocumentMethod = "fileHistory.import";
    public const string RelinkDocumentMethod = "fileHistory.relink";
    public const string MaterializeDiffPairMethod =
        "fileHistory.materializeDiffPair";
    public const string AssertEffectiveRevisionMethod =
        "fileHistory.assertEffectiveRevision";

    private readonly Func<WorkspaceDocumentBinding?> _bindingProvider;
    private readonly DocumentCapabilityStore _capabilities;
    private readonly ILocalDocumentActions _actions;
    private readonly ILocalDocumentPreview _preview;
    private readonly ILocalDocumentFilePicker _filePicker;
    private readonly IWorkspaceHostEpochLeaseSource? _epochLeaseSource;
    private readonly WorkspaceDocumentDiffCoordinator? _diffCoordinator;
    private long _sequence;
    private bool _disposed;

    public WorkspaceDocumentOsAdapter(
        Func<WorkspaceDocumentBinding?> bindingProvider,
        DocumentCapabilityStore capabilities,
        ILocalDocumentActions actions,
        ILocalDocumentPreview preview,
        ILocalDocumentFilePicker filePicker)
        : this(
            bindingProvider,
            capabilities,
            actions,
            preview,
            filePicker,
            epochLeaseSource: null,
            diffEngine: null,
            diffTempRoot: null)
    {
    }

    internal WorkspaceDocumentOsAdapter(
        Func<WorkspaceDocumentBinding?> bindingProvider,
        DocumentCapabilityStore capabilities,
        ILocalDocumentActions actions,
        ILocalDocumentPreview preview,
        ILocalDocumentFilePicker filePicker,
        IWorkspaceHostEpochLeaseSource? epochLeaseSource,
        IDocumentDiffEngine? diffEngine,
        string? diffTempRoot)
    {
        _bindingProvider = bindingProvider
            ?? throw new ArgumentNullException(nameof(bindingProvider));
        _capabilities = capabilities
            ?? throw new ArgumentNullException(nameof(capabilities));
        _actions = actions ?? throw new ArgumentNullException(nameof(actions));
        _preview = preview ?? throw new ArgumentNullException(nameof(preview));
        _filePicker = filePicker
            ?? throw new ArgumentNullException(nameof(filePicker));
        _epochLeaseSource = epochLeaseSource;
        if (diffEngine is not null && !string.IsNullOrWhiteSpace(diffTempRoot))
        {
            if (epochLeaseSource is null)
                throw new ArgumentException(
                    "Document diff requires a workspace epoch lease source.");
            _diffCoordinator = new WorkspaceDocumentDiffCoordinator(
                epochLeaseSource,
                diffEngine,
                diffTempRoot);
        }
        else if (diffEngine is not null || diffTempRoot is not null)
        {
            throw new ArgumentException(
                "Document diff dependencies must be configured together.");
        }
    }

    public async Task<DocumentListPayload> ListGlobalAsync(
        CancellationToken cancellationToken,
        DocumentQueryInput? query = null)
    {
        WorkspaceDocumentBinding binding = RequireBinding();
        DocumentQueryPage documents = await ReadDocumentsAsync(
            binding,
            query ?? DocumentQueryInput.Default,
            cancellationToken).ConfigureAwait(false);
        if (query?.Cursor is null)
            _capabilities.RevokeAll();
        DocumentBridgeEntry[] entries = documents.Documents
            .Select(document => CreateEntry(binding, document))
            .ToArray();
        return new DocumentListPayload(
            null,
            null,
            entries,
            documents.NextCursor,
            documents.TopologyRevision);
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
            new DocumentListPayload(collection, itemId, [], null, 0));
    }

    public void Open(string handle)
        => _actions.Open(ResolveExisting(handle, "open"));

    public void Reveal(string handle)
        => _actions.Reveal(ResolveExisting(handle, "reveal"));

    public void Preview(string handle)
        => _preview.Show(ResolveExisting(handle, "preview"));

    public string ResolveDragOutPath(string handle)
        => ResolveExisting(handle, "dragOut");

    public Task<DocumentDiffPayload> CompareAsync(
        string handle,
        string historicalRevisionId,
        string expectedEffectiveRevisionId,
        CancellationToken cancellationToken)
    {
        WorkspaceDocumentBinding binding = RequireBinding();
        DocumentCapabilityDescriptor descriptor = Resolve(
            handle,
            "diff",
            binding);
        if (_diffCoordinator is null)
            throw new DocumentFileOperationException(
                "当前桌面宿主未提供文档比较能力。",
                "DOCUMENT_DIFF_UNAVAILABLE");
        return _diffCoordinator.CompareAsync(
            binding,
            descriptor,
            handle,
            historicalRevisionId,
            expectedEffectiveRevisionId,
            cancellationToken);
    }

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

    internal bool MatchesScope(WorkspaceWireScope? scope)
    {
        if (_disposed || scope is null || scope.Scope != "workspace")
            return false;
        WorkspaceDocumentBinding? binding = _bindingProvider();
        return binding is not null
            && binding.WorkspaceId == scope.WorkspaceId
            && binding.SessionEpoch == scope.SessionEpoch;
    }

    public void Dispose()
    {
        if (_disposed)
            return;
        _disposed = true;
        _capabilities.RotateEpoch();
        _preview.Dispose();
    }

    private async Task<DocumentQueryPage> ReadDocumentsAsync(
        WorkspaceDocumentBinding binding,
        DocumentQueryInput query,
        CancellationToken cancellationToken)
    {
        if (!binding.RpcMethods.Contains(
                QueryDocumentsMethod,
                StringComparer.Ordinal))
            throw new DocumentFileOperationException(
                "当前 Sidecar 未提供文档列表能力。",
                "DOCUMENT_LIST_UNAVAILABLE");

        string requestId = $"desktop-documents-{Guid.NewGuid():N}";
        Guid operationId = Guid.NewGuid();
        using WorkspaceRequestEpochLease? lease = CaptureEpochLease(
            binding,
            operationId);
        using CancellationTokenSource? linkedCancellation = LinkCancellation(
            lease,
            cancellationToken);
        JsonElement wire = CreateWire(binding, operationId, lease);
        JsonElement parameters = JsonSerializer.SerializeToElement(query);
        WorkspaceV2ForwardResult response = await binding.Gateway.ForwardAsync(
            requestId,
            QueryDocumentsMethod,
            wire,
            parameters,
            pathGrant: null,
            linkedCancellation?.Token ?? cancellationToken).ConfigureAwait(false);
        RequireCurrentLease(lease);
        if (response.Error is not null)
            throw new DocumentFileOperationException(
                "Sidecar 未能读取文档列表。",
                response.Error.Code);
        JsonElement result = response.Result
            ?? throw InvalidList();
        RequireExactProperties(result, "documents", "nextCursor", "topologyRevision");
        JsonElement source = result.GetProperty("documents");
        if (source.ValueKind != JsonValueKind.Array || source.GetArrayLength() > 500 ||
            result.GetProperty("topologyRevision").ValueKind != JsonValueKind.Number ||
            !result.GetProperty("topologyRevision").TryGetUInt64(out ulong topologyRevision))
            throw InvalidList();

        var documents = new List<FileDocumentSummaryV2>(source.GetArrayLength());
        foreach (JsonElement item in source.EnumerateArray())
        {
            FileDocumentSummaryV2 document;
            try
            {
                document = WorkspaceV2Json.DeserializeStrict<FileDocumentSummaryV2>(
                    item.GetRawText());
            }
            catch (JsonException)
            {
                throw InvalidList();
            }
            _ = ResolveWorkspacePath(binding.Root, document.RelativePath);
            documents.Add(document);
        }
        JsonElement cursorValue = result.GetProperty("nextCursor");
        string? nextCursor = cursorValue.ValueKind switch
        {
            JsonValueKind.Null => null,
            JsonValueKind.String => cursorValue.GetString(),
            _ => throw InvalidList(),
        };
        return new DocumentQueryPage(documents, nextCursor, topologyRevision);
    }

    private DocumentBridgeEntry CreateEntry(
        WorkspaceDocumentBinding binding,
        FileDocumentSummaryV2 document)
    {
        string fullPath = ResolveWorkspacePath(
            binding.Root,
            document.RelativePath);
        bool active = document.Status == FileDocumentStatus.Active;
        bool available = active && File.Exists(fullPath) &&
            IsSafeMaterializedPath(binding.Root, fullPath);
        var granted = new List<string> { "history" };
        if (active && _diffCoordinator is not null &&
            binding.RpcMethods.Contains(
                MaterializeDiffPairMethod,
                StringComparer.Ordinal) &&
            binding.RpcMethods.Contains(
                AssertEffectiveRevisionMethod,
                StringComparer.Ordinal))
        {
            granted.Add("diff");
        }
        string previewKind = "none";
        if (available)
        {
            granted.AddRange(["open", "reveal", "dragOut"]);
            if (binding.Writable)
                granted.Add("unlink");
            if (_preview.CanPreview(fullPath))
            {
                granted.Add("preview");
                previewKind = "system";
            }
        }
        else if (active && binding.Writable)
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
        string? currentRevision = document.FormalVersion is null
            ? null
            : $"V{document.FormalVersion}";
        return new DocumentBridgeEntry(
            document.DocumentId.ToString("D"),
            handle,
            document.RelativePath,
            document.DisplayName,
            document.Extension,
            document.MimeType,
            document.SizeBytes,
            document.EffectiveRevisionCreatedAt,
            document.FormalVersion,
            document.Status.ToString().ToLowerInvariant(),
            available ? "available" : "missing",
            previewKind,
            currentRevision,
            document.EffectiveRevisionId.ToString("D"),
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
        using WorkspaceRequestEpochLease? lease = CaptureEpochLease(
            binding,
            operationId);
        using CancellationTokenSource? linkedCancellation = LinkCancellation(
            lease,
            cancellationToken);
        JsonElement wire = CreateWire(binding, operationId, lease);
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
            linkedCancellation?.Token ?? cancellationToken).ConfigureAwait(false);
        RequireCurrentLease(lease);
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
        Guid operationId,
        WorkspaceRequestEpochLease? lease)
    {
        WorkspaceWireScope? scope = lease?.Scope;
        return JsonSerializer.SerializeToElement(
            new
            {
                scope = "workspace",
                operationId = (scope?.OperationId ?? operationId).ToString("D"),
                sequence = scope?.Sequence ?? checked(
                    (ulong)Interlocked.Increment(ref _sequence)),
                workspaceId = (scope?.WorkspaceId ?? binding.WorkspaceId).ToString("D"),
                sessionEpoch = scope?.SessionEpoch ?? binding.SessionEpoch,
            });
    }

    private WorkspaceRequestEpochLease? CaptureEpochLease(
        WorkspaceDocumentBinding binding,
        Guid operationId)
    {
        if (_epochLeaseSource is null)
            return null;
        if (!_epochLeaseSource.TryCaptureHost(
                binding.WorkspaceId,
                binding.SessionEpoch,
                operationId,
                out WorkspaceRequestEpochLease? lease) ||
            lease is null)
            throw new DocumentFileOperationException(
                "工作区会话已切换，请刷新后重试。",
                "DOCUMENT_SESSION_STALE");
        return lease;
    }

    private static CancellationTokenSource? LinkCancellation(
        WorkspaceRequestEpochLease? lease,
        CancellationToken cancellationToken)
        => lease is null
            ? null
            : CancellationTokenSource.CreateLinkedTokenSource(
                cancellationToken,
                lease.CancellationToken);

    private void RequireCurrentLease(WorkspaceRequestEpochLease? lease)
    {
        if (lease is not null && !_epochLeaseSource!.IsCurrent(lease))
            throw new DocumentFileOperationException(
                "工作区会话已切换，请刷新后重试。",
                "DOCUMENT_SESSION_STALE");
    }

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
    string RelativePath,
    string DisplayName,
    string Extension,
    string MimeType,
    ulong SizeBytes,
    DateTimeOffset EffectiveRevisionCreatedAt,
    ulong? FormalVersion,
    string Status,
    string Availability,
    string PreviewKind,
    string? CurrentRevision,
    string? EffectiveRevisionId,
    string LinkType,
    IReadOnlyList<string> Capabilities);

public sealed record DocumentListPayload(
    string? Collection,
    string? ItemId,
    IReadOnlyList<DocumentBridgeEntry> Entries,
    string? NextCursor,
    ulong TopologyRevision);

internal sealed record DocumentQueryPage(
    IReadOnlyList<FileDocumentSummaryV2> Documents,
    string? NextCursor,
    ulong TopologyRevision);

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record DocumentFilterInput
{
    [JsonPropertyName("field")]
    public required string Field { get; init; }

    [JsonPropertyName("operator")]
    public required string Operator { get; init; }

    [JsonPropertyName("value")]
    public required JsonElement Value { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record DocumentSortInput
{
    [JsonPropertyName("field")]
    public required string Field { get; init; }

    [JsonPropertyName("direction")]
    public required string Direction { get; init; }
}

[JsonUnmappedMemberHandling(JsonUnmappedMemberHandling.Disallow)]
public sealed record DocumentQueryInput
{
    public static DocumentQueryInput Default { get; } = new()
    {
        Logic = "and",
        Filters =
        [
            new DocumentFilterInput
            {
                Field = "status",
                Operator = "eq",
                Value = JsonSerializer.SerializeToElement("active"),
            },
        ],
        Sort =
        [
            new DocumentSortInput
            {
                Field = "effectiveRevisionCreatedAt",
                Direction = "desc",
            },
        ],
        Limit = 100,
        Cursor = null,
    };

    [JsonPropertyName("logic")]
    public required string Logic { get; init; }

    [JsonPropertyName("filters")]
    public required IReadOnlyList<DocumentFilterInput> Filters { get; init; }

    [JsonPropertyName("sort")]
    public required IReadOnlyList<DocumentSortInput> Sort { get; init; }

    [JsonPropertyName("limit")]
    public required int Limit { get; init; }

    [JsonPropertyName("cursor")]
    public required string? Cursor { get; init; }
}

public sealed record DocumentDiffPayload(
    string EntryHandle,
    string HistoricalRevisionId,
    string EffectiveRevisionId,
    string Outcome,
    int? AddedLines,
    int? RemovedLines,
    string? Failure);
