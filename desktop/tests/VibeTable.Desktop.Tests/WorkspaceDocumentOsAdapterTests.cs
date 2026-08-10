using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Workspace.Diff;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceDocumentOsAdapterTests
{
    private const string DiffOperationId =
        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    private static readonly Guid WorkspaceId =
        Guid.Parse("11111111-1111-4111-8111-111111111111");
    private static readonly Guid DocumentId =
        Guid.Parse("22222222-2222-4222-8222-222222222222");
    private static readonly Guid RevisionId =
        Guid.Parse("33333333-3333-4333-8333-333333333333");

    [TestMethod]
    public void DiffCoordinatorMapsThePublicFileHistoryStaleCode()
    {
        Assert.AreEqual(
            "stale",
            WorkspaceDocumentDiffCoordinator.MapSidecarFailure(
                "filehistory.effective_revision_stale"));
        Assert.AreEqual(
            "io",
            WorkspaceDocumentDiffCoordinator.MapSidecarFailure(
                "workspace.internal_failed"));
    }

    [TestMethod]
    public void CanonicalRelativePathIsResolvedFromFilesRoot()
    {
        string root = Path.Combine(Path.GetTempPath(), Guid.NewGuid().ToString("N"));

        string resolved = WorkspaceDocumentOsAdapter.ResolveWorkspacePath(
            root,
            "reports/q3.txt");

        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(root, "files", "reports", "q3.txt")),
            resolved);
        Assert.ThrowsExactly<DocumentFileOperationException>(() =>
            WorkspaceDocumentOsAdapter.ResolveWorkspacePath(root, "../escape.txt"));
        Assert.ThrowsExactly<DocumentFileOperationException>(() =>
            WorkspaceDocumentOsAdapter.ResolveWorkspacePath(
                root,
                Path.GetFullPath(Path.Combine(root, "absolute.txt"))));
    }

    [TestMethod]
    public async Task ImportUsesOneShotSidecarGrantAndNeverCopiesInDesktop()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string source = Path.Combine(directory.Path, "report.txt");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        await File.WriteAllTextAsync(source, "authoritative input");
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            Assert.AreEqual(
                WorkspaceDocumentOsAdapter.ImportDocumentMethod,
                root.GetProperty("method").GetString());
            JsonElement parameters = root.GetProperty("params");
            Assert.AreEqual(
                "report.txt",
                parameters.GetProperty("relativePath").GetString());
            Assert.AreEqual(
                "text/plain",
                parameters.GetProperty("mimeType").GetString());
            string grantId = parameters.GetProperty("pathGrant").GetString()!;
            Assert.IsTrue(grantId.StartsWith(
                "host-path-grant://",
                StringComparison.Ordinal));
            using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
            Assert.AreEqual(grantId, grant.RootElement
                .GetProperty("grantId").GetString());
            Assert.AreEqual("file-import", grant.RootElement
                .GetProperty("purpose").GetString());
            Assert.AreEqual(
                Path.GetFullPath(source),
                grant.RootElement.GetProperty("path").GetString());
            Assert.IsFalse(File.Exists(
                Path.Combine(workspaceRoot, "files", "report.txt")));
            return RpcSuccess(root, FileDocument("report.txt", 2));
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [WorkspaceDocumentOsAdapter.ImportDocumentMethod]);

        WorkspaceDocumentImportResult result =
            await adapter.ImportFromHostPathAsync(
                source,
                CancellationToken.None);

        Assert.AreEqual(WorkspaceId, result.WorkspaceId);
        Assert.AreEqual("report.txt", result.RelativePath);
        Assert.IsFalse(File.Exists(
            Path.Combine(workspaceRoot, "files", "report.txt")));
    }

    [TestMethod]
    public async Task ListAndImportUseTheSharedHostEpochSequenceAndReleaseEveryLease()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string source = Path.Combine(directory.Path, "report.txt");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        await File.WriteAllTextAsync(source, "authoritative input");
        var sequences = new List<ulong>();
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            sequences.Add(root.GetProperty("wire").GetProperty("sequence").GetUInt64());
            return root.GetProperty("method").GetString() ==
                WorkspaceDocumentOsAdapter.ListDocumentsMethod
                    ? RpcSuccess(root, "{\"documents\":[]}")
                    : RpcSuccess(root, FileDocument("report.txt", 2));
        });
        var epochs = new FakeEpochLeaseSource(initialSequence: 100);
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.ListDocumentsMethod,
                WorkspaceDocumentOsAdapter.ImportDocumentMethod,
            ],
            epochs);

        await adapter.ListGlobalAsync(CancellationToken.None);
        await adapter.ImportFromHostPathAsync(source, CancellationToken.None);

        CollectionAssert.AreEqual(new ulong[] { 101, 102 }, sequences);
        Assert.AreEqual(2, epochs.LeaseCount);
        Assert.AreEqual(2, epochs.CompletedLeaseCount);
    }

    [TestMethod]
    public async Task ListProjectsFormalVersionSeparatelyFromEffectiveUuid()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string materialized = Path.Combine(
            workspaceRoot,
            "files",
            "reports",
            "q3.txt");
        Directory.CreateDirectory(Path.GetDirectoryName(materialized)!);
        await File.WriteAllTextAsync(materialized, "current leaf");
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            return RpcSuccess(
                root,
                $$"""{"documents":[{{FileDocument("reports/q3.txt", 4)}}]}""");
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [WorkspaceDocumentOsAdapter.ListDocumentsMethod]);

        DocumentListPayload result =
            await adapter.ListGlobalAsync(CancellationToken.None);

        DocumentBridgeEntry entry = result.Entries.Single();
        Assert.AreEqual("V3", entry.CurrentRevision);
        Assert.AreEqual(RevisionId.ToString("D"), entry.EffectiveRevisionId);
        Assert.AreEqual("available", entry.Availability);
        Assert.AreEqual("q3.txt", entry.DisplayName);
    }

    [TestMethod]
    public async Task DiffCapabilityMaterializesThroughHostGrantAndCleansEveryFile()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string materialized = Path.Combine(
            workspaceRoot,
            "files",
            "reports",
            "q3.txt");
        string diffRoot = Path.Combine(directory.Path, "diff-sessions");
        Directory.CreateDirectory(Path.GetDirectoryName(materialized)!);
        await File.WriteAllTextAsync(materialized, "current leaf");
        var engine = new InspectingDiffEngine();
        var epochs = new FakeEpochLeaseSource();
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            string method = root.GetProperty("method").GetString()!;
            if (method == WorkspaceDocumentOsAdapter.ListDocumentsMethod)
            {
                return RpcSuccess(
                    root,
                    $$"""{"documents":[{{FileDocument("reports/q3.txt", 4)}}]}""");
            }
            if (method == WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod)
            {
                using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
                string destination = grant.RootElement.GetProperty("path").GetString()!;
                File.WriteAllText(
                    Path.Combine(
                        destination,
                        WorkspaceDocumentDiffCoordinator.HistoricalFileName),
                    "before");
                File.WriteAllText(
                    Path.Combine(
                        destination,
                        WorkspaceDocumentDiffCoordinator.EffectiveFileName),
                    "after");
                return RpcSuccess(
                    root,
                    $$"""
                    {
                      "documentId":"{{DocumentId:D}}",
                      "historicalRevisionId":"44444444-4444-4444-8444-444444444444",
                      "effectiveRevisionId":"{{RevisionId:D}}",
                      "historicalMimeType":"text/plain",
                      "effectiveMimeType":"text/plain"
                    }
                    """);
            }
            Assert.AreEqual(
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
                method);
            return RpcSuccess(
                root,
                $$"""
                {
                  "documentId":"{{DocumentId:D}}",
                  "effectiveRevisionId":"{{RevisionId:D}}",
                  "stable":true
                }
                """);
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.ListDocumentsMethod,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
            ],
            epochs,
            engine,
            diffRoot);
        DocumentBridgeEntry entry = (await adapter.ListGlobalAsync(
            CancellationToken.None)).Entries.Single();

        Assert.IsTrue(entry.Capabilities.Contains("diff"));
        DocumentDiffPayload result = await adapter.CompareAsync(
            entry.EntryHandle,
            "44444444-4444-4444-8444-444444444444",
            RevisionId.ToString("D"),
            CancellationToken.None);

        Assert.AreEqual("changedWithDetails", result.Outcome);
        Assert.AreEqual(1, result.AddedLines);
        Assert.AreEqual(1, result.RemovedLines);
        Assert.AreEqual("before", engine.Before);
        Assert.AreEqual("after", engine.After);
        Assert.IsTrue(!Directory.Exists(diffRoot) ||
            Directory.GetFileSystemEntries(diffRoot).Length == 0);
        Assert.AreEqual(3, epochs.LeaseCount);
        Assert.AreEqual(3, epochs.CompletedLeaseCount);
    }

    [TestMethod]
    public async Task DispatcherCancelCooperativelyCancelsTheRunningDiffEngine()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string materialized = Path.Combine(
            workspaceRoot,
            "files",
            "reports",
            "q3.txt");
        string diffRoot = Path.Combine(directory.Path, "diff-sessions");
        Directory.CreateDirectory(Path.GetDirectoryName(materialized)!);
        await File.WriteAllTextAsync(materialized, "current leaf");
        var engine = new BlockingDiffEngine();
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            string method = root.GetProperty("method").GetString()!;
            if (method == WorkspaceDocumentOsAdapter.ListDocumentsMethod)
            {
                return RpcSuccess(
                    root,
                    $$"""{"documents":[{{FileDocument("reports/q3.txt", 4)}}]}""");
            }
            Assert.AreEqual(
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                method);
            using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
            string destination = grant.RootElement.GetProperty("path").GetString()!;
            File.WriteAllText(
                Path.Combine(destination, WorkspaceDocumentDiffCoordinator.HistoricalFileName),
                "before");
            File.WriteAllText(
                Path.Combine(destination, WorkspaceDocumentDiffCoordinator.EffectiveFileName),
                "after");
            return RpcSuccess(
                root,
                $$"""
                {
                  "documentId":"{{DocumentId:D}}",
                  "historicalRevisionId":"44444444-4444-4444-8444-444444444444",
                  "effectiveRevisionId":"{{RevisionId:D}}",
                  "historicalMimeType":"text/plain",
                  "effectiveMimeType":"text/plain"
                }
                """);
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.ListDocumentsMethod,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
            ],
            new FakeEpochLeaseSource(),
            engine,
            diffRoot);
        DocumentBridgeEntry entry = (await adapter.ListGlobalAsync(
            CancellationToken.None)).Entries.Single();
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"),
            sink);
        dispatcher.SetDocumentWorkspace(adapter);

        dispatcher.Dispatch(Request(
            "document.diffRequested",
            "diff-1",
            $$"""
            {
              "entryHandle":"{{entry.EntryHandle}}",
              "operationId":"{{DiffOperationId}}",
              "historicalRevisionId":"44444444-4444-4444-8444-444444444444",
              "expectedEffectiveRevisionId":"{{RevisionId:D}}"
            }
            """));
        await engine.Started.Task.WaitAsync(TimeSpan.FromSeconds(2));
        dispatcher.Dispatch(Request(
            "document.diffCancelRequested",
            "cancel-1",
            $$"""
            {
              "entryHandle":"{{entry.EntryHandle}}",
              "operationId":"{{DiffOperationId}}"
            }
            """));

        FakeWebReplySink.Reply? cancel = await sink.WaitForAsync(
            "document.diffCancelCompleted");
        FakeWebReplySink.Reply? completed = await sink.WaitForAsync(
            "document.diffCompleted");
        Assert.IsNotNull(cancel);
        Assert.AreEqual("cancel-1", cancel.RequestId);
        Assert.IsTrue(((dynamic)cancel.Payload!).cancelled);
        Assert.IsNotNull(completed);
        Assert.AreEqual("diff-1", completed.RequestId);
        Assert.AreEqual("failure", ((DocumentDiffPayload)completed.Payload!).Outcome);
        Assert.AreEqual("cancelled", ((DocumentDiffPayload)completed.Payload!).Failure);
        Assert.IsTrue(engine.CancellationObserved);
        Assert.IsTrue(!Directory.Exists(diffRoot) ||
            Directory.GetFileSystemEntries(diffRoot).Length == 0);
    }

    [TestMethod]
    public async Task DispatcherCancelBeforeRegistrationIsAppliedToTheSameDiffOperation()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string materialized = Path.Combine(
            workspaceRoot,
            "files",
            "reports",
            "q3.txt");
        string diffRoot = Path.Combine(directory.Path, "diff-sessions");
        Directory.CreateDirectory(Path.GetDirectoryName(materialized)!);
        await File.WriteAllTextAsync(materialized, "current leaf");
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            string method = root.GetProperty("method").GetString()!;
            if (method == WorkspaceDocumentOsAdapter.ListDocumentsMethod)
            {
                return RpcSuccess(
                    root,
                    $$"""{"documents":[{{FileDocument("reports/q3.txt", 4)}}]}""");
            }
            Assert.AreEqual(
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                method);
            using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
            string destination = grant.RootElement.GetProperty("path").GetString()!;
            File.WriteAllText(
                Path.Combine(
                    destination,
                    WorkspaceDocumentDiffCoordinator.HistoricalFileName),
                "before");
            File.WriteAllText(
                Path.Combine(
                    destination,
                    WorkspaceDocumentDiffCoordinator.EffectiveFileName),
                "after");
            return RpcSuccess(
                root,
                $$"""
                {
                  "documentId":"{{DocumentId:D}}",
                  "historicalRevisionId":"44444444-4444-4444-8444-444444444444",
                  "effectiveRevisionId":"{{RevisionId:D}}",
                  "historicalMimeType":"text/plain",
                  "effectiveMimeType":"text/plain"
                }
                """);
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.ListDocumentsMethod,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
            ],
            new FakeEpochLeaseSource(),
            new BlockingDiffEngine(),
            diffRoot);
        DocumentBridgeEntry entry = (await adapter.ListGlobalAsync(
            CancellationToken.None)).Entries.Single();
        var sink = new FakeWebReplySink();
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"),
            sink);
        dispatcher.SetDocumentWorkspace(adapter);

        dispatcher.Dispatch(Request(
            "document.diffRequested",
            "diff-after-cancel",
            $$"""
            {
              "entryHandle":"{{entry.EntryHandle}}",
              "operationId":"{{DiffOperationId}}",
              "historicalRevisionId":"44444444-4444-4444-8444-444444444444",
              "expectedEffectiveRevisionId":"{{RevisionId:D}}"
            }
            """));
        dispatcher.Dispatch(Request(
            "document.diffCancelRequested",
            "cancel-before-registration",
            $$"""
            {
              "entryHandle":"{{entry.EntryHandle}}",
              "operationId":"{{DiffOperationId}}"
            }
            """));

        FakeWebReplySink.Reply? cancel = await sink.WaitForAsync(
            "document.diffCancelCompleted");
        FakeWebReplySink.Reply? completed = await sink.WaitForAsync(
            "document.diffCompleted");
        Assert.IsNotNull(cancel);
        Assert.IsTrue(((dynamic)cancel.Payload!).cancelled);
        Assert.IsNotNull(completed);
        Assert.AreEqual(
            "cancelled",
            ((DocumentDiffPayload)completed.Payload!).Failure);
        Assert.IsTrue(!Directory.Exists(diffRoot) ||
            Directory.GetFileSystemEntries(diffRoot).Length == 0);
    }

    private static WorkspaceDocumentOsAdapter Adapter(
        string workspaceRoot,
        WorkspaceV2HttpGateway gateway,
        IReadOnlyCollection<string> methods,
        IWorkspaceHostEpochLeaseSource? epochs = null,
        IDocumentDiffEngine? engine = null,
        string? diffRoot = null)
    {
        var binding = new WorkspaceDocumentBinding(
            WorkspaceId,
            7,
            true,
            workspaceRoot,
            gateway,
            methods);
        return new WorkspaceDocumentOsAdapter(
            () => binding,
            new DocumentCapabilityStore(),
            new NoopActions(),
            new NoopPreview(),
            new NoopPicker(),
            epochs,
            engine,
            diffRoot);
    }

    private static WorkspaceV2HttpGateway Gateway(HttpMessageHandler handler)
        => new(
            () => new PocketBaseAdminContext(
                new Uri(
                    "http://127.0.0.1:43125/api/vibetable/v1/admin/bootstrap"),
                new Uri("http://127.0.0.1:43125/"),
                "X-VibeTable-Session",
                "private-secret"),
            handler);

    private static string FileDocument(string relativePath, ulong nextFormalVersion)
        => $$"""
        {
          "contractVersion":"2.0",
          "documentId":"{{DocumentId:D}}",
          "workspaceId":"{{WorkspaceId:D}}",
          "relativePath":"{{relativePath}}",
          "status":"active",
          "effectiveRevisionId":"{{RevisionId:D}}",
          "nextRevisionOrdinal":{{nextFormalVersion}},
          "nextFormalVersion":{{nextFormalVersion}}
        }
        """;

    private static HttpResponseMessage RpcSuccess(
        JsonElement request,
        string resultJson)
        => Json(
            "{\"jsonrpc\":\"2.0\",\"id\":"
            + JsonSerializer.Serialize(request.GetProperty("id").GetString())
            + ",\"wire\":"
            + request.GetProperty("wire").GetRawText()
            + ",\"result\":"
            + resultJson
            + "}");

    private static string DecodeGrant(HttpRequestMessage request)
    {
        string encoded = request.Headers
            .GetValues("X-VibeTable-Path-Grant")
            .Single()
            .Replace('-', '+')
            .Replace('_', '/');
        encoded += new string('=', (4 - encoded.Length % 4) % 4);
        return Encoding.UTF8.GetString(Convert.FromBase64String(encoded));
    }

    private static RoutedWebRequest Request(
        string type,
        string requestId,
        string payload)
    {
        using JsonDocument document = JsonDocument.Parse(payload);
        return new RoutedWebRequest(
            type,
            requestId,
            document.RootElement.Clone(),
            string.Empty);
    }

    private static HttpResponseMessage Json(string body)
        => new(HttpStatusCode.OK)
        {
            Content = new StringContent(body, Encoding.UTF8, "application/json"),
        };

    private sealed class RecordingHandler(
        Func<HttpRequestMessage, HttpResponseMessage> responder)
        : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
            => Task.FromResult(responder(request));
    }

    private sealed class NoopActions : ILocalDocumentActions
    {
        public void Open(string fullPath) { }
        public void Reveal(string fullPath) { }
    }

    private sealed class NoopPreview : ILocalDocumentPreview
    {
        public bool CanPreview(string fullPath) => false;
        public void Show(string fullPath) { }
        public void Dispose() { }
    }

    private sealed class NoopPicker : ILocalDocumentFilePicker
    {
        public Task<string?> PickFileAsync(
            DocumentFilePickPurpose purpose,
            string? suggestedFileName,
            CancellationToken token)
            => Task.FromResult<string?>(null);
    }

    private sealed class InspectingDiffEngine : IDocumentDiffEngine
    {
        public string? Before { get; private set; }
        public string? After { get; private set; }

        public async Task<DocumentDiffOutcome> CompareAsync(
            DocumentDiffRequest request,
            CancellationToken cancellationToken)
        {
            await using Stream before = await request.Before.OpenReadAsync(
                cancellationToken);
            await using Stream after = await request.After.OpenReadAsync(
                cancellationToken);
            using var beforeReader = new StreamReader(before);
            using var afterReader = new StreamReader(after);
            Before = await beforeReader.ReadToEndAsync(cancellationToken);
            After = await afterReader.ReadToEndAsync(cancellationToken);
            return DocumentDiffOutcome.ChangedWithDetails(1, 1);
        }
    }

    private sealed class BlockingDiffEngine : IDocumentDiffEngine
    {
        public TaskCompletionSource<bool> Started { get; } = new(
            TaskCreationOptions.RunContinuationsAsynchronously);

        public bool CancellationObserved { get; private set; }

        public async Task<DocumentDiffOutcome> CompareAsync(
            DocumentDiffRequest request,
            CancellationToken cancellationToken)
        {
            Started.TrySetResult(true);
            try
            {
                await Task.Delay(Timeout.InfiniteTimeSpan, cancellationToken);
            }
            finally
            {
                CancellationObserved = cancellationToken.IsCancellationRequested;
            }
            return DocumentDiffOutcome.Changed;
        }
    }

    private sealed class FakeEpochLeaseSource(
        ulong initialSequence = 0) : IWorkspaceHostEpochLeaseSource
    {
        private ulong _sequence = initialSequence;

        public int LeaseCount { get; private set; }
        public int CompletedLeaseCount { get; private set; }

        public bool TryCaptureHost(
            Guid workspaceId,
            ulong sessionEpoch,
            Guid operationId,
            out WorkspaceRequestEpochLease? lease)
        {
            LeaseCount++;
            lease = new WorkspaceRequestEpochLease(
                new WorkspaceWireScope
                {
                    Scope = "workspace",
                    WorkspaceId = workspaceId,
                    SessionEpoch = sessionEpoch,
                    OperationId = operationId,
                    Sequence = ++_sequence,
                },
                CancellationToken.None,
                () => CompletedLeaseCount++);
            return true;
        }

        public bool IsCurrent(WorkspaceRequestEpochLease? lease) => lease is not null;
    }

    private sealed class TemporaryDirectory : IDisposable
    {
        public TemporaryDirectory()
        {
            Path = System.IO.Path.Combine(
                System.IO.Path.GetTempPath(),
                "vibetable-desktop-tests",
                Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Path);
        }

        public string Path { get; }

        public void Dispose()
        {
            if (Directory.Exists(Path))
                Directory.Delete(Path, recursive: true);
        }
    }
}
