using System.Collections.Concurrent;
using System.Runtime.InteropServices;
using System.Text.Json;
using System.Windows.Threading;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

/// <summary>
/// #465: closed host text-clipboard routes. Covers the strict separation of
/// empty-success from busy/unavailable/failed/timeout, the reply envelope
/// budget, workspace-session lease capture/invalidation, and the STA
/// dispatcher marshalling + cancellation of WindowsTextClipboard.
/// </summary>
[TestClass]
public sealed class ClipboardRequestControllerTests
{
    [TestMethod]
    public async Task ReadEmptyTextIsSuccessNotFailure()
    {
        using var fixture = await Fixture.OpenWithApiAsync(new FakeApi(""));
        JsonElement reply = await fixture.RequestAsync("clipboard.readText", "{}");
        Assert.AreEqual("", reply.GetProperty("text").GetString());
        Assert.AreEqual(0, fixture.FailureCount);
    }

    [TestMethod]
    public async Task ReadRoundTripsUnicodeText()
    {
        const string text = "任务\t多行\n换行 \"引号\" 😀 零 0 false 007";
        using var fixture = await Fixture.OpenWithApiAsync(new FakeApi(text));
        JsonElement reply = await fixture.RequestAsync("clipboard.readText", "{}");
        Assert.AreEqual(text, reply.GetProperty("text").GetString());
    }

    [TestMethod]
    public async Task ReadMapsDistinctOutcomesWithoutLeakingBodies()
    {
        foreach (var (outcome, code) in new[]
        {
            (TextClipboardOutcome.Busy, "CLIPBOARD_BUSY"),
            (TextClipboardOutcome.Unavailable, "CLIPBOARD_UNAVAILABLE"),
            (TextClipboardOutcome.Failed, "CLIPBOARD_FAILED"),
            (TextClipboardOutcome.TimedOut, "CLIPBOARD_TIMEOUT"),
        })
        {
            using var fixture = await Fixture.OpenWithApiAsync(new FakeApi("SECRET-正文"));
            await fixture.RequestAsync("clipboard.readText", "{}",
                expectedError: code, clipboard: new ScriptedClipboard(outcome));
            Assert.IsFalse(fixture.LastFailureMessage?.Contains("SECRET") == true, outcome.ToString());
        }
    }

    [TestMethod]
    public async Task RealComCantOpenMapsToBusyOnTheStaPipeline()
    {
        var api = new FakeApi("body", outcome: TextClipboardOutcome.Busy);
        using var fixture = await Fixture.OpenWithApiAsync(api);
        await fixture.RequestAsync("clipboard.readText", "{}", expectedError: "CLIPBOARD_BUSY");
    }

    [TestMethod]
    public async Task BusyClipboardSucceedsAgainAfterRelease()
    {
        var api = new FakeApi(outcome: TextClipboardOutcome.Busy);
        using var fixture = await Fixture.OpenWithApiAsync(api);
        await fixture.RequestAsync("clipboard.readText", "{}", expectedError: "CLIPBOARD_BUSY");
        api.Outcome = TextClipboardOutcome.Succeeded;
        api.Text = "released";
        JsonElement reply = await fixture.RequestAsync("clipboard.readText", "{}");
        Assert.AreEqual("released", reply.GetProperty("text").GetString());
    }

    [TestMethod]
    public async Task OverBudgetReadFailsWholeWithFileImportGuidance()
    {
        string fiveMiB = new string('x', 5 * 1024 * 1024);
        using var fixture = await Fixture.OpenWithApiAsync(new FakeApi(fiveMiB));
        await fixture.RequestAsync("clipboard.readText", "{}", expectedError: "CLIPBOARD_TOO_LARGE");
        Assert.AreEqual(0, fixture.SuccessCount);
    }

    [TestMethod]
    public async Task WriteSucceedsEchoesWrittenAndReachesClipboard()
    {
        var api = new FakeApi();
        using var fixture = await Fixture.OpenWithApiAsync(api);
        JsonElement reply = await fixture.RequestAsync(
            "clipboard.writeText", $$"""{"text":"a\tb\nc"}""");
        Assert.IsTrue(reply.GetProperty("written").GetBoolean());
        Assert.AreEqual("a\tb\nc", api.LastWrittenText);
    }

    [TestMethod]
    public async Task InvalidPayloadsAreRejectedAsBadPayload()
    {
        var api = new FakeApi();
        using var fixture = await Fixture.OpenWithApiAsync(api);
        await fixture.RequestAsync("clipboard.readText", $$"""{"text":"x"}""", expectedError: "BAD_PAYLOAD");
        await fixture.RequestAsync("clipboard.writeText", "{}", expectedError: "BAD_PAYLOAD");
        await fixture.RequestAsync("clipboard.writeText", $$"""{"text":1}""", expectedError: "BAD_PAYLOAD");
        await fixture.RequestAsync("clipboard.writeText", $$"""{"text":"a","extra":1}""", expectedError: "BAD_PAYLOAD");
        Assert.IsNull(api.ReadThreadId);
        Assert.IsNull(api.WriteThreadId);
    }

    [TestMethod]
    public async Task MissingOrReplayedScopeNeverReachesTheClipboard()
    {
        var api = new FakeApi("never");
        using var fixture = await Fixture.OpenWithApiAsync(api);
        await fixture.DispatchDirectAsync("clipboard.readText", "{}", scope: null,
            expectedError: "BAD_WORKSPACE_SCOPE");
        Assert.IsNull(api.ReadThreadId, "A scopeless request must never reach the clipboard.");

        WorkspaceWireScope used = fixture.NextScope();
        await fixture.RequestAsync("clipboard.readText", "{}", scope: used);
        await fixture.RequestAsync("clipboard.readText", "{}", scope: used,
            expectedError: "BAD_WORKSPACE_SCOPE");
    }

    [TestMethod]
    public async Task SessionClosedMidReadStaysSilent()
    {
        var gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var api = new GatedApi(gate, release, "late-text");
        using var fixture = await Fixture.OpenWithApiAsync(api);
        Task dispatch = fixture.DispatchFireAndForgetAsync("clipboard.readText", "{}");
        await gate.Task.WaitAsync(TimeSpan.FromSeconds(5));

        Task closing = fixture.Manager.CloseAsync("clipboard read in flight");
        try
        {
            await Task.WhenAll(
                dispatch.WaitAsync(TimeSpan.FromSeconds(5)),
                closing.WaitAsync(TimeSpan.FromSeconds(10)));
            Assert.IsFalse(api.Finished.Task.IsCompleted, "Session draining must not wait for a blocked native call.");
        }
        finally
        {
            release.TrySetResult();
        }
        Assert.AreEqual(0, fixture.TotalReplyCount, "A stale session must not emit any reply.");
    }

    [TestMethod]
    public async Task UnknownTypeRepliesUnknownType()
    {
        using var fixture = await Fixture.OpenWithApiAsync(new FakeApi());
        await fixture.RequestAsync("clipboard.clear", "{}", expectedError: "UNKNOWN_TYPE");
    }

    [TestMethod]
    public void ControllerHandlesOnlyTheClosedTypePair()
    {
        Assert.IsTrue(ClipboardRequestController.Handles("clipboard.readText"));
        Assert.IsTrue(ClipboardRequestController.Handles("clipboard.writeText"));
        Assert.IsFalse(ClipboardRequestController.Handles("clipboard.readImage"));
        Assert.IsFalse(ClipboardRequestController.Handles("table.previewPasteRequested"));
    }

    // -------------------------------------------------------------------
    // WindowsTextClipboard STA marshalling
    // -------------------------------------------------------------------

    [TestMethod]
    public async Task WindowsClipboardRunsOnStaDispatcherThread()
    {
        using var sta = new StaDispatcherHost();
        var api = new FakeApi("sta-text");
        var clipboard = new WindowsTextClipboard(sta.Dispatcher, api);

        TextClipboardResult read = await clipboard.ReadTextAsync(CancellationToken.None);
        TextClipboardResult write = await clipboard.WriteTextAsync("w", CancellationToken.None);

        Assert.AreEqual(TextClipboardOutcome.Succeeded, read.Outcome);
        Assert.AreEqual("sta-text", read.Text);
        Assert.AreEqual(TextClipboardOutcome.Succeeded, write.Outcome);
        Assert.AreEqual(sta.ThreadId, api.ReadThreadId);
        Assert.AreEqual(sta.ThreadId, api.WriteThreadId);
        Assert.AreEqual("w", api.LastWrittenText);
    }

    [TestMethod]
    public async Task WindowsClipboardMapsOnlyCantOpenToBusy()
    {
        using var sta = new StaDispatcherHost();
        var busy = new WindowsTextClipboard(sta.Dispatcher,
            new ThrowingApi(new COMException("openclipboard failed", unchecked((int)0x800401D0))));
        Assert.AreEqual(TextClipboardOutcome.Busy, (await busy.ReadTextAsync(CancellationToken.None)).Outcome);

        var other = new WindowsTextClipboard(sta.Dispatcher,
            new ThrowingApi(new COMException("setclipboarddata failed", unchecked((int)0x800401D2))));
        Assert.AreEqual(TextClipboardOutcome.Failed, (await other.ReadTextAsync(CancellationToken.None)).Outcome);
    }

    [TestMethod]
    public async Task WindowsClipboardWithoutTextYieldsEmptySuccess()
    {
        using var sta = new StaDispatcherHost();
        var clipboard = new WindowsTextClipboard(sta.Dispatcher, new FakeApi(containsText: false));
        TextClipboardResult read = await clipboard.ReadTextAsync(CancellationToken.None);
        Assert.AreEqual(TextClipboardOutcome.Succeeded, read.Outcome);
        Assert.AreEqual("", read.Text);
    }

    [TestMethod]
    public async Task WindowsClipboardCancelledBeforeStartNeverTouchesTheClipboard()
    {
        using var sta = new StaDispatcherHost();
        var api = new FakeApi("never-written");
        using var cts = new CancellationTokenSource();
        // Entry-observed gate at the dispatch's own Send priority: the write
        // provably stays queued behind the busy dispatcher until cancelled.
        var gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var gateEntered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        _ = sta.Dispatcher.InvokeAsync(
            () => { gateEntered.TrySetResult(); gate.Task.GetAwaiter().GetResult(); },
            DispatcherPriority.Send);
        try
        {
            await gateEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            var clipboard = new WindowsTextClipboard(sta.Dispatcher, api);
            Task<TextClipboardResult> pending = clipboard.WriteTextAsync("late-write", cts.Token);

            cts.Cancel();
            gate.TrySetResult();
            TextClipboardResult result = await pending.WaitAsync(TimeSpan.FromSeconds(5));
            await sta.Dispatcher.InvokeAsync(() => { }, DispatcherPriority.Background)
                .Task.WaitAsync(TimeSpan.FromSeconds(5));

            Assert.AreEqual(TextClipboardOutcome.Unavailable, result.Outcome);
            Assert.IsNull(api.WriteThreadId, "A cancelled queued write must never reach the clipboard.");
            Assert.IsNull(api.LastWrittenText);
            Assert.IsFalse(result.WriteMayHaveCompleted);
        }
        finally
        {
            gate.TrySetResult();
        }
    }

    [TestMethod]
    public async Task WindowsClipboardCompletedOperationKeepsItsRealResult()
    {
        using var sta = new StaDispatcherHost();
        using var cts = new CancellationTokenSource();
        var clipboard = new WindowsTextClipboard(sta.Dispatcher, new FakeApi("real-result"));
        TextClipboardResult read = await clipboard.ReadTextAsync(cts.Token);
        TextClipboardResult write = await clipboard.WriteTextAsync("committed", cts.Token);

        cts.Cancel();

        Assert.AreEqual(TextClipboardOutcome.Succeeded, read.Outcome);
        Assert.AreEqual("real-result", read.Text);
        Assert.AreEqual(TextClipboardOutcome.Succeeded, write.Outcome);
        Assert.IsFalse(write.WriteMayHaveCompleted);
    }

    [TestMethod]
    public async Task WindowsClipboardReportsShutdownDispatcherAsUnavailable()
    {
        using var sta = new StaDispatcherHost();
        var clipboard = new WindowsTextClipboard(sta.Dispatcher, new FakeApi("x"));
        sta.Shutdown();
        TextClipboardResult read = await clipboard.ReadTextAsync(CancellationToken.None);
        Assert.AreEqual(TextClipboardOutcome.Unavailable, read.Outcome);
    }

    // -------------------------------------------------------------------
    // Request deadline (bound to the bridge budget, no retry)
    // -------------------------------------------------------------------

    [TestMethod]
    public async Task DeadlineAbortsAQueuedReadAsTimeoutWithoutAnyLateResult()
    {
        var gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var gateEntered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var api = new FakeApi("late-text");
        using var fixture = await Fixture.OpenWithApiAsync(api, TimeSpan.FromMilliseconds(200));
        // Same-priority, entry-observed gate: the read provably stays queued
        // behind the busy dispatcher until the deadline aborts it.
        _ = fixture.StaDispatcher.InvokeAsync(
            () => { gateEntered.TrySetResult(); gate.Task.GetAwaiter().GetResult(); },
            DispatcherPriority.Send);
        try
        {
            await gateEntered.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await fixture.RequestAsync("clipboard.readText", "{}", expectedError: "CLIPBOARD_TIMEOUT");
            await Task.Delay(300);
        }
        finally
        {
            gate.TrySetResult();
        }
        await fixture.StaDispatcher.InvokeAsync(() => { }, DispatcherPriority.Background)
            .Task.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.IsNull(api.ReadThreadId, "A deadline-aborted read must never reach the clipboard.");
    }

    [TestMethod]
    public async Task StartedReadTimesOutWhileUiContinuesAndLateResultStaysSilent()
    {
        var started = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var api = new GatedApi(started, release, "real-late-text");
        using var fixture = await Fixture.OpenWithApiAsync(api, TimeSpan.FromMilliseconds(500));
        using var ui = new StaDispatcherHost();
        try
        {
            Task<JsonElement> request = await ui.Dispatcher.InvokeAsync(() => fixture.RequestAsync(
                "clipboard.readText", "{}", expectedError: "CLIPBOARD_TIMEOUT"));
            await started.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await ui.Dispatcher.InvokeAsync(() => { }).Task.WaitAsync(TimeSpan.FromSeconds(2));
            await request.WaitAsync(TimeSpan.FromSeconds(3));
            Assert.IsFalse(api.Finished.Task.IsCompleted, "The host deadline must not wait for OLE to return.");
            Assert.AreEqual(1, fixture.TotalReplyCount);

            for (int i = 0; i < 10; i++)
                await fixture.RequestAsync("clipboard.readText", "{}", expectedError: "CLIPBOARD_BUSY");
            Assert.AreEqual(1, api.CallCount, "Retries must neither queue native calls nor start replacement threads.");
        }
        finally
        {
            release.TrySetResult();
        }
        await api.Finished.Task.WaitAsync(TimeSpan.FromSeconds(5));
        await fixture.StaDispatcher.InvokeAsync(() => { }, DispatcherPriority.Background);
        Assert.AreEqual(11, fixture.TotalReplyCount, "A late result must not emit a second reply.");
        Assert.AreEqual(0, fixture.SuccessCount);
    }

    [TestMethod]
    public async Task StartedWriteTimesOutWithUnknownCommitAndLateWriteDoesNotReply()
    {
        var started = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var api = new GatedApi(started, release, "");
        using var fixture = await Fixture.OpenWithApiAsync(api, TimeSpan.FromMilliseconds(500));
        try
        {
            Task<JsonElement> request = fixture.RequestAsync(
                "clipboard.writeText", "{\"text\":\"late-write\"}", expectedError: "CLIPBOARD_TIMEOUT");
            await started.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await request.WaitAsync(TimeSpan.FromSeconds(3));
            Assert.IsNull(api.LastWrittenText);
            StringAssert.Contains(fixture.LastFailureMessage!, "写入结果尚未确认");
            await fixture.RequestAsync("clipboard.writeText", "{\"text\":\"retry\"}", expectedError: "CLIPBOARD_BUSY");
            Assert.AreEqual(1, api.CallCount);
        }
        finally
        {
            release.TrySetResult();
        }
        await api.Finished.Task.WaitAsync(TimeSpan.FromSeconds(5));
        await fixture.StaDispatcher.InvokeAsync(() => { }, DispatcherPriority.Background);
        Assert.AreEqual("late-write", api.LastWrittenText, "A started OLE write cannot be claimed to have been cancelled.");
        Assert.AreEqual(2, fixture.TotalReplyCount);
        Assert.AreEqual(0, fixture.SuccessCount);
    }

    [TestMethod]
    public async Task ProductionStaIsIndependentOfUiAndSharedAcrossClipboardInstances()
    {
        var started = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var api = new GatedApi(started, release, "native-result");
        var clipboard = new WindowsTextClipboard(api);
        var retryApi = new FakeApi("retry-result");
        var anotherClipboard = new WindowsTextClipboard(retryApi);
        using var ui = new StaDispatcherHost();
        using var deadline = new CancellationTokenSource(TimeSpan.FromMilliseconds(500));
        try
        {
            Task<TextClipboardResult> read = await ui.Dispatcher.InvokeAsync(() => clipboard.ReadTextAsync(deadline.Token));
            await started.Task.WaitAsync(TimeSpan.FromSeconds(5));
            await ui.Dispatcher.InvokeAsync(() => { }).Task.WaitAsync(TimeSpan.FromSeconds(2));
            TextClipboardResult result = await read.WaitAsync(TimeSpan.FromSeconds(3));
            Assert.AreEqual(TextClipboardOutcome.Unavailable, result.Outcome);
            Assert.AreNotEqual(ui.ThreadId, api.ThreadId);
            Assert.AreEqual(ApartmentState.STA, api.Apartment);
            Assert.AreEqual(TextClipboardOutcome.Busy,
                (await anotherClipboard.ReadTextAsync(CancellationToken.None)).Outcome);
            Assert.IsNull(retryApi.ReadThreadId);
        }
        finally
        {
            release.TrySetResult();
        }
        await api.Finished.Task.WaitAsync(TimeSpan.FromSeconds(5));
        // Busy is valid until the native dispatch fully completes; do not create a replacement STA.
        TextClipboardResult retried;
        using var retryDeadline = new CancellationTokenSource(TimeSpan.FromSeconds(5));
        do
        {
            retried = await anotherClipboard.ReadTextAsync(retryDeadline.Token);
            if (retried.Outcome == TextClipboardOutcome.Busy)
                await Task.Delay(10, retryDeadline.Token);
        } while (retried.Outcome == TextClipboardOutcome.Busy);
        Assert.AreEqual(TextClipboardOutcome.Succeeded, retried.Outcome);
        Assert.AreEqual(api.ThreadId, retryApi.ReadThreadId, "Recovery must reuse the sole process-wide STA.");
    }

    // -------------------------------------------------------------------
    // Real WPF clipboard harness (synthetic content; restores prior text)
    // -------------------------------------------------------------------

    [TestMethod]
    public async Task RealWpfClipboardRoundTripsMultiLineTsvThroughStaDispatcher()
    {
        using var sta = new StaDispatcherHost();
        var api = new WpfTextClipboardApi();
        var clipboard = new WindowsTextClipboard(sta.Dispatcher, api);
        const string tsv = "任务\t状态\r\n\"多\n行\"\t引号\r\nfalse\t007\r\n😀\t";
        using (ClipboardTextRestore restore = await ClipboardTextRestore.CaptureAsync(sta.Dispatcher))
        {
            TextClipboardResult write = await clipboard.WriteTextAsync(tsv, CancellationToken.None);
            Assert.AreEqual(TextClipboardOutcome.Succeeded, write.Outcome);
            TextClipboardResult read = await clipboard.ReadTextAsync(CancellationToken.None);
            Assert.AreEqual(TextClipboardOutcome.Succeeded, read.Outcome);
            Assert.AreEqual(tsv, read.Text);
        }
    }

    [TestMethod]
    public async Task RealWpfClipboardReadSurvivesWin32ClipboardHold()
    {
        // Real-machine finding: WPF reads via OLE (OleGetClipboard), so another
        // process holding the Win32 clipboard open does NOT break our read
        // path. The CLIPBRD_E_CANT_OPEN → Busy mapping stays covered by the
        // verified-HRESULT fake above; true OLE contention needs E2E.
        using var sta = new StaDispatcherHost();
        var clipboard = new WindowsTextClipboard(sta.Dispatcher, new WpfTextClipboardApi());
        using (ClipboardTextRestore restore = await ClipboardTextRestore.CaptureAsync(sta.Dispatcher))
        using (var held = ClipboardHolder.Hold())
        {
            TextClipboardResult read = await clipboard.ReadTextAsync(CancellationToken.None);
            Assert.AreEqual(TextClipboardOutcome.Succeeded, read.Outcome);
        }
        TextClipboardResult after = await clipboard.ReadTextAsync(CancellationToken.None);
        Assert.AreEqual(TextClipboardOutcome.Succeeded, after.Outcome);
    }

    // -------------------------------------------------------------------
    // Fixture
    // -------------------------------------------------------------------

    private sealed record PostedReply(string? ErrorCode, string? Message, JsonElement? Payload);

    private sealed class Fixture : IWebReplySink, IWorkspaceRuntimeFactory, IDisposable
    {
        private readonly WorkspaceRegistry _registry;
        private readonly ConcurrentDictionary<string, TaskCompletionSource<PostedReply>> _pending = new();
        private long _sequence;

        private Fixture(string root)
        {
            Root = root;
            _registry = new WorkspaceRegistry(Root);
            Manager = new WorkspaceSessionManager(_registry, this);
            Filter = new WorkspaceSessionEnvelopeFilter(Manager);
            Manager.SetRequestDrainHook(Filter);
            Sta = new StaDispatcherHost();
            Clipboard = new ScriptedClipboard(TextClipboardOutcome.Succeeded);
        }

        private string Root { get; }
        private StaDispatcherHost Sta { get; }
        private WorkspaceRegistryEntryV2? Workspace { get; set; }
        private List<PostedReply> Replies { get; } = [];
        private ITextClipboard Clipboard { get; set; }
        private TimeSpan? Deadline { get; set; }

        public WorkspaceSessionManager Manager { get; }
        public WorkspaceSessionEnvelopeFilter Filter { get; }
        public System.Windows.Threading.Dispatcher StaDispatcher => Sta.Dispatcher;
        public int SuccessCount => Replies.Count(reply => reply.ErrorCode is null);
        public int FailureCount => Replies.Count(reply => reply.ErrorCode is not null);
        public int TotalReplyCount => Replies.Count;
        public string? LastFailureMessage => Replies.LastOrDefault(reply => reply.ErrorCode is not null)?.Message;
        public string? LastFailureCode => Replies.LastOrDefault(reply => reply.ErrorCode is not null)?.ErrorCode;

        public static async Task<Fixture> OpenWithApiAsync(IWindowsTextClipboardApi api, TimeSpan? deadline = null)
        {
            var fixture = new Fixture(Path.Combine(
                Path.GetTempPath(), "vibetable-clipboard-" + Guid.NewGuid().ToString("N")))
            {
                Deadline = deadline,
            };
            await fixture.OpenAsync();
            fixture.Clipboard = new WindowsTextClipboard(fixture.Sta.Dispatcher, api);
            return fixture;
        }

        private async Task OpenAsync()
        {
            var created = WorkspaceLayout.Create(
                Path.Combine(Root, "workspace"), "Clipboard",
                WorkspaceStorageMode.Direct, WorkspaceEncryptionMode.Convenient);
            Workspace = _registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion,
                WorkspaceId = created.Manifest.WorkspaceId,
                DisplayName = "Clipboard",
                SelectedRoot = created.SelectedRoot,
                ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed,
                CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null,
                LastKnownHealth = WorkspaceHealth.Healthy,
                LastSnapshotAt = null,
                LastSyncAt = null,
                PendingSync = false,
            });
            await Manager.OpenAsync(Workspace.WorkspaceId, WorkspaceOpenMode.Writable);
        }

        public WorkspaceWireScope NextScope() => new()
        {
            Scope = "workspace",
            WorkspaceId = Workspace!.WorkspaceId,
            SessionEpoch = Manager.Current.SessionEpoch,
            Sequence = (ulong)Interlocked.Increment(ref _sequence),
            OperationId = Guid.NewGuid(),
        };

        public async Task<JsonElement> RequestAsync(
            string type, string payload,
            string? expectedError = null, WorkspaceWireScope? scope = null,
            ITextClipboard? clipboard = null)
        {
            PostedReply reply = await ReplyAsync(type, payload, scope ?? NextScope(), clipboard);
            Assert.AreEqual(expectedError, reply.ErrorCode);
            if (expectedError is not null) return default;
            Assert.IsNotNull(reply.Payload, reply.ErrorCode);
            return reply.Payload!.Value;
        }

        /// <summary>Dispatches with an exact (possibly null) scope and awaits the reply.</summary>
        public async Task DispatchDirectAsync(
            string type, string payload, WorkspaceWireScope? scope, string expectedError)
        {
            PostedReply reply = await ReplyAsync(type, payload, scope, Clipboard);
            Assert.AreEqual(expectedError, reply.ErrorCode);
        }

        /// <summary>Dispatches and returns the dispatch task (no reply expected).</summary>
        public Task DispatchFireAndForgetAsync(string type, string payload)
            => DispatchAsync(type, payload, NextScope());

        private async Task<PostedReply> ReplyAsync(
            string type, string payload, WorkspaceWireScope? scope, ITextClipboard? clipboard)
        {
            string requestId = Guid.NewGuid().ToString("N");
            var completion = new TaskCompletionSource<PostedReply>(
                TaskCreationOptions.RunContinuationsAsynchronously);
            Assert.IsTrue(_pending.TryAdd(requestId, completion));
            Task dispatch = DispatchAsync(type, payload, scope, requestId, clipboard);
            PostedReply reply = await completion.Task.WaitAsync(TimeSpan.FromSeconds(15));
            await dispatch.WaitAsync(TimeSpan.FromSeconds(15));
            return reply;
        }

        private Task DispatchAsync(
            string type, string payload, WorkspaceWireScope? scope,
            string? requestId = null, ITextClipboard? clipboard = null)
        {
            var request = new RoutedWebRequest(
                type, requestId ?? Guid.NewGuid().ToString("N"),
                JsonDocument.Parse(payload).RootElement.Clone(),
                payload, scope);
            return new ClipboardRequestController(this, Filter, clipboard ?? Clipboard, Deadline)
                .DispatchAsync(request);
        }

        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong sessionEpoch)
            => new Runtime(workspace.WorkspaceId, sessionEpoch);

        public void PostResponse(string type, string? requestId, object? payload)
        {
            var reply = new PostedReply(null, null, JsonSerializer.SerializeToElement(payload));
            Replies.Add(reply);
            Complete(requestId, reply);
        }

        public void PostNotification(string type, object? payload) => Assert.Fail("Unexpected notification.");

        public void PostOperationFailed(
            string? requestId, string message, string? code = null,
            string? operation = null, string? operationId = null)
        {
            var reply = new PostedReply(code, message, null);
            Replies.Add(reply);
            Complete(requestId, reply);
        }

        private void Complete(string? requestId, PostedReply reply)
        {
            Assert.IsNotNull(requestId);
            Assert.IsTrue(_pending.TryGetValue(requestId, out var completion));
            completion.TrySetResult(reply);
        }

        public void Dispose()
        {
            Manager.DisposeAsync().AsTask().GetAwaiter().GetResult();
            Filter.Dispose();
            Sta.Dispose();
            Directory.Delete(Root, recursive: true);
        }
    }

    private sealed class Runtime(Guid workspaceId, ulong sessionEpoch) : IWorkspaceRuntime
    {
        public Guid WorkspaceId => workspaceId;
        public ulong SessionEpoch => sessionEpoch;
        public Task StartAsync(WorkspaceOpenMode mode, WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task VerifyAsync(WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task DrainAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }

    // -------------------------------------------------------------------
    // Fakes
    // -------------------------------------------------------------------

    private sealed class ScriptedClipboard(TextClipboardOutcome outcome) : ITextClipboard
    {
        public Task<TextClipboardResult> ReadTextAsync(CancellationToken token)
            => Task.FromResult(new TextClipboardResult(outcome));
        public Task<TextClipboardResult> WriteTextAsync(string text, CancellationToken token)
            => Task.FromResult(new TextClipboardResult(outcome));
    }

    private sealed class FakeApi : IWindowsTextClipboardApi
    {
        public FakeApi(
            string text = "",
            TextClipboardOutcome outcome = TextClipboardOutcome.Succeeded,
            bool containsText = true)
        {
            Text = text;
            Outcome = outcome;
            ContainsTextFlag = containsText;
        }

        public string Text { get; set; }
        public TextClipboardOutcome Outcome { get; set; }
        public bool ContainsTextFlag { get; }
        public int? ReadThreadId { get; private set; }
        public int? WriteThreadId { get; private set; }
        public string? LastWrittenText { get; private set; }

        public bool ContainsText() => ContainsTextFlag;

        public string GetText()
        {
            ReadThreadId = Environment.CurrentManagedThreadId;
            return Outcome switch
            {
                TextClipboardOutcome.Busy => throw new COMException(
                    "openclipboard failed", unchecked((int)0x800401D0)),
                TextClipboardOutcome.Failed => throw new InvalidOperationException("boom"),
                _ => Text,
            };
        }

        public void SetText(string text)
        {
            WriteThreadId = Environment.CurrentManagedThreadId;
            LastWrittenText = text;
            if (Outcome == TextClipboardOutcome.Busy)
            {
                throw new COMException("openclipboard failed", unchecked((int)0x800401D0));
            }
        }
    }

    private sealed class GatedApi(
        TaskCompletionSource started, TaskCompletionSource release, string text) : IWindowsTextClipboardApi
    {
        public TaskCompletionSource Finished { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
        public int CallCount { get; private set; }
        public int ThreadId { get; private set; }
        public ApartmentState Apartment { get; private set; }
        public string? LastWrittenText { get; private set; }
        public bool ContainsText() => true;
        public string GetText()
        {
            WaitForRelease();
            Finished.TrySetResult();
            return text;
        }
        public void SetText(string value)
        {
            WaitForRelease();
            LastWrittenText = value;
            Finished.TrySetResult();
        }
        private void WaitForRelease()
        {
            CallCount++;
            ThreadId = Environment.CurrentManagedThreadId;
            Apartment = Thread.CurrentThread.GetApartmentState();
            started.TrySetResult();
            release.Task.GetAwaiter().GetResult();
        }
    }

    private sealed class ThrowingApi(Exception exception) : IWindowsTextClipboardApi
    {
        public bool ContainsText() => true;
        public string GetText() => throw exception;
        public void SetText(string text) => throw exception;
    }

    // System clipboard tests require an explicitly isolated, empty test desktop.
    private sealed class ClipboardTextRestore(Dispatcher dispatcher) : IDisposable
    {
        public static async Task<ClipboardTextRestore> CaptureAsync(Dispatcher dispatcher)
        {
            if (Environment.GetEnvironmentVariable("VIBETABLE_NATIVE_CLIPBOARD_TESTS") != "1")
                Assert.Inconclusive("Native clipboard tests require an isolated desktop.");
            await dispatcher.InvokeAsync(() =>
            {
                if (System.Windows.Clipboard.GetDataObject()?.GetFormats().Length > 0)
                    Assert.Inconclusive("The isolated test clipboard must be empty.");
            });
            return new ClipboardTextRestore(dispatcher);
        }

        public void Dispose() => dispatcher.Invoke(System.Windows.Clipboard.Clear);
    }

    /// <summary>Holds the OS clipboard open from a dedicated thread to force CLIPBRD_E_CANT_OPEN.</summary>
    private sealed class ClipboardHolder : IDisposable
    {
        [DllImport("user32.dll", SetLastError = true)]
        private static extern bool OpenClipboard(IntPtr hWndNewOwner);

        [DllImport("user32.dll", SetLastError = true)]
        private static extern bool CloseClipboard();

        private readonly Thread _thread;
        private readonly TaskCompletionSource _opened = new(TaskCreationOptions.RunContinuationsAsynchronously);
        private readonly TaskCompletionSource _release = new(TaskCreationOptions.RunContinuationsAsynchronously);

        private ClipboardHolder()
        {
            _thread = new Thread(() =>
            {
                if (!OpenClipboard(IntPtr.Zero))
                {
                    _opened.TrySetException(new InvalidOperationException("OpenClipboard failed."));
                    return;
                }
                _opened.TrySetResult();
                _release.Task.GetAwaiter().GetResult();
                CloseClipboard();
            });
            _thread.IsBackground = true;
        }

        public static ClipboardHolder Hold()
        {
            var holder = new ClipboardHolder();
            holder._thread.Start();
            holder._opened.Task.Wait(TimeSpan.FromSeconds(2));
            return holder;
        }

        public void Dispose()
        {
            _release.TrySetResult();
            _thread.Join(TimeSpan.FromSeconds(2));
        }
    }

    private sealed class StaDispatcherHost : IDisposable
    {
        private readonly Thread _thread;
        private readonly TaskCompletionSource<Dispatcher> _ready = new(TaskCreationOptions.RunContinuationsAsynchronously);

        public StaDispatcherHost()
        {
            _thread = new Thread(() =>
            {
                Dispatcher dispatcher = Dispatcher.CurrentDispatcher;
                ThreadId = Environment.CurrentManagedThreadId;
                _ready.SetResult(dispatcher);
                Dispatcher.Run();
            });
            _thread.SetApartmentState(ApartmentState.STA);
            _thread.IsBackground = true;
            _thread.Start();
            Dispatcher = _ready.Task.GetAwaiter().GetResult();
        }

        public Dispatcher Dispatcher { get; }
        public int ThreadId { get; private set; }

        public void Shutdown() => Dispatcher.InvokeShutdown();

        public void Dispose()
        {
            Dispatcher.InvokeShutdown();
            _thread.Join(TimeSpan.FromSeconds(5));
        }
    }
}
