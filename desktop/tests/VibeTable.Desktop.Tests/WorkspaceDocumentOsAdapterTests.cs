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
    [TestMethod]
    public void DocumentControllerHandlesOnlyTheClosedBrowseAndDiffUnion()
    {
        foreach (string type in new[]
        {
            "document.listRequested",
            "document.openRequested",
            "document.revealRequested",
            "document.previewRequested",
            "document.diffRequested",
            "document.diffCancelRequested",
            "document.pickRequested",
            "document.relinkRequested",
        })
        {
            Assert.IsTrue(DocumentBrowseRequestController.Handles(type), type);
        }
        Assert.IsFalse(DocumentBrowseRequestController.Handles("document.rawRequested"));
        Assert.IsFalse(DocumentBrowseRequestController.Handles("file.uploadRequested"));
    }

    [TestMethod]
    public async Task DocumentListJoinsTheReplacementWorkspaceGenerationAfterTransportFailure()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        var started = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var staleHandler = new AsyncRecordingHandler(async (_, cancellationToken) =>
        {
            started.TrySetResult(true);
            await release.Task.WaitAsync(cancellationToken);
            throw new HttpRequestException("retired sidecar generation");
        });
        int intermediateCalls = 0;
        var intermediateHandler = new RecordingHandler(_ =>
        {
            intermediateCalls++;
            throw new AssertFailedException("an obsolete intermediate generation was used");
        });
        int replacementCalls = 0;
        var replacementHandler = new RecordingHandler(request =>
        {
            replacementCalls++;
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            return RpcSuccess(
                body.RootElement,
                "{\"documents\":[],\"nextCursor\":null,\"topologyRevision\":0}");
        });
        using WorkspaceV2HttpGateway staleGateway = Gateway(staleHandler);
        using WorkspaceV2HttpGateway intermediateGateway = Gateway(intermediateHandler);
        using WorkspaceV2HttpGateway replacementGateway = Gateway(replacementHandler);
        using WorkspaceDocumentOsAdapter stale = Adapter(
            workspaceRoot,
            staleGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        using WorkspaceDocumentOsAdapter intermediate = Adapter(
            workspaceRoot,
            intermediateGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        using WorkspaceDocumentOsAdapter replacement = Adapter(
            workspaceRoot,
            replacementGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        var sink = new FakeWebReplySink();
        var controller = new DocumentBrowseRequestController(
            sink,
            TimeSpan.FromSeconds(1));
        controller.SetWorkspace(stale);
        Task pending = controller.DispatchAsync(Request(
            "document.listRequested",
            "list-after-recovery",
            """
            {
              "authority":"workspace",
              "scope":{"kind":"global"}
            }
            """,
            WorkspaceScope()));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));

        controller.SetWorkspace(intermediate);
        controller.SetWorkspace(replacement);
        release.TrySetResult(true);
        await pending;

        FakeWebReplySink.Reply? loaded = await sink.WaitForAsync(
            "document.listLoaded");
        Assert.IsNotNull(loaded);
        Assert.AreEqual("list-after-recovery", loaded.RequestId);
        Assert.AreEqual(0, intermediateCalls);
        Assert.AreEqual(1, replacementCalls);
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task DocumentListJoinsOneReplacementGenerationWithoutWaitingForAnother()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        var started = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var staleHandler = new AsyncRecordingHandler((_, _) =>
        {
            started.TrySetResult(true);
            return Task.FromException<HttpResponseMessage>(
                new HttpRequestException("retired sidecar generation"));
        });
        var replacementHandler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            return RpcSuccess(
                body.RootElement,
                "{\"documents\":[],\"nextCursor\":null,\"topologyRevision\":0}");
        });
        using WorkspaceV2HttpGateway staleGateway = Gateway(staleHandler);
        using WorkspaceV2HttpGateway replacementGateway = Gateway(replacementHandler);
        using WorkspaceDocumentOsAdapter stale = Adapter(
            workspaceRoot,
            staleGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        using WorkspaceDocumentOsAdapter replacement = Adapter(
            workspaceRoot,
            replacementGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        var sink = new FakeWebReplySink();
        var controller = new DocumentBrowseRequestController(
            sink,
            TimeSpan.FromMilliseconds(250));
        controller.SetWorkspace(stale);
        Task pending = controller.DispatchAsync(Request(
            "document.listRequested",
            "list-after-one-replacement",
            """
            {
              "authority":"workspace",
              "scope":{"kind":"global"}
            }
            """,
            WorkspaceScope()));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));

        controller.SetWorkspace(replacement);
        await pending;

        Assert.IsNotNull(await sink.WaitForAsync("document.listLoaded"));
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task DocumentListRetriesOnlyOneReplacementGeneration()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        var initialStarted = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var replacementStarted = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseReplacement = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var initialHandler = new AsyncRecordingHandler((_, _) =>
        {
            initialStarted.TrySetResult(true);
            return Task.FromException<HttpResponseMessage>(
                new HttpRequestException("retired sidecar generation"));
        });
        int replacementCalls = 0;
        var replacementHandler = new AsyncRecordingHandler(async (_, cancellationToken) =>
        {
            replacementCalls++;
            replacementStarted.TrySetResult(true);
            await releaseReplacement.Task.WaitAsync(cancellationToken);
            throw new HttpRequestException("replacement generation is unavailable");
        });
        int laterCalls = 0;
        var laterHandler = new RecordingHandler(request =>
        {
            laterCalls++;
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            return RpcSuccess(
                body.RootElement,
                "{\"documents\":[],\"nextCursor\":null,\"topologyRevision\":0}");
        });
        using WorkspaceV2HttpGateway initialGateway = Gateway(initialHandler);
        using WorkspaceV2HttpGateway replacementGateway = Gateway(replacementHandler);
        using WorkspaceV2HttpGateway laterGateway = Gateway(laterHandler);
        using WorkspaceDocumentOsAdapter initial = Adapter(
            workspaceRoot,
            initialGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        using WorkspaceDocumentOsAdapter replacement = Adapter(
            workspaceRoot,
            replacementGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        using WorkspaceDocumentOsAdapter later = Adapter(
            workspaceRoot,
            laterGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        var sink = new FakeWebReplySink();
        var controller = new DocumentBrowseRequestController(
            sink,
            TimeSpan.FromSeconds(1));
        controller.SetWorkspace(initial);
        Task pending = controller.DispatchAsync(Request(
            "document.listRequested",
            "list-with-one-retry",
            """
            {
              "authority":"workspace",
              "scope":{"kind":"global"}
            }
            """,
            WorkspaceScope()));
        await initialStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));

        controller.SetWorkspace(replacement);
        await replacementStarted.Task.WaitAsync(TimeSpan.FromSeconds(2));
        controller.SetWorkspace(later);
        releaseReplacement.TrySetResult(true);
        await pending;

        FakeWebReplySink.Reply? failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("BACKEND_UNAVAILABLE", ((dynamic)failed.Payload!).code);
        Assert.AreEqual(1, replacementCalls);
        Assert.AreEqual(0, laterCalls);
        Assert.IsFalse(sink.Replies.Any(reply => reply.Type == "document.listLoaded"));
    }

    [TestMethod]
    public async Task DocumentListPreservesTheSafeWorkspaceUnavailableCode()
    {
        using var adapter = new WorkspaceDocumentOsAdapter(
            () => null,
            new DocumentCapabilityStore(),
            new NoopActions(),
            new NoopPreview(),
            new NoopPicker());
        var sink = new FakeWebReplySink();
        var controller = new DocumentBrowseRequestController(sink);
        controller.SetWorkspace(adapter);

        await controller.DispatchAsync(Request(
            "document.listRequested",
            "list-without-binding",
            """
            {
              "authority":"workspace",
              "scope":{"kind":"global"}
            }
            """,
            WorkspaceScope()));

        FakeWebReplySink.Reply? failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual(
            "DOCUMENT_WORKSPACE_UNAVAILABLE",
            ((dynamic)failed.Payload!).code);
    }

    [TestMethod]
    public async Task DocumentListDoesNotRecoverAcrossWorkspaceSessions()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        var started = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var staleHandler = new AsyncRecordingHandler(async (_, cancellationToken) =>
        {
            started.TrySetResult(true);
            await release.Task.WaitAsync(cancellationToken);
            throw new HttpRequestException("retired sidecar generation");
        });
        int replacementCalls = 0;
        var replacementHandler = new RecordingHandler(_ =>
        {
            replacementCalls++;
            throw new AssertFailedException("a stale request crossed workspace sessions");
        });
        using WorkspaceV2HttpGateway staleGateway = Gateway(staleHandler);
        using WorkspaceV2HttpGateway replacementGateway = Gateway(replacementHandler);
        using WorkspaceDocumentOsAdapter stale = Adapter(
            workspaceRoot,
            staleGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        Guid otherWorkspaceId = Guid.Parse(
            "99999999-9999-4999-8999-999999999999");
        var otherBinding = new WorkspaceDocumentBinding(
            otherWorkspaceId,
            8,
            true,
            workspaceRoot,
            replacementGateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);
        using var replacement = new WorkspaceDocumentOsAdapter(
            () => otherBinding,
            new DocumentCapabilityStore(),
            new NoopActions(),
            new NoopPreview(),
            new NoopPicker());
        var sink = new FakeWebReplySink();
        var controller = new DocumentBrowseRequestController(
            sink,
            TimeSpan.FromSeconds(1));
        controller.SetWorkspace(stale);
        Task pending = controller.DispatchAsync(Request(
            "document.listRequested",
            "list-across-session",
            """
            {
              "authority":"workspace",
              "scope":{"kind":"global"}
            }
            """,
            WorkspaceScope()));
        await started.Task.WaitAsync(TimeSpan.FromSeconds(2));

        controller.SetWorkspace(replacement);
        release.TrySetResult(true);
        await pending;

        FakeWebReplySink.Reply? failed = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failed);
        Assert.AreEqual("DOCUMENT_SESSION_STALE", ((dynamic)failed.Payload!).code);
        Assert.AreEqual(0, replacementCalls);
    }

    private const string DiffOperationId =
        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    private const string BeforeHash =
        "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb";
    private const string AfterHash =
        "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8";
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
                WorkspaceDocumentOsAdapter.QueryDocumentsMethod
                    ? RpcSuccess(root, "{\"documents\":[],\"nextCursor\":null,\"topologyRevision\":0}")
                    : RpcSuccess(root, FileDocument("report.txt", 2));
        });
        var epochs = new FakeEpochLeaseSource(initialSequence: 100);
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.QueryDocumentsMethod,
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
    [DataRow(false)]
    [DataRow(true)]
    public async Task ConcurrentRendererReadCannotOvertakeHostImport(bool cancelMiddle)
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string source = Path.Combine(directory.Path, "report.txt");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        await File.WriteAllTextAsync(source, "authoritative input");
        var entered = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        ulong highWatermark = 0;
        var handler = new AsyncRecordingHandler(async (request, token) =>
        {
            using JsonDocument body = JsonDocument.Parse(await request.Content!.ReadAsStringAsync(token));
            JsonElement root = body.RootElement;
            bool isImport = root.GetProperty("method").GetString() == WorkspaceDocumentOsAdapter.ImportDocumentMethod;
            if (isImport)
            {
                entered.TrySetResult(true);
                await release.Task.WaitAsync(token);
            }
            ulong sequence = root.GetProperty("wire").GetProperty("sequence").GetUInt64();
            // Match the Sidecar dispatcher's strict committed high-watermark.
            if (sequence <= highWatermark)
                return Json(JsonSerializer.Serialize(new
                {
                    jsonrpc = "2.0", id = root.GetProperty("id").GetString(),
                    wire = root.GetProperty("wire"),
                    error = new { code = "workspace.sequence_stale", message = "stale", retryable = false },
                }));
            highWatermark = sequence;
            return RpcSuccess(root, isImport ? FileDocument("report.txt", 2)
                : "{\"documents\":[],\"nextCursor\":null,\"topologyRevision\":0}");
        });
        var epochs = new FakeEpochLeaseSource(initialSequence: 100);
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(workspaceRoot, gateway,
            [WorkspaceDocumentOsAdapter.ImportDocumentMethod], epochs);
        Task<WorkspaceDocumentImportResult> import = adapter.ImportFromHostPathAsync(source, CancellationToken.None);
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(5));
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = "workspace", workspaceId = WorkspaceId, sessionEpoch = 7,
            operationId = Guid.NewGuid(), sequence = 1124,
        });
        using var cancelRead = new CancellationTokenSource();
        Task<WorkspaceV2ForwardResult> read = gateway.ForwardAsync("renderer-read",
            WorkspaceDocumentOsAdapter.QueryDocumentsMethod, wire,
            JsonSerializer.SerializeToElement(new { }), null, cancelRead.Token);
        if (cancelMiddle)
        {
            cancelRead.Cancel();
            Assert.IsTrue(read.IsCanceled, "Queued cancellation must be observed before the predecessor finishes.");
            read = gateway.ForwardAsync("renderer-after-cancel",
                WorkspaceDocumentOsAdapter.QueryDocumentsMethod, wire,
                JsonSerializer.SerializeToElement(new { }), null, CancellationToken.None);
            Assert.IsFalse(read.IsCompleted, "Cancellation must not let the successor overtake the import.");
        }
        release.TrySetResult(true);
        WorkspaceDocumentImportResult imported = await import.WaitAsync(TimeSpan.FromSeconds(5));
        WorkspaceV2ForwardResult listed = await read.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.AreEqual("report.txt", imported.RelativePath);
        Assert.IsNull(listed.Error);
        Assert.AreEqual<ulong>(1124, highWatermark);
        Assert.AreEqual(1, epochs.CompletedLeaseCount);
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
                DocumentQueryResult(FileDocumentSummary("reports/q3.txt", 3)));
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);

        DocumentListPayload result =
            await adapter.ListGlobalAsync(CancellationToken.None);

        DocumentBridgeEntry entry = result.Entries.Single();
        Assert.AreEqual("V3", entry.CurrentRevision);
        Assert.AreEqual(RevisionId.ToString("D"), entry.EffectiveRevisionId);
        Assert.AreEqual("available", entry.Availability);
        Assert.AreEqual("q3.txt", entry.DisplayName);
    }

    [TestMethod]
    public async Task CursorPageKeepsCapabilitiesIssuedForEarlierPages()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string firstPath = Path.Combine(workspaceRoot, "files", "first.txt");
        string secondPath = Path.Combine(workspaceRoot, "files", "second.txt");
        Directory.CreateDirectory(Path.GetDirectoryName(firstPath)!);
        await File.WriteAllTextAsync(firstPath, "first");
        await File.WriteAllTextAsync(secondPath, "second");
        int requests = 0;
        Guid secondDocumentId = Guid.Parse(
            "44444444-4444-4444-8444-444444444444");
        Guid secondRevisionId = Guid.Parse(
            "55555555-5555-4555-8555-555555555555");
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            requests += 1;
            if (requests == 1)
            {
                Assert.AreEqual(
                    JsonValueKind.Null,
                    root.GetProperty("params").GetProperty("cursor").ValueKind);
                return RpcSuccess(
                    root,
                    $$"""{"documents":[{{FileDocumentSummary("first.txt", 1)}}],"nextCursor":"opaque-next","topologyRevision":4}""");
            }
            Assert.AreEqual(
                "opaque-next",
                root.GetProperty("params").GetProperty("cursor").GetString());
            string second = FileDocumentSummary("second.txt", 1)
                .Replace(DocumentId.ToString("D"), secondDocumentId.ToString("D"))
                .Replace(RevisionId.ToString("D"), secondRevisionId.ToString("D"));
            return RpcSuccess(root, DocumentQueryResult(second));
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [WorkspaceDocumentOsAdapter.QueryDocumentsMethod]);

        DocumentListPayload first = await adapter.ListGlobalAsync(
            CancellationToken.None);
        DocumentQueryInput nextQuery = DocumentQueryInput.Default with
        {
            Cursor = first.NextCursor,
        };
        await adapter.ListGlobalAsync(CancellationToken.None, nextQuery);

        adapter.Open(first.Entries.Single().EntryHandle);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task DiffCapabilityMaterializesThroughHostGrantAndCleansEveryFile(bool advanceDuringPage)
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
        byte[] sourceContent = await File.ReadAllBytesAsync(materialized);
        DateTime sourceLastWriteTime = File.GetLastWriteTimeUtc(materialized);
        var engine = new InspectingDiffEngine();
        var epochs = new FakeEpochLeaseSource();
        bool sourceStale = false;
        int staleAssertions = 0;
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            string method = root.GetProperty("method").GetString()!;
            if (method == WorkspaceDocumentOsAdapter.QueryDocumentsMethod)
            {
                return RpcSuccess(
                    root,
                    DocumentQueryResult(FileDocumentSummary("reports/q3.txt", 3)));
            }
            if (method == WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod)
            {
                using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
                string destination = grant.RootElement.GetProperty("path").GetString()!;
                Assert.AreEqual("input", Path.GetFileName(destination));
                Assert.IsTrue(File.Exists(Path.Combine(
                    Directory.GetParent(destination)!.FullName,
                    "manifest.json")));
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
                      "effectiveMimeType":"text/plain",
                      "historicalContentHash":"{{BeforeHash}}",
                      "effectiveContentHash":"{{AfterHash}}"
                    }
                    """);
            }
            Assert.AreEqual(
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
                method);
            JsonElement parameters = root.GetProperty("params");
            Assert.AreEqual(
                "44444444-4444-4444-8444-444444444444",
                parameters.GetProperty("historicalRevisionId").GetString());
            Assert.AreEqual(
                BeforeHash,
                parameters.GetProperty("expectedHistoricalContentHash").GetString());
            Assert.AreEqual(
                AfterHash,
                parameters.GetProperty("expectedEffectiveContentHash").GetString());
            return RpcSuccess(
                root,
                $$"""
                {
                  "documentId":"{{DocumentId:D}}",
                  "historicalRevisionId":"44444444-4444-4444-8444-444444444444",
                  "effectiveRevisionId":"{{RevisionId:D}}",
                  "historicalContentHash":"{{BeforeHash}}",
                  "effectiveContentHash":"{{AfterHash}}",
                  "stable":{{(!sourceStale || (advanceDuringPage && staleAssertions++ == 0)).ToString().ToLowerInvariant()}}
                }
                """);
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.QueryDocumentsMethod,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
            ],
            epochs,
            engine,
            diffRoot);
        DocumentBridgeEntry entry = (await adapter.ListGlobalAsync(
            CancellationToken.None)).Entries.Single();

        Assert.IsTrue(entry.Capabilities.Contains("diff"));
        DocumentDiffSessionResult result = await adapter.CompareAsync(
            entry.EntryHandle,
            "44444444-4444-4444-8444-444444444444",
            RevisionId.ToString("D"),
            CancellationToken.None);

        Assert.AreEqual(DocumentDiffResultOutcome.Ready, result.Outcome);
        Assert.IsNotNull(result.Session);
        Assert.AreEqual(1, result.Session.Summary.TotalChangeGroups);
        DocumentDiffChangePageResult page = await adapter.ReadDiffPageAsync(
            new(result.Session.SessionId, null, 1), CancellationToken.None);
        Assert.AreEqual(DocumentDiffResultOutcome.Ready, page.Outcome);
        Assert.IsNotNull(page.Page);
        Assert.AreEqual(1, page.Page.Changes.Count);
        Assert.AreEqual("before\n", page.Page.Changes[0].Before!.Runs[0].Text);
        Assert.AreEqual("after\n", page.Page.Changes[0].After!.Runs[0].Text);
        var samePage = await adapter.ReadDiffPageAsync(new(result.Session.SessionId, null, 1), CancellationToken.None);
        Assert.AreEqual(page.Page.Changes[0].ChangeId, samePage.Page!.Changes[0].ChangeId);
        DocumentDiffChangePageResult invalid = await adapter.ReadDiffPageAsync(
            new(result.Session.SessionId, "not-issued", 1), CancellationToken.None);
        Assert.AreEqual(DocumentDiffPageFailure.InvalidCursor, invalid.Failure);
        sourceStale = true; // Restore/activation makes the authority's CAS assertion fail.
        var stalePage = await adapter.ReadDiffPageAsync(new(result.Session.SessionId, null, 1), CancellationToken.None);
        Assert.AreEqual(DocumentDiffPageFailure.Stale, stalePage.Failure);
        adapter.CloseDiffSession(result.Session.SessionId);
        DocumentDiffChangePageResult expired = await adapter.ReadDiffPageAsync(
            new(result.Session.SessionId, null, 1), CancellationToken.None);
        Assert.AreEqual(DocumentDiffPageFailure.SessionExpired, expired.Failure);
        Assert.AreEqual("before", engine.Before);
        Assert.AreEqual("after", engine.After);
        CollectionAssert.AreEqual(sourceContent, await File.ReadAllBytesAsync(materialized));
        Assert.AreEqual(sourceLastWriteTime, File.GetLastWriteTimeUtc(materialized));
        Assert.IsTrue(!Directory.Exists(diffRoot) ||
            Directory.GetFileSystemEntries(diffRoot).Length == 0);
        Assert.AreEqual(epochs.LeaseCount, epochs.CompletedLeaseCount);
    }

    [TestMethod]
    public async Task DiffRejectsMaterializedContentWhenRevisionHashDoesNotMatch()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string materialized = Path.Combine(workspaceRoot, "files", "reports", "q3.txt");
        string diffRoot = Path.Combine(directory.Path, "diff-sessions");
        Directory.CreateDirectory(Path.GetDirectoryName(materialized)!);
        await File.WriteAllTextAsync(materialized, "current leaf");
        var engine = new InspectingDiffEngine();
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            string method = root.GetProperty("method").GetString()!;
            if (method == WorkspaceDocumentOsAdapter.QueryDocumentsMethod)
            {
                return RpcSuccess(
                    root,
                    DocumentQueryResult(FileDocumentSummary("reports/q3.txt", 3)));
            }
            Assert.AreEqual(WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod, method);
            using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
            string destination = grant.RootElement.GetProperty("path").GetString()!;
            File.WriteAllText(
                Path.Combine(destination, WorkspaceDocumentDiffCoordinator.HistoricalFileName),
                "tamper"); // Same byte length as "before"; length cannot establish identity.
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
                  "effectiveMimeType":"text/plain",
                  "historicalContentHash":"{{BeforeHash}}",
                  "effectiveContentHash":"{{AfterHash}}"
                }
                """);
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.QueryDocumentsMethod,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
            ],
            new FakeEpochLeaseSource(),
            engine,
            diffRoot);
        DocumentBridgeEntry entry = (await adapter.ListGlobalAsync(
            CancellationToken.None)).Entries.Single();

        DocumentDiffSessionResult result = await adapter.CompareAsync(
            entry.EntryHandle,
            "44444444-4444-4444-8444-444444444444",
            RevisionId.ToString("D"),
            CancellationToken.None);

        Assert.AreEqual(DocumentDiffResultOutcome.Failure, result.Outcome);
        Assert.AreEqual(DocumentDiffSessionFailure.Stale, result.Failure);
        Assert.IsNull(engine.Before);
        Assert.IsTrue(!Directory.Exists(diffRoot) ||
            Directory.GetFileSystemEntries(diffRoot).Length == 0);
    }

    [TestMethod]
    public async Task DocumentControllerCancelCooperativelyCancelsTheRunningDiffEngine()
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
            if (method == WorkspaceDocumentOsAdapter.QueryDocumentsMethod)
            {
                return RpcSuccess(
                    root,
                    DocumentQueryResult(FileDocumentSummary("reports/q3.txt", 3)));
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
                  "effectiveMimeType":"text/plain",
                  "historicalContentHash":"{{BeforeHash}}",
                  "effectiveContentHash":"{{AfterHash}}"
                }
                """);
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.QueryDocumentsMethod,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
            ],
            new FakeEpochLeaseSource(),
            engine,
            diffRoot);
        DocumentBridgeEntry entry = (await adapter.ListGlobalAsync(
            CancellationToken.None)).Entries.Single();
        var sink = new FakeWebReplySink();
        var controller = new DocumentBrowseRequestController(sink);
        controller.SetWorkspace(adapter);

        Task diff = controller.DispatchAsync(Request(
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
        await controller.DispatchAsync(Request(
            "document.diffCancelRequested",
            "cancel-1",
            $$"""
            {
              "entryHandle":"{{entry.EntryHandle}}",
              "operationId":"{{DiffOperationId}}"
            }
            """));
        await diff;

        FakeWebReplySink.Reply? cancel = await sink.WaitForAsync(
            "document.diffCancelCompleted");
        FakeWebReplySink.Reply? completed = await sink.WaitForAsync(
            "document.diffCompleted");
        Assert.IsNotNull(cancel);
        Assert.AreEqual("cancel-1", cancel.RequestId);
        Assert.IsTrue(((dynamic)cancel.Payload!).cancelled);
        Assert.IsNotNull(completed);
        Assert.AreEqual("diff-1", completed.RequestId);
        Assert.AreEqual(DocumentDiffResultOutcome.Failure, ((DocumentDiffSessionResult)completed.Payload!).Outcome);
        Assert.AreEqual(DocumentDiffSessionFailure.Cancelled, ((DocumentDiffSessionResult)completed.Payload!).Failure);
        Assert.IsTrue(engine.CancellationObserved);
        Assert.IsTrue(!Directory.Exists(diffRoot) ||
            Directory.GetFileSystemEntries(diffRoot).Length == 0);
    }

    [TestMethod]
    public async Task DocumentControllerRegistersDiffBeforeCancellation()
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
            if (method == WorkspaceDocumentOsAdapter.QueryDocumentsMethod)
            {
                return RpcSuccess(
                    root,
                    DocumentQueryResult(FileDocumentSummary("reports/q3.txt", 3)));
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
                  "effectiveMimeType":"text/plain",
                  "historicalContentHash":"{{BeforeHash}}",
                  "effectiveContentHash":"{{AfterHash}}"
                }
                """);
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [
                WorkspaceDocumentOsAdapter.QueryDocumentsMethod,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod,
            ],
            new FakeEpochLeaseSource(),
            new BlockingDiffEngine(),
            diffRoot);
        DocumentBridgeEntry entry = (await adapter.ListGlobalAsync(
            CancellationToken.None)).Entries.Single();
        var sink = new FakeWebReplySink();
        var controller = new DocumentBrowseRequestController(sink);
        controller.SetWorkspace(adapter);

        Task diff = controller.DispatchAsync(Request(
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
        await controller.DispatchAsync(Request(
            "document.diffCancelRequested",
            "cancel-before-registration",
            $$"""
            {
              "entryHandle":"{{entry.EntryHandle}}",
              "operationId":"{{DiffOperationId}}"
            }
            """));
        await diff;

        FakeWebReplySink.Reply? cancel = await sink.WaitForAsync(
            "document.diffCancelCompleted");
        FakeWebReplySink.Reply? completed = await sink.WaitForAsync(
            "document.diffCompleted");
        Assert.IsNotNull(cancel);
        Assert.IsTrue(((dynamic)cancel.Payload!).cancelled);
        Assert.IsNotNull(completed);
        Assert.AreEqual(
            DocumentDiffSessionFailure.Cancelled,
            ((DocumentDiffSessionResult)completed.Payload!).Failure);
        Assert.IsTrue(!Directory.Exists(diffRoot) ||
            Directory.GetFileSystemEntries(diffRoot).Length == 0);
    }

    [TestMethod]
    [DataRow("docx", false)]
    [DataRow("xlsx", false)]
    [DataRow("bin", false)]
    [DataRow("txt", true)]
    [DataRow("epoch", true)]
    public async Task PublicationCloseAndExpiryRevokeMetadataWithoutDeletingActiveReaders(string extension, bool closeDuringPublication)
    {
        using var directory = new TemporaryDirectory();
        var time = new PublicationTimeProvider();
        using var broker = new DocumentDiffArtifactBroker(directory.Path, TimeSpan.FromMinutes(1), time);
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            bool materializing = root.GetProperty("method").GetString() == WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod;
            if (materializing)
            {
                using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
                string destination = grant.RootElement.GetProperty("path").GetString()!;
                File.WriteAllText(Path.Combine(destination, "historical.content"), "before");
                File.WriteAllText(Path.Combine(destination, "effective.content"), "before");
            }
            var result = new Dictionary<string, object>
            {
                ["documentId"] = DocumentId.ToString("D"),
                ["historicalRevisionId"] = "44444444-4444-4444-8444-444444444444",
                ["effectiveRevisionId"] = RevisionId.ToString("D"),
                ["historicalContentHash"] = BeforeHash,
                ["effectiveContentHash"] = BeforeHash,
            };
            if (materializing)
            {
                result["historicalMimeType"] = "application/octet-stream";
                result["effectiveMimeType"] = "application/octet-stream";
            }
            else result["stable"] = true;
            return RpcSuccess(root, JsonSerializer.Serialize(result));
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        var binding = new WorkspaceDocumentBinding(WorkspaceId, 7, true, directory.Path, gateway,
            [WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod, WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod]);
        var capabilities = new DocumentCapabilityStore();
        string handle = capabilities.Issue(WorkspaceId, 7, DocumentId, $"synthetic.{extension}", RevisionId, ["diff"]);
        WorkspaceDocumentDiffCoordinator? coordinator = null;
        var epochs = new FakeEpochLeaseSource();
        var engine = new InspectingDiffEngine(() =>
        {
            if (extension == "epoch") epochs.Current = false;
            else if (closeDuringPublication) time.OnNextRead = () => coordinator!.CloseAllSessions();
        });
        coordinator = new WorkspaceDocumentDiffCoordinator(epochs, engine, broker);
        var compared = await coordinator.CompareAsync(binding, capabilities.Resolve(handle, "diff", WorkspaceId, 7),
            handle, "44444444-4444-4444-8444-444444444444", RevisionId.ToString("D"), CancellationToken.None);
        if (closeDuringPublication)
        {
            Assert.AreEqual(DocumentDiffSessionFailure.Stale, compared.Failure);
        }
        else
        {
            Assert.IsNotNull(compared.Session);
            Assert.IsTrue(compared.Session.Warnings.Contains(DocumentDiffWarning.PartialCoverage));
            Assert.IsTrue(compared.Session.Coverage.Areas.Any(area => area.Status == DocumentDiffCoverageStatus.NotCovered));
            using (var reader = broker.OpenRead(compared.Session.SessionId, WorkspaceId, 7, DocumentDiffArtifactKind.ChangeIndex))
            {
                time.Now += TimeSpan.FromMinutes(2);
                broker.CleanupExpired();
                var expired = await coordinator.ReadPageAsync(binding, new(compared.Session.SessionId, null, 1), CancellationToken.None);
                Assert.AreEqual(DocumentDiffPageFailure.SessionExpired, expired.Failure);
                Assert.IsTrue(Directory.GetDirectories(directory.Path).Length > 0);
            }
        }
        Assert.AreEqual(0, Directory.GetFileSystemEntries(directory.Path).Length);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task ProductionXlsxPagesCarryCacheWarningAndAuthorityCAS(bool stale)
    {
        string repo = AppContext.BaseDirectory;
        while (!File.Exists(Path.Combine(repo, ".ci/project.json")))
            repo = Directory.GetParent(repo)!.FullName;
        string fixtures = Path.Combine(repo, "desktop/tests/VibeTable.DocumentDiff.OpenXml.Tests/TestData/Qualification/xlsx");
        byte[] before = File.ReadAllBytes(Path.Combine(fixtures, "format-before.xlsx"));
        byte[] after = File.ReadAllBytes(Path.Combine(fixtures, "format-after.xlsx"));
        // Simulates the authority's existing content-addressed materialization contract.
        string beforeHash = "sha256:" + Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(before)).ToLowerInvariant();
        string afterHash = "sha256:" + Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(after)).ToLowerInvariant();
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            bool materializing = root.GetProperty("method").GetString() == WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod;
            if (materializing)
            {
                using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
                string input = grant.RootElement.GetProperty("path").GetString()!;
                File.WriteAllBytes(Path.Combine(input, "historical.content"), before);
                File.WriteAllBytes(Path.Combine(input, "effective.content"), after);
            }
            var result = new Dictionary<string, object>
            {
                ["documentId"] = DocumentId.ToString("D"),
                ["historicalRevisionId"] = "44444444-4444-4444-8444-444444444444",
                ["effectiveRevisionId"] = RevisionId.ToString("D"),
                ["historicalContentHash"] = beforeHash,
                ["effectiveContentHash"] = afterHash,
            };
            if (materializing)
            {
                result["historicalMimeType"] = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";
                result["effectiveMimeType"] = result["historicalMimeType"];
            }
            else result["stable"] = !stale;
            return RpcSuccess(root, JsonSerializer.Serialize(result));
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        var binding = new WorkspaceDocumentBinding(WorkspaceId, 7, true, directory.Path, gateway,
            [WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod, WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod]);
        var capabilities = new DocumentCapabilityStore();
        string handle = capabilities.Issue(WorkspaceId, 7, DocumentId, "synthetic.xlsx", RevisionId, ["diff"]);
        var coordinator = new WorkspaceDocumentDiffCoordinator(new FakeEpochLeaseSource(),
            new VibeTable.DocumentDiff.OpenXml.OpenXmlDocumentDiffEngine(), broker);
        var compared = await coordinator.CompareAsync(binding, capabilities.Resolve(handle, "diff", WorkspaceId, 7),
            handle, "44444444-4444-4444-8444-444444444444", RevisionId.ToString("D"), CancellationToken.None);
        if (stale)
            Assert.AreEqual(DocumentDiffSessionFailure.Stale, compared.Failure);
        else
        {
            Assert.IsNotNull(compared.Session);
            Assert.AreEqual(DocumentDiffFormat.Xlsx, compared.Session.Format);
            Assert.AreEqual(DocumentDiffProvider.XlsxBuiltIn, compared.Session.Provider);
            Assert.AreEqual(3, compared.Session.Summary.TotalChangeGroups);
            Assert.AreEqual(1, compared.Session.Summary.FormattingChanges);
            Assert.AreEqual(2, compared.Session.Summary.OtherChanges);
            Assert.IsTrue(compared.Session.Warnings.Contains(DocumentDiffWarning.CachedValuesNotRecalculated));
            Assert.IsTrue(compared.Session.Warnings.Contains(DocumentDiffWarning.PartialCoverage));
            var page = await coordinator.ReadPageAsync(binding, new(compared.Session.SessionId, null, 1), CancellationToken.None);
            Assert.IsNotNull(page.Page);
            DocumentDiffChange format = page.Page.Changes.Single();
            Assert.AreEqual(DocumentDiffChangeKind.Format, format.Kind);
            Assert.AreEqual("报价表", format.Location.SheetName);
            Assert.AreEqual("B7", format.Location.CellAddress);
            StringAssert.Contains(string.Concat(format.Before!.Runs.Select(run => run.Text)), "numFmt=builtin:2");
            StringAssert.Contains(string.Concat(format.After!.Runs.Select(run => run.Text)), "numFmt=builtin:10");
            var next = await coordinator.ReadPageAsync(binding, new(compared.Session.SessionId, page.Page.NextCursor, 50), CancellationToken.None);
            Assert.IsNotNull(next.Page);
            CollectionAssert.AreEqual(new[] { "row 8 hidden: true", "hidden columns: 6:6" },
                next.Page.Changes.Select(change => string.Concat(change.Before!.Runs.Select(run => run.Text))).ToArray());
            CollectionAssert.AreEqual(new[] { "row 8 hidden: false", "hidden columns: <none>" },
                next.Page.Changes.Select(change => string.Concat(change.After!.Runs.Select(run => run.Text))).ToArray());
            coordinator.CloseSession(compared.Session.SessionId);
            var expired = await coordinator.ReadPageAsync(binding, new(compared.Session.SessionId, null, 50), CancellationToken.None);
            Assert.AreEqual(DocumentDiffPageFailure.SessionExpired, expired.Failure);
        }
        Assert.AreEqual(0, Directory.GetFileSystemEntries(directory.Path).Length);
        CollectionAssert.AreEqual(before, File.ReadAllBytes(Path.Combine(fixtures, "format-before.xlsx")));
        CollectionAssert.AreEqual(after, File.ReadAllBytes(Path.Combine(fixtures, "format-after.xlsx")));
    }

    private sealed class PublicationTimeProvider : TimeProvider
    {
        public DateTimeOffset Now { get; set; } = DateTimeOffset.UtcNow;
        public Action? OnNextRead { get; set; }
        public override DateTimeOffset GetUtcNow()
        {
            Action? action = OnNextRead;
            OnNextRead = null;
            action?.Invoke();
            return Now;
        }
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

    private static string FileDocumentSummary(string relativePath, ulong formalVersion)
        => $$"""
        {
          "contractVersion":"2.0",
          "documentId":"{{DocumentId:D}}",
          "relativePath":"{{relativePath}}",
          "displayName":"{{Path.GetFileName(relativePath)}}",
          "extension":"{{Path.GetExtension(relativePath).TrimStart('.')}}",
          "mimeType":"text/plain",
          "sizeBytes":12,
          "effectiveRevisionId":"{{RevisionId:D}}",
          "effectiveRevisionCreatedAt":"2026-08-12T09:00:00Z",
          "formalVersion":{{formalVersion}},
          "status":"active"
        }
        """;

    private static string DocumentQueryResult(string document)
        => $$"""{"documents":[{{document}}],"nextCursor":null,"topologyRevision":4}""";

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
        string payload,
        WorkspaceWireScope? scope = null)
    {
        using JsonDocument document = JsonDocument.Parse(payload);
        return new RoutedWebRequest(
            type,
            requestId,
            document.RootElement.Clone(),
            string.Empty,
            scope);
    }

    private static WorkspaceWireScope WorkspaceScope()
        => new()
        {
            Scope = "workspace",
            WorkspaceId = WorkspaceId,
            SessionEpoch = 7,
            OperationId = Guid.NewGuid(),
            Sequence = 1,
        };

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

    private sealed class AsyncRecordingHandler(
        Func<HttpRequestMessage, CancellationToken, Task<HttpResponseMessage>> responder)
        : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
            => responder(request, cancellationToken);
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

    private sealed class InspectingDiffEngine(Action? afterCompared = null) : IDocumentDiffEngine
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
            DocumentDiffOutcome result = await new DocumentDiffEngine().CompareAsync(request, cancellationToken);
            afterCompared?.Invoke();
            return result;
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
        public bool Current { get; set; } = true;

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

        public bool IsCurrent(WorkspaceRequestEpochLease? lease) => Current && lease is not null;
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
