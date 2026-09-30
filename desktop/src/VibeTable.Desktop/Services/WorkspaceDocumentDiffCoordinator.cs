using System.Collections.Concurrent;
using System.IO;
using System.Text;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Workspace.Diff;

namespace VibeTable.Desktop.Services;

internal sealed class WorkspaceDocumentDiffCoordinator
{
    public const string HistoricalFileName = "historical.content";
    public const string EffectiveFileName = "effective.content";

    private readonly IWorkspaceHostEpochLeaseSource _epochLeaseSource;
    private readonly IDocumentDiffEngine _engine;
    private readonly DocumentDiffArtifactBroker _artifacts;
    private readonly string? _workerExecutablePath;
    private readonly ConcurrentDictionary<Guid, ReadySession> _sessions = [];
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);
    internal const int MaxPageBytes = 64 * 1024;

    public WorkspaceDocumentDiffCoordinator(
        IWorkspaceHostEpochLeaseSource epochLeaseSource,
        IDocumentDiffEngine engine,
        DocumentDiffArtifactBroker artifacts, string? workerExecutablePath = null)
    {
        _epochLeaseSource = epochLeaseSource
            ?? throw new ArgumentNullException(nameof(epochLeaseSource));
        _engine = engine ?? throw new ArgumentNullException(nameof(engine));
        _artifacts = artifacts ?? throw new ArgumentNullException(nameof(artifacts));
        _workerExecutablePath = workerExecutablePath;
        _artifacts.SessionClosed += id => _sessions.TryRemove(id, out _);
    }

    public async Task<DocumentDiffSessionResult> CompareAsync(
        WorkspaceDocumentBinding binding,
        DocumentCapabilityDescriptor descriptor,
        string entryHandle,
        string historicalRevisionId,
        string expectedEffectiveRevisionId,
        CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(binding);
        ArgumentNullException.ThrowIfNull(descriptor);
        ArgumentException.ThrowIfNullOrWhiteSpace(entryHandle);
        if (!Guid.TryParse(historicalRevisionId, out Guid historicalId) ||
            historicalId == Guid.Empty ||
            !Guid.TryParse(expectedEffectiveRevisionId, out Guid expectedId) ||
            expectedId == Guid.Empty ||
            descriptor.EffectiveRevisionId != expectedId)
        {
            return Failure(
                entryHandle,
                historicalRevisionId,
                expectedEffectiveRevisionId,
                "stale");
        }

        Guid operationId = Guid.NewGuid();
        if (!_epochLeaseSource.TryCaptureHost(
                binding.WorkspaceId,
                binding.SessionEpoch,
                operationId,
                out WorkspaceRequestEpochLease? lease) ||
            lease is null)
        {
            return Failure(
                entryHandle,
                historicalRevisionId,
                expectedEffectiveRevisionId,
                "stale");
        }

        using (lease)
        using (var linkedCancellation = CancellationTokenSource.CreateLinkedTokenSource(
                   cancellationToken,
                   lease.CancellationToken))
        {
            linkedCancellation.CancelAfter(TimeSpan.FromSeconds(30));
            try
            {
                using DocumentDiffArtifactOperation artifacts = _artifacts.CreateOperation(
                    operationId,
                    binding.WorkspaceId,
                    binding.SessionEpoch);
                MaterializedDiffPair pair = await MaterializeAsync(
                    binding,
                    descriptor,
                    historicalId,
                    expectedId,
                    artifacts.InputDirectory,
                    lease,
                    linkedCancellation.Token).ConfigureAwait(false);
                await using DocumentDiffVerifiedInputLease inputs =
                    await artifacts.OpenVerifiedInputsAsync(
                    pair.HistoricalContentHash,
                    pair.EffectiveContentHash,
                    linkedCancellation.Token).ConfigureAwait(false);
                if (!_epochLeaseSource.IsCurrent(lease))
                    return Failure(entryHandle, historicalRevisionId,
                        expectedEffectiveRevisionId, "stale");

                string indexPath = artifacts.PrepareArtifact(DocumentDiffArtifactKind.ChangeIndex, "changes.jsonl");
                DocumentDiffDetails? workerDetails = null;
                long[]? workerOffsets = null;
                if (_workerExecutablePath is not null && (Path.GetExtension(descriptor.RelativePath)
                        .Equals(".docx", StringComparison.OrdinalIgnoreCase) ||
                        pair.EffectiveMimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document"))
                    (workerDetails, workerOffsets) = await CompareDocxAsync(artifacts, indexPath,
                        linkedCancellation.Token).ConfigureAwait(false);
                DocumentDiffOutcome outcome = workerDetails is not null
                    ? DocumentDiffOutcome.Changed with { Details = workerDetails }
                    : await _engine.CompareAsync(
                    new DocumentDiffRequest(
                        ContentSource(
                            descriptor.RelativePath,
                            pair.HistoricalMimeType,
                            artifacts.HistoricalInputPath),
                        ContentSource(
                            descriptor.RelativePath,
                            pair.EffectiveMimeType,
                            artifacts.EffectiveInputPath)),
                    linkedCancellation.Token).ConfigureAwait(false);
                if (outcome.Kind == DocumentDiffOutcomeKind.Failure)
                {
                    return Failure(
                        entryHandle,
                        historicalRevisionId,
                        expectedEffectiveRevisionId,
                        outcome.Failure == DocumentDiffFailureKind.Cancelled
                            ? !_epochLeaseSource.IsCurrent(lease) ? "stale"
                                : cancellationToken.IsCancellationRequested ? "cancelled" : "timeout"
                            : FailureName(outcome.Failure));
                }

                DocumentDiffSessionResult? assertionFailure = await AssertEffectiveAsync(
                    binding,
                    descriptor.DocumentId,
                    historicalId,
                    pair.HistoricalContentHash,
                    expectedId,
                    pair.EffectiveContentHash,
                    entryHandle,
                    historicalRevisionId,
                    lease,
                    linkedCancellation.Token).ConfigureAwait(false);
                if (assertionFailure is not null)
                    return assertionFailure;
                inputs.ConfirmSourceStable();
                DocumentDiffDetails details = outcome.Details ?? ShallowDetails(outcome,
                    descriptor.RelativePath, pair.EffectiveMimeType);
                var offsets = new List<long>();
                if (workerOffsets is not null) offsets.AddRange(workerOffsets);
                if (workerOffsets is null)
                await using (var index = new FileStream(indexPath, FileMode.CreateNew, FileAccess.Write, FileShare.None))
                {
                    foreach (DocumentDiffChange change in details.Changes)
                    {
                        linkedCancellation.Token.ThrowIfCancellationRequested();
                        byte[] line = JsonSerializer.SerializeToUtf8Bytes(change, JsonOptions);
                        if (line.Length + 1 > MaxPageBytes - 1024) throw new JsonException("Diff change exceeds page budget.");
                        offsets.Add(index.Position);
                        await index.WriteAsync(line, linkedCancellation.Token).ConfigureAwait(false);
                        await index.WriteAsync("\n"u8.ToArray(), linkedCancellation.Token).ConfigureAwait(false);
                    }
                }
                // Recheck after writing the derived index; a ready session must still be current.
                assertionFailure = await AssertEffectiveAsync(binding, descriptor.DocumentId,
                    historicalId, pair.HistoricalContentHash, expectedId, pair.EffectiveContentHash,
                    entryHandle, historicalRevisionId, lease, linkedCancellation.Token).ConfigureAwait(false);
                if (assertionFailure is not null) return assertionFailure;
                linkedCancellation.Token.ThrowIfCancellationRequested();
                Guid sessionId = Guid.NewGuid();
                var session = CreateSession(sessionId, entryHandle, historicalId, expectedId, details);
                var ready = new ReadySession(binding.WorkspaceId, binding.SessionEpoch, descriptor.DocumentId,
                    pair, entryHandle, offsets.ToArray());
                _sessions[sessionId] = ready;
                try { artifacts.Complete(sessionId); }
                catch { _sessions.TryRemove(sessionId, out _); throw; }
                if (!_sessions.ContainsKey(sessionId) || !_epochLeaseSource.IsCurrent(lease) || linkedCancellation.IsCancellationRequested)
                {
                    CloseSession(sessionId);
                    return Failure(entryHandle, historicalRevisionId, expectedEffectiveRevisionId, "stale");
                }
                return DocumentDiffSessionResult.Ready(session);
            }
            catch (OperationCanceledException)
            {
                string failure = _epochLeaseSource.IsCurrent(lease)
                    ? cancellationToken.IsCancellationRequested ? "cancelled" : "timeout"
                    : "stale";
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, failure);
            }
            catch (TimeoutException)
            {
                return Failure(entryHandle, historicalRevisionId, expectedEffectiveRevisionId, "timeout");
            }
            catch (DocumentDiffSidecarException exception)
            {
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, exception.Failure);
            }
            catch (DocumentDiffArtifactStaleException)
            {
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, "stale");
            }
            catch (Exception exception) when (
                exception is IOException or UnauthorizedAccessException or JsonException)
            {
                return Failure(entryHandle, historicalRevisionId,
                    expectedEffectiveRevisionId, "io");
            }
        }
    }

    public async Task<DocumentDiffChangePageResult> ReadPageAsync(
        WorkspaceDocumentBinding binding, DocumentDiffChangePageRequest request,
        CancellationToken cancellationToken)
    {
        if (!_sessions.TryGetValue(request.SessionId, out ReadySession? session))
            return DocumentDiffChangePageResult.Failed(DocumentDiffPageFailure.SessionExpired);
        if (session.WorkspaceId != binding.WorkspaceId || session.SessionEpoch != binding.SessionEpoch ||
            !_epochLeaseSource.TryCaptureHost(binding.WorkspaceId, binding.SessionEpoch,
                Guid.NewGuid(), out WorkspaceRequestEpochLease? lease) || lease is null)
        {
            CloseSession(request.SessionId);
            return DocumentDiffChangePageResult.Failed(DocumentDiffPageFailure.Stale);
        }
        using (lease)
        using (var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, lease.CancellationToken))
        {
            linked.CancelAfter(TimeSpan.FromSeconds(30));
            try
            {
                int start = 0;
                if (request.Cursor is not null && !session.Cursors.TryGetValue(request.Cursor, out start))
                    return DocumentDiffChangePageResult.Failed(DocumentDiffPageFailure.InvalidCursor);
                await using DocumentDiffArtifactReadLease artifact = _artifacts.OpenRead(request.SessionId,
                    binding.WorkspaceId, binding.SessionEpoch, DocumentDiffArtifactKind.ChangeIndex);
                DocumentDiffSessionResult? stale = await AssertPageCurrentAsync(binding, session, lease, linked.Token)
                    .ConfigureAwait(false);
                if (stale is not null) return InvalidatePage(request.SessionId);
                var changes = new List<DocumentDiffChange>();
                int bytes = 0;
                if (start < session.Offsets.Length)
                {
                    artifact.Stream.Position = session.Offsets[start];
                    using var reader = new StreamReader(artifact.Stream, Encoding.UTF8, leaveOpen: true);
                    while (start + changes.Count < session.Offsets.Length && changes.Count < request.Limit)
                    {
                        string line = await reader.ReadLineAsync(linked.Token).ConfigureAwait(false)
                            ?? throw new JsonException("Incomplete change index.");
                        int size = Encoding.UTF8.GetByteCount(line);
                        if (bytes + size > MaxPageBytes - 1024 && changes.Count > 0) break;
                        bytes += size;
                        changes.Add(JsonSerializer.Deserialize<DocumentDiffChange>(line, JsonOptions)
                            ?? throw new JsonException("Invalid change index."));
                    }
                }
                stale = await AssertPageCurrentAsync(binding, session, lease, linked.Token).ConfigureAwait(false);
                if (stale is not null || !_sessions.ContainsKey(request.SessionId))
                    return InvalidatePage(request.SessionId);
                int next = start + changes.Count;
                string? cursor = next < session.Offsets.Length
                    ? session.CursorNames.GetOrAdd(next, _ => Guid.NewGuid().ToString("D")) : null;
                if (cursor is not null) session.Cursors[cursor] = next;
                return DocumentDiffChangePageResult.Ready(request, request.SessionId, changes, cursor);
            }
            catch (DocumentDiffArtifactUnavailableException)
            {
                CloseSession(request.SessionId);
                return DocumentDiffChangePageResult.Failed(DocumentDiffPageFailure.SessionExpired);
            }
            catch (OperationCanceledException)
            {
                CloseSession(request.SessionId);
                return DocumentDiffChangePageResult.Failed(_epochLeaseSource.IsCurrent(lease)
                    ? cancellationToken.IsCancellationRequested ? DocumentDiffPageFailure.Cancelled
                        : DocumentDiffPageFailure.Timeout : DocumentDiffPageFailure.Stale);
            }
            catch (Exception exception) when (exception is IOException or UnauthorizedAccessException or JsonException)
            {
                CloseSession(request.SessionId);
                throw;
            }
        }
    }

    private Task<DocumentDiffSessionResult?> AssertPageCurrentAsync(WorkspaceDocumentBinding binding,
        ReadySession session, WorkspaceRequestEpochLease lease, CancellationToken token)
        => AssertEffectiveAsync(binding, session.DocumentId, session.Pair.HistoricalRevisionId,
            session.Pair.HistoricalContentHash, session.Pair.EffectiveRevisionId, session.Pair.EffectiveContentHash,
            session.EntryHandle, session.Pair.HistoricalRevisionId.ToString("D"), lease, token);

    private DocumentDiffChangePageResult InvalidatePage(Guid sessionId)
    {
        CloseSession(sessionId);
        return DocumentDiffChangePageResult.Failed(DocumentDiffPageFailure.Stale);
    }

    public void CloseSession(Guid sessionId)
    {
        _sessions.TryRemove(sessionId, out _);
        _artifacts.CloseSession(sessionId);
    }

    public void CloseAllSessions()
    {
        foreach (Guid id in _sessions.Keys) CloseSession(id);
    }

    private static DocumentDiffSession CreateSession(Guid id, string handle, Guid historical,
        Guid effective, DocumentDiffDetails details)
    {
        int Count(DocumentDiffChangeKind kind) => details.Changes.Count(change => change.Kind == kind);
        var summary = details.Summary ?? new DocumentDiffSummary(details.Changes.Count, details.Changes.Count,
            Count(DocumentDiffChangeKind.Insert), Count(DocumentDiffChangeKind.Delete),
            Count(DocumentDiffChangeKind.Replace), Count(DocumentDiffChangeKind.Move),
            Count(DocumentDiffChangeKind.Format), Count(DocumentDiffChangeKind.Table),
            Count(DocumentDiffChangeKind.Comment), Count(DocumentDiffChangeKind.Other));
        var warnings = details.Warnings.Distinct().ToList();
        if (details.Coverage.Truncated && !warnings.Contains(DocumentDiffWarning.ResultTruncated))
            warnings.Add(DocumentDiffWarning.ResultTruncated);
        if (details.Coverage.Areas.Any(area => area.Status != DocumentDiffCoverageStatus.Covered)
            && !warnings.Contains(DocumentDiffWarning.PartialCoverage))
            warnings.Add(DocumentDiffWarning.PartialCoverage);
        return new DocumentDiffSession(id, handle, historical, effective, details.Format,
            details.Format == DocumentDiffFormat.Xlsx ? DocumentDiffProvider.XlsxBuiltIn : DocumentDiffProvider.BuiltIn,
            DocumentDiffFidelity.Structural, summary, details.Coverage, warnings, false, false);
    }

    private async Task<(DocumentDiffDetails Details, long[] Offsets)> CompareDocxAsync(
        DocumentDiffArtifactOperation artifacts, string indexPath, CancellationToken cancellationToken)
    {
        if (!File.Exists(_workerExecutablePath)) throw new DocumentDiffSidecarException("providerUnavailable");
        int exit;
        try
        {
            exit = await DocumentDiffWorkerSupervisor.RunAsync(
                new System.Diagnostics.ProcessStartInfo(_workerExecutablePath!), artifacts.OperationDirectory,
                TimeSpan.FromSeconds(30), cancellationToken, compareDocx: true).ConfigureAwait(false);
        }
        catch (DocumentDiffWorkerExitUnknownException)
        {
            artifacts.RetainArtifacts();
            throw;
        }
        if (exit != 0) throw new DocumentDiffSidecarException(exit switch
        {
            2 => "invalidContent", 3 or 4 => "unsupported", _ => "io",
        });
        return await ReadWorkerResultAsync(artifacts.IndexDirectory, indexPath, cancellationToken)
            .ConfigureAwait(false);
    }

    internal static async Task<(DocumentDiffDetails Details, long[] Offsets)> ReadWorkerResultAsync(
        string indexDirectory, string indexPath, CancellationToken cancellationToken)
    {
        try
        {
            string resultPath = Path.Combine(indexDirectory, "result.json");
            foreach (string path in new[] { resultPath, indexPath })
                if ((File.GetAttributes(path) & (FileAttributes.ReparsePoint | FileAttributes.Directory)) != 0)
                    throw new IOException("Unsafe worker result.");
            DocumentDiffWorkerResult result;
            await using (var metadata = new FileStream(resultPath, FileMode.Open, FileAccess.Read, FileShare.Read))
            {
                if (metadata.Length > 256 * 1024) throw new JsonException("Oversized worker metadata.");
                result = await JsonSerializer.DeserializeAsync<DocumentDiffWorkerResult>(metadata, JsonOptions,
                    cancellationToken).ConfigureAwait(false) ?? throw new JsonException("Missing worker metadata.");
            }
            if (result.Version != 2 || result.Summary is null || result.Coverage is null || result.Warnings is null ||
                result.Summary.TotalChangeGroups > 20_000 || result.Summary.RawRevisionCount > 500_000)
                throw new JsonException("Invalid worker metadata.");
            var changes = new List<DocumentDiffChange>();
            var offsets = new List<long>();
            var ids = new HashSet<Guid>();
            long position = 0;
            await using (var index = new FileStream(indexPath, FileMode.Open, FileAccess.Read, FileShare.Read))
            {
                if (index.Length > 64L * 1024 * 1024) throw new JsonException("Oversized change index.");
                using var reader = new StreamReader(index, new UTF8Encoding(false, true), leaveOpen: true);
                while (await reader.ReadLineAsync(cancellationToken).ConfigureAwait(false) is { } line)
                {
                    int size = Encoding.UTF8.GetByteCount(line) + 1;
                    if (size > MaxPageBytes - 1024 || changes.Count == 20_000)
                        throw new JsonException("Worker change exceeds the page budget.");
                    DocumentDiffChange change = JsonSerializer.Deserialize<DocumentDiffChange>(line, JsonOptions)
                        ?? throw new JsonException("Missing worker change.");
                    if (!ids.Add(change.ChangeId)) throw new JsonException("Duplicate worker change.");
                    foreach (DocumentDiffRichSnippet? snippet in new[] { change.Before, change.After })
                        if (snippet is not null && (snippet.Runs.Sum(run => (long)run.Text.Length) > 2048 ||
                            snippet.Runs.Where(run => run.Role == DocumentDiffRichRunRole.Context)
                                .Sum(run => (long)run.Text.Length) > 256))
                            throw new JsonException("Worker snippet exceeds its text budget.");
                    offsets.Add(position);
                    position += size;
                    changes.Add(change);
                }
                if (position != index.Length) throw new JsonException("Invalid change-index framing.");
            }
            int Count(DocumentDiffChangeKind kind) => changes.Count(change => change.Kind == kind);
            DocumentDiffSummary summary = result.Summary;
            if (summary.TotalChangeGroups != changes.Count || summary.Insertions != Count(DocumentDiffChangeKind.Insert) ||
                summary.Deletions != Count(DocumentDiffChangeKind.Delete) || summary.Replacements != Count(DocumentDiffChangeKind.Replace) ||
                summary.Moves != Count(DocumentDiffChangeKind.Move) || summary.FormattingChanges != Count(DocumentDiffChangeKind.Format) ||
                summary.TableChanges != Count(DocumentDiffChangeKind.Table) || summary.CommentChanges != Count(DocumentDiffChangeKind.Comment) ||
                summary.OtherChanges != Count(DocumentDiffChangeKind.Other))
                throw new JsonException("Worker summary does not match its change index.");
            return (new DocumentDiffDetails(DocumentDiffFormat.Docx, changes.AsReadOnly(), result.Coverage)
                { Summary = summary, Warnings = result.Warnings }, offsets.ToArray());
        }
        catch (ArgumentException exception)
        {
            // Includes JSON constructor validation and strict UTF-8 decoding failures.
            throw new JsonException("Invalid Worker result.", exception);
        }
    }

    private static DocumentDiffDetails ShallowDetails(DocumentDiffOutcome outcome, string name, string mime)
    {
        bool identical = outcome.Kind == DocumentDiffOutcomeKind.Identical;
        string extension = Path.GetExtension(name).ToLowerInvariant();
        var format = extension == ".docx" ? DocumentDiffFormat.Docx
            : extension == ".xlsx" ? DocumentDiffFormat.Xlsx
            : DocumentDiffEngine.IsText(new DocumentContentSource(name, mime, null,
                _ => throw new InvalidOperationException())) ? DocumentDiffFormat.Text : DocumentDiffFormat.Binary;
        DocumentDiffChange[] changes = identical ? [] : [new(Guid.NewGuid(), DocumentDiffChangeKind.Other,
            new DocumentDiffLocation(DocumentDiffPart.Body), null, null, DocumentDiffConfidence.Exact)];
        DocumentDiffCoverageArea[] uncovered = format switch
        {
            DocumentDiffFormat.Docx => [DocumentDiffCoverageArea.Structure, DocumentDiffCoverageArea.Formatting,
                DocumentDiffCoverageArea.Tables, DocumentDiffCoverageArea.HeadersFooters, DocumentDiffCoverageArea.Images],
            DocumentDiffFormat.Xlsx => [DocumentDiffCoverageArea.WorksheetValues, DocumentDiffCoverageArea.WorksheetFormulas,
                DocumentDiffCoverageArea.WorksheetStyles, DocumentDiffCoverageArea.WorksheetMerges,
                DocumentDiffCoverageArea.WorksheetVisibility],
            DocumentDiffFormat.Binary => [DocumentDiffCoverageArea.VisibleText],
            _ => [],
        };
        var areas = new List<DocumentDiffCoverageEntry>();
        if (format is DocumentDiffFormat.Text or DocumentDiffFormat.Docx)
            areas.Add(new(DocumentDiffCoverageArea.VisibleText, identical ? DocumentDiffCoverageStatus.Covered
                : DocumentDiffCoverageStatus.NotCovered));
        areas.AddRange(uncovered.Select(area => new DocumentDiffCoverageEntry(area, DocumentDiffCoverageStatus.NotCovered)));
        return new DocumentDiffDetails(format, changes, new DocumentDiffCoverage(areas, truncated: false));
    }

    private sealed record ReadySession(Guid WorkspaceId, ulong SessionEpoch, Guid DocumentId,
        MaterializedDiffPair Pair, string EntryHandle, long[] Offsets)
    {
        public ConcurrentDictionary<string, int> Cursors { get; } = new(StringComparer.Ordinal);
        public ConcurrentDictionary<int, string> CursorNames { get; } = [];
    }

    private async Task<MaterializedDiffPair> MaterializeAsync(
        WorkspaceDocumentBinding binding,
        DocumentCapabilityDescriptor descriptor,
        Guid historicalRevisionId,
        Guid expectedEffectiveRevisionId,
        string destination,
        WorkspaceRequestEpochLease lease,
        CancellationToken cancellationToken)
    {
        string grantId = $"host-path-grant://{Guid.NewGuid():D}";
        JsonElement parameters = JsonSerializer.SerializeToElement(new
        {
            documentId = descriptor.DocumentId.ToString("D"),
            historicalRevisionId = historicalRevisionId.ToString("D"),
            expectedEffectiveRevisionId = expectedEffectiveRevisionId.ToString("D"),
            pathGrant = grantId,
        });
        WorkspaceV2ForwardResult response = await binding.Gateway.ForwardAsync(
            $"desktop-diff-{lease.Scope.OperationId:N}",
            WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
            Wire(lease.Scope),
            parameters,
            new WorkspaceSidecarPathGrant(
                grantId,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                lease.Scope.OperationId,
                "document-diff-materialize",
                destination),
            cancellationToken).ConfigureAwait(false);
        if (response.Error is not null)
            throw SidecarFailure(response.Error.Code);
        JsonElement result = response.Result
            ?? throw new JsonException("Missing materialized diff result.");
        RequireExactProperties(result,
            "documentId",
            "historicalRevisionId",
            "effectiveRevisionId",
            "historicalMimeType",
            "effectiveMimeType",
            "historicalContentHash",
            "effectiveContentHash");
        var pair = new MaterializedDiffPair(
            RequiredGuid(result, "documentId"),
            RequiredGuid(result, "historicalRevisionId"),
            RequiredGuid(result, "effectiveRevisionId"),
            RequiredString(result, "historicalMimeType"),
            RequiredString(result, "effectiveMimeType"),
            RequiredString(result, "historicalContentHash"),
            RequiredString(result, "effectiveContentHash"));
        if (pair.DocumentId != descriptor.DocumentId ||
            pair.HistoricalRevisionId != historicalRevisionId ||
            pair.EffectiveRevisionId != expectedEffectiveRevisionId ||
            !File.Exists(Path.Combine(destination, "historical.content")) ||
            !File.Exists(Path.Combine(destination, "effective.content")))
            throw new JsonException("Materialized diff identity is invalid.");
        return pair;
    }

    private async Task<DocumentDiffSessionResult?> AssertEffectiveAsync(
        WorkspaceDocumentBinding binding,
        Guid documentId,
        Guid historicalRevisionId,
        string expectedHistoricalContentHash,
        Guid expectedEffectiveRevisionId,
        string expectedEffectiveContentHash,
        string entryHandle,
        string historicalRevisionIdText,
        WorkspaceRequestEpochLease operationLease,
        CancellationToken cancellationToken)
    {
        if (!_epochLeaseSource.TryCaptureHost(
                binding.WorkspaceId,
                binding.SessionEpoch,
                Guid.NewGuid(),
                out WorkspaceRequestEpochLease? assertionLease) ||
            assertionLease is null)
        {
            return Failure(entryHandle, historicalRevisionIdText,
                expectedEffectiveRevisionId.ToString("D"), "stale");
        }
        using (assertionLease)
        {
            WorkspaceV2ForwardResult response = await binding.Gateway.ForwardAsync(
                $"desktop-diff-assert-{assertionLease.Scope.OperationId:N}",
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
                Wire(assertionLease.Scope),
                JsonSerializer.SerializeToElement(new
                {
                    documentId = documentId.ToString("D"),
                    historicalRevisionId = historicalRevisionId.ToString("D"),
                    expectedHistoricalContentHash,
                    expectedEffectiveRevisionId =
                        expectedEffectiveRevisionId.ToString("D"),
                    expectedEffectiveContentHash,
                }),
                pathGrant: null,
                cancellationToken).ConfigureAwait(false);
            if (response.Error is not null)
                return Failure(entryHandle, historicalRevisionIdText,
                    expectedEffectiveRevisionId.ToString("D"),
                    MapSidecarFailure(response.Error.Code));
            JsonElement result = response.Result
                ?? throw new JsonException("Missing revision assertion result.");
            RequireExactProperties(
                result,
                "documentId",
                "historicalRevisionId",
                "effectiveRevisionId",
                "historicalContentHash",
                "effectiveContentHash",
                "stable");
            if (RequiredGuid(result, "documentId") != documentId ||
                RequiredGuid(result, "historicalRevisionId") != historicalRevisionId ||
                RequiredGuid(result, "effectiveRevisionId") !=
                    expectedEffectiveRevisionId ||
                !string.Equals(
                    RequiredString(result, "historicalContentHash"),
                    expectedHistoricalContentHash,
                    StringComparison.Ordinal) ||
                !string.Equals(
                    RequiredString(result, "effectiveContentHash"),
                    expectedEffectiveContentHash,
                    StringComparison.Ordinal) ||
                result.GetProperty("stable").ValueKind != JsonValueKind.True ||
                !_epochLeaseSource.IsCurrent(operationLease) ||
                !_epochLeaseSource.IsCurrent(assertionLease))
            {
                return Failure(entryHandle, historicalRevisionIdText,
                    expectedEffectiveRevisionId.ToString("D"), "stale");
            }
            return null;
        }
    }

    private static DocumentContentSource ContentSource(
        string name,
        string mimeType,
        string path)
    {
        var info = new FileInfo(path);
        return new DocumentContentSource(
            name,
            mimeType,
            info.Length,
            _ => ValueTask.FromResult<Stream>(new FileStream(
                path,
                FileMode.Open,
                FileAccess.Read,
                FileShare.Read,
                64 * 1024,
                FileOptions.Asynchronous | FileOptions.SequentialScan)));
    }

    private static JsonElement Wire(WorkspaceWireScope scope)
        => JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            workspaceId = scope.WorkspaceId.ToString("D"),
            sessionEpoch = scope.SessionEpoch,
            operationId = scope.OperationId.ToString("D"),
            sequence = scope.Sequence,
        });

    private static void RequireExactProperties(
        JsonElement value,
        params string[] expected)
    {
        if (value.ValueKind != JsonValueKind.Object)
            throw new JsonException("Diff result must be an object.");
        string[] actual = value.EnumerateObject()
            .Select(property => property.Name)
            .Order(StringComparer.Ordinal)
            .ToArray();
        if (!actual.SequenceEqual(
                expected.Order(StringComparer.Ordinal),
                StringComparer.Ordinal))
            throw new JsonException("Diff result shape is invalid.");
    }

    private static Guid RequiredGuid(JsonElement value, string property)
    {
        if (!value.TryGetProperty(property, out JsonElement element) ||
            element.ValueKind != JsonValueKind.String ||
            !Guid.TryParse(element.GetString(), out Guid result) ||
            result == Guid.Empty)
            throw new JsonException($"{property} is invalid.");
        return result;
    }

    private static string RequiredString(JsonElement value, string property)
    {
        if (!value.TryGetProperty(property, out JsonElement element) ||
            element.ValueKind != JsonValueKind.String ||
            string.IsNullOrWhiteSpace(element.GetString()))
            throw new JsonException($"{property} is invalid.");
        return element.GetString()!;
    }

    private static DocumentDiffSidecarException SidecarFailure(string code)
        => new(MapSidecarFailure(code));

    internal static string MapSidecarFailure(string code)
        => code == "filehistory.effective_revision_stale"
            ? "stale"
            : "io";

    private static string FailureName(DocumentDiffFailureKind? failure)
        => failure switch
        {
            DocumentDiffFailureKind.Unsupported => "unsupported",
            DocumentDiffFailureKind.InvalidContent => "invalidContent",
            DocumentDiffFailureKind.Io => "io",
            DocumentDiffFailureKind.Cancelled => "cancelled",
            _ => "io",
        };

    private static DocumentDiffSessionResult Failure(
        string entryHandle,
        string historicalRevisionId,
        string effectiveRevisionId,
        string failure)
        => DocumentDiffSessionResult.Failed(failure switch
        {
            "stale" => DocumentDiffSessionFailure.Stale,
            "cancelled" => DocumentDiffSessionFailure.Cancelled,
            "unsupported" => DocumentDiffSessionFailure.Unsupported,
            "invalidContent" => DocumentDiffSessionFailure.InvalidContent,
            "timeout" => DocumentDiffSessionFailure.Timeout,
            "providerUnavailable" => DocumentDiffSessionFailure.ProviderUnavailable,
            _ => DocumentDiffSessionFailure.Io,
        });

    private sealed record MaterializedDiffPair(
        Guid DocumentId,
        Guid HistoricalRevisionId,
        Guid EffectiveRevisionId,
        string HistoricalMimeType,
        string EffectiveMimeType,
        string HistoricalContentHash,
        string EffectiveContentHash);

    private sealed class DocumentDiffSidecarException(string failure)
        : Exception
    {
        public string Failure { get; } = failure;
    }
}
