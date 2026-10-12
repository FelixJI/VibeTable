using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DashboardNativeOutputControllerTests
{
    private sealed class FakeOutputHost : INativeDashboardOutputHost
    {
        public string? Target { get; set; } = "unused";
        public Exception? PrintFailure { get; set; }
        public Exception? PickerFailure { get; set; }
        public int Dialogs { get; private set; }
        public int PrintDialogs { get; private set; }

        public string? SelectExportTarget(string format, string defaultName)
        {
            Dialogs++;
            if (PickerFailure is not null) throw PickerFailure;
            return Target;
        }

        public void ShowPrintDialog()
        {
            PrintDialogs++;
            if (PrintFailure is not null) throw PrintFailure;
        }
    }

    private sealed class BrokerFixture : IAsyncDisposable
    {
        internal readonly string Root = Path.Combine(
            Path.GetTempPath(), "vibetable-dash-output-" + Guid.NewGuid().ToString("N"));
        internal readonly CancellationTokenSource Epoch = new();
        internal readonly HostSessionFileBroker Broker;
        internal int Retired;
        internal int Leases;
        internal Action? AfterCommit;

        internal BrokerFixture(TimeProvider? time = null)
        {
            Directory.CreateDirectory(Root);
            Broker = new HostSessionFileBroker(
                () =>
                {
                    Epoch.Token.ThrowIfCancellationRequested();
                    Interlocked.Increment(ref Leases);
                    return new WorkspaceRequestEpochLease(
                        new WorkspaceWireScope
                        {
                            Scope = "workspace",
                            WorkspaceId = Guid.NewGuid(),
                            SessionEpoch = 1,
                            OperationId = Guid.NewGuid(),
                            Sequence = 1,
                        },
                        Epoch.Token,
                        () => Interlocked.Decrement(ref Leases));
                },
                action =>
                {
                    Epoch.Token.ThrowIfCancellationRequested();
                    action();
                    AfterCommit?.Invoke();
                }, time);
        }

        internal string TargetPath(string name = "board.png") => Path.Combine(Root, name);

        public async ValueTask DisposeAsync()
        {
            Interlocked.Exchange(ref Retired, 1);
            Broker.Retire();
            try
            {
                await Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(15));
            }
            catch (OperationCanceledException) { }
            Epoch.Dispose();
            Directory.Delete(Root, recursive: true);
        }
    }

    /// <summary>
    /// A real session manager plus envelope filter so stale scope rejection
    /// exercises the same retirement path as production.
    /// </summary>
    private sealed class SessionFixture : IWorkspaceRuntimeFactory, IDisposable
    {
        private readonly string _root = Path.Combine(
            Path.GetTempPath(), "vibetable-dash-session-" + Guid.NewGuid().ToString("N"));
        private readonly WorkspaceRegistry _registry;
        private ulong _sequence;

        internal SessionFixture()
        {
            _registry = new WorkspaceRegistry(_root);
            Manager = new WorkspaceSessionManager(_registry, this);
            Filter = new WorkspaceSessionEnvelopeFilter(Manager);
            Manager.SetRequestDrainHook(Filter);
        }

        internal WorkspaceSessionManager Manager { get; }
        internal WorkspaceSessionEnvelopeFilter Filter { get; }

        internal async Task OpenAsync()
        {
            var created = WorkspaceLayout.Create(Path.Combine(_root, "workspace"), "Dashboard output test",
                WorkspaceStorageMode.Direct, WorkspaceEncryptionMode.Convenient);
            var entry = _registry.Register(new WorkspaceRegistryEntryV2
            {
                ContractVersion = WorkspaceV2Json.ContractVersion, WorkspaceId = created.Manifest.WorkspaceId,
                DisplayName = "Dashboard output test", SelectedRoot = created.SelectedRoot, ActivityRoot = null,
                StorageKind = WorkspaceStorageKind.Fixed, CoordinationStrength = WorkspaceCoordinationStrength.Strong,
                LastOpenedAt = null, LastKnownHealth = WorkspaceHealth.Healthy, LastSnapshotAt = null,
                LastSyncAt = null, PendingSync = false,
            });
            await Manager.OpenAsync(entry.WorkspaceId, WorkspaceOpenMode.Writable);
        }

        internal WorkspaceWireScope CurrentScope()
            => new()
            {
                Scope = "workspace",
                WorkspaceId = Manager.Current.WorkspaceId!.Value,
                SessionEpoch = Manager.Current.SessionEpoch,
                Sequence = ++_sequence,
                OperationId = Guid.NewGuid(),
            };

        public IWorkspaceRuntime Create(WorkspaceRegistryEntryV2 workspace, ulong epoch)
            => new Runtime(workspace.WorkspaceId, epoch);

        public void Dispose()
        {
            Manager.DisposeAsync().AsTask().GetAwaiter().GetResult();
            Filter.Dispose();
            Directory.Delete(_root, recursive: true);
        }
    }

    private sealed class Runtime(Guid id, ulong epoch) : IWorkspaceRuntime
    {
        public Guid WorkspaceId => id;
        public ulong SessionEpoch => epoch;
        public Task StartAsync(WorkspaceOpenMode mode, WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task VerifyAsync(WorkspaceActivationBudget budget) => Task.CompletedTask;
        public Task DrainAsync(CancellationToken token) => Task.CompletedTask;
        public Task StopAsync(CancellationToken token) => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }

    private static RoutedWebRequest Request(
        string type,
        object? payload,
        string requestId = "req-1",
        WorkspaceWireScope? scope = null)
        => new(
            type,
            requestId,
            payload is null
                ? JsonSerializer.SerializeToElement(new { })
                : JsonSerializer.SerializeToElement(payload),
            "{}",
            scope);

    private static object ExportPayload(long? byteLength = 24) => new
    {
        fileName = "sales",
        format = "csv",
        blobUrl = "blob:https://app.vibetable.local/abc123",
        byteLength = byteLength,
    };

    [TestMethod]
    public async Task Prepare_grants_target_without_leaking_the_native_path()
    {
        await using var fixture = new BrokerFixture();
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var host = new FakeOutputHost { Target = fixture.TargetPath("sales.csv") };
        var sink = new FakeWebReplySink();
        var gate = new DashboardExportDownloadGate(_ => { });
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => fixture.Broker, gate);

        await controller.DispatchAsync(Request("dashboard.exportTargetRequested",
            ExportPayload(), scope: sessions.CurrentScope()));

        FakeWebReplySink.Reply? reply =
            await sink.WaitForAsync("dashboard.exportTargetGranted");
        Assert.IsNotNull(reply);
        JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
        string token = payload.GetProperty("token").GetString()!;
        string display = payload.GetProperty("displayName").GetString()!;
        Assert.IsTrue(token.StartsWith("download-"));
        Assert.AreEqual("sales.csv", display);
        Assert.IsFalse(payload.GetRawText().Contains(fixture.Root));
        Assert.AreEqual(1, host.Dialogs);
    }

    [TestMethod]
    public async Task Prepare_without_scope_never_opens_the_native_dialog()
    {
        await using var fixture = new BrokerFixture();
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var host = new FakeOutputHost { Target = fixture.TargetPath("sales.csv") };
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => fixture.Broker, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.exportTargetRequested", ExportPayload()));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", Code(failure));
        Assert.AreEqual(0, host.Dialogs);
    }

    [TestMethod]
    public async Task Prepare_with_a_stale_session_scope_never_opens_the_native_dialog()
    {
        await using var fixture = new BrokerFixture();
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        // A scope captured for an epoch that a workspace switch retired must
        // not trigger native output for the newer workspace.
        WorkspaceWireScope stale = sessions.CurrentScope();
        await sessions.Manager.CloseAsync("switch workspace");
        var host = new FakeOutputHost { Target = fixture.TargetPath("sales.csv") };
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => fixture.Broker, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.exportTargetRequested",
            ExportPayload(), scope: stale));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", Code(failure));
        Assert.AreEqual(0, host.Dialogs);
    }

    [TestMethod]
    public async Task Prepare_replays_the_same_scope_only_once()
    {
        await using var fixture = new BrokerFixture();
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        WorkspaceWireScope scope = sessions.CurrentScope();
        var host = new FakeOutputHost { Target = fixture.TargetPath("sales.csv") };
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => fixture.Broker, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.exportTargetRequested",
            ExportPayload(), scope: scope));
        await sink.WaitForAsync("dashboard.exportTargetGranted");
        // Sequence replay protection refuses the identical envelope.
        await controller.DispatchAsync(Request("dashboard.exportTargetRequested",
            ExportPayload(), requestId: "req-2", scope: scope));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", Code(failure));
        Assert.AreEqual(1, host.Dialogs);
    }

    [TestMethod]
    public async Task Prepare_reports_dialog_cancellation()
    {
        await using var fixture = new BrokerFixture();
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var host = new FakeOutputHost { Target = null };
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => fixture.Broker, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.exportTargetRequested",
            ExportPayload(), scope: sessions.CurrentScope()));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("CANCELLED", Code(failure));
    }

    [TestMethod]
    public async Task Prepare_rejects_invalid_payloads()
    {
        await using var fixture = new BrokerFixture();
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var host = new FakeOutputHost { Target = fixture.TargetPath() };
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => fixture.Broker, new DashboardExportDownloadGate(_ => { }));
        WorkspaceWireScope scope = sessions.CurrentScope();

        await controller.DispatchAsync(Request("dashboard.exportTargetRequested", new
        {
            fileName = "sales",
            format = "xlsx",
            blobUrl = "blob:https://app.vibetable.local/abc123",
            byteLength = 24,
        }, scope: scope));
        await controller.DispatchAsync(Request("dashboard.exportTargetRequested", new
        {
            fileName = "sales",
            format = "csv",
            blobUrl = "blob:https://evil.example/abc123",
            byteLength = 24,
        }, scope: scope));
        await controller.DispatchAsync(Request("dashboard.exportTargetRequested", new
        {
            fileName = "sales",
            format = "csv",
            blobUrl = "blob:https://app.vibetable.local/abc123",
        }, scope: scope));
        await controller.DispatchAsync(Request("dashboard.exportTargetRequested", new
        {
            fileName = "sales",
            format = "csv",
            blobUrl = "blob:https://app.vibetable.local/abc123",
            byteLength = DashboardNativeOutputController.MaxExportBytes + 1,
        }, scope: scope));

        List<FakeWebReplySink.Reply> failures = sink.Replies
            .Where(reply => reply.Type == "operation.failed")
            .ToList();
        Assert.AreEqual(4, failures.Count);
        foreach (FakeWebReplySink.Reply failure in failures.Take(3))
        {
            Assert.AreEqual("BAD_PAYLOAD", Code(failure));
        }
        Assert.AreEqual("EXPORT_TOO_LARGE", Code(failures[3]));
        Assert.AreEqual(0, host.Dialogs);
    }

    [TestMethod]
    public async Task Prepare_reports_missing_broker_binding()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var host = new FakeOutputHost { Target = "C:\\temp\\x.csv" };
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => null, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.exportTargetRequested",
            ExportPayload(), scope: sessions.CurrentScope()));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("BACKEND_UNAVAILABLE", Code(failure));
    }

    [TestMethod]
    public async Task Admitted_download_commits_atomically_and_settles_saved()
    {
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath("board.png");
        await File.WriteAllTextAsync(target, "old");
        object? settled = null;
        var gate = new DashboardExportDownloadGate(payload => settled = payload);
        HostDownloadTarget opened = await fixture.Broker
            .OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc", "new-png".Length));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/abc",
            out DashboardDownloadAdmission? admission));
        Assert.IsNotNull(admission);
        await File.WriteAllTextAsync(opened.TemporaryPath, "new-png");

        await gate.SettleAsync(admission, completed: true, null);

        Assert.AreEqual("new-png", await File.ReadAllTextAsync(target));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        JsonElement payload = JsonSerializer.SerializeToElement(settled);
        Assert.AreEqual(opened.Id, payload.GetProperty("token").GetString());
        Assert.AreEqual("saved", payload.GetProperty("outcome").GetString());
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Blocked_download_keeps_target_and_settles_failed()
    {
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath("board.png");
        await File.WriteAllTextAsync(target, "old");
        object? settled = null;
        var gate = new DashboardExportDownloadGate(payload => settled = payload);
        HostDownloadTarget opened = await fixture.Broker
            .OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc", 7));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/abc",
            out DashboardDownloadAdmission? admission));
        Assert.IsNotNull(admission);
        await File.WriteAllTextAsync(opened.TemporaryPath, "partial");

        await gate.SettleAsync(admission!, completed: false, "FileBlocked");

        Assert.AreEqual("old", await File.ReadAllTextAsync(target));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        JsonElement payload = JsonSerializer.SerializeToElement(settled);
        Assert.AreEqual("failed", payload.GetProperty("outcome").GetString());
        Assert.AreEqual("FileBlocked", payload.GetProperty("reason").GetString());
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Oversized_download_fails_truthfully_and_never_replaces_the_target()
    {
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath("board.png");
        await File.WriteAllTextAsync(target, "old");
        object? settled = null;
        var gate = new DashboardExportDownloadGate(payload => settled = payload);
        HostDownloadTarget opened = await fixture.Broker
            .OpenDownloadTargetAsync(target);
        // The declared length passed the request budget, but WebView2 wrote
        // more bytes than declared: the settle check must fail, not cancel.
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc", 8));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/abc",
            out DashboardDownloadAdmission? admission));
        Assert.IsNotNull(admission);
        await File.WriteAllTextAsync(opened.TemporaryPath, "more-bytes-than-declared");

        await gate.SettleAsync(admission!, completed: true, null);

        Assert.AreEqual("old", await File.ReadAllTextAsync(target));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        JsonElement payload = JsonSerializer.SerializeToElement(settled);
        Assert.AreEqual("failed", payload.GetProperty("outcome").GetString());
        Assert.AreEqual("BYTE_LENGTH_MISMATCH", payload.GetProperty("reason").GetString());
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task Over_budget_download_fails_truthfully(bool completed)
    {
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath("board.png");
        object? settled = null;
        var gate = new DashboardExportDownloadGate(payload => settled = payload);
        HostDownloadTarget opened = await fixture.Broker
            .OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc", 8));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/abc",
            out DashboardDownloadAdmission? admission));
        Assert.IsNotNull(admission);
        // Simulate progress monitoring aborting an unbounded download.
        await File.WriteAllTextAsync(opened.TemporaryPath, "xx");

        Assert.IsTrue(admission!.RecordFailure("EXPORT_TOO_LARGE"));
        Assert.IsFalse(admission.RecordFailure("SECOND_FAILURE"));
        Assert.IsNull(settled);
        Assert.IsTrue(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(1, fixture.Leases);
        await gate.SettleAsync(admission, completed, "UserCanceled");

        Assert.IsFalse(File.Exists(target));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        JsonElement payload = JsonSerializer.SerializeToElement(settled);
        Assert.AreEqual("failed", payload.GetProperty("outcome").GetString());
        Assert.AreEqual("EXPORT_TOO_LARGE", payload.GetProperty("reason").GetString());
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Registration_refuses_declarations_outside_the_budget()
    {
        await using var fixture = new BrokerFixture();
        var gate = new DashboardExportDownloadGate(_ => { });
        HostDownloadTarget opened = await fixture.Broker
            .OpenDownloadTargetAsync(fixture.TargetPath());

        Assert.IsFalse(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc", 0));
        Assert.IsFalse(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc",
            DashboardExportDownloadGate.MaxExportBytes + 1));
        await fixture.Broker.CompleteDownloadAsync(opened.Id, commit: false);
    }

    [TestMethod]
    public async Task Retired_epoch_never_replaces_target_before_commit()
    {
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath("board.png");
        await File.WriteAllTextAsync(target, "old");
        object? settled = null;
        var gate = new DashboardExportDownloadGate(payload => settled = payload);
        HostDownloadTarget opened = await fixture.Broker
            .OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc", 4));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/abc",
            out DashboardDownloadAdmission? admission));
        Assert.IsNotNull(admission);
        await File.WriteAllTextAsync(opened.TemporaryPath, "late");
        // Workspace switch retires the binding while the download is in flight.
        fixture.Epoch.Cancel();

        await gate.SettleAsync(admission!, completed: true, null);

        Assert.AreEqual("old", await File.ReadAllTextAsync(target));
        JsonElement payload = JsonSerializer.SerializeToElement(settled);
        Assert.AreEqual("cancelled", payload.GetProperty("outcome").GetString());
    }

    [TestMethod]
    public async Task Admission_is_one_shot_and_exact()
    {
        await using var fixture = new BrokerFixture();
        var gate = new DashboardExportDownloadGate(_ => { });
        HostDownloadTarget opened = await fixture.Broker
            .OpenDownloadTargetAsync(fixture.TargetPath());
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/abc", 8));

        Assert.IsFalse(gate.TryAdmit("blob:https://app.vibetable.local/other",
            out _));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/abc",
            out DashboardDownloadAdmission? first));
        Assert.IsNotNull(first);
        // The exact URL is consumed; the same URL cannot download twice.
        Assert.IsFalse(gate.TryAdmit("blob:https://app.vibetable.local/abc",
            out _));
        int settledCount = 0;
        var settlementGate = new DashboardExportDownloadGate(_ => settledCount++);
        await settlementGate.SettleAsync(first!, completed: false, "UserCanceled");
        await settlementGate.SettleAsync(first!, completed: false, "UserCanceled");
        Assert.AreEqual(1, settledCount);
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Print_reports_only_that_the_dialog_opened()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var host = new FakeOutputHost();
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => null, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.printRequested", new { },
            scope: sessions.CurrentScope()));
        await controller.DispatchAsync(Request("dashboard.printRequested", new { },
            requestId: "req-2", scope: sessions.CurrentScope()));

        IReadOnlyList<FakeWebReplySink.Reply> replies = await sink.WaitForCountAsync(
            "dashboard.printDialogOpened", 2);
        foreach (FakeWebReplySink.Reply reply in replies)
        {
            Assert.AreEqual(true, JsonSerializer.SerializeToElement(reply.Payload)
                .GetProperty("opened").GetBoolean());
        }
        // Each request triggers one host call; the WebView2 runtime itself
        // refuses to open a second dialog while one is already open.
        Assert.AreEqual(2, host.PrintDialogs);
    }

    [TestMethod]
    public async Task Print_without_scope_or_with_a_stale_scope_never_opens_the_dialog()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        WorkspaceWireScope stale = sessions.CurrentScope();
        await sessions.Manager.CloseAsync("switch workspace");
        var host = new FakeOutputHost();
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => null, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.printRequested", new { }));
        await controller.DispatchAsync(Request("dashboard.printRequested", new { },
            requestId: "req-2", scope: stale));

        List<FakeWebReplySink.Reply> failures = sink.Replies
            .Where(reply => reply.Type == "operation.failed")
            .ToList();
        Assert.AreEqual(2, failures.Count);
        foreach (FakeWebReplySink.Reply failure in failures)
        {
            Assert.AreEqual("BAD_WORKSPACE_SCOPE", Code(failure));
        }
        Assert.AreEqual(0, host.PrintDialogs);
    }

    [TestMethod]
    public async Task Print_failure_is_surfaced_not_swallowed()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var host = new FakeOutputHost
        {
            PrintFailure = new InvalidOperationException("core unavailable"),
        };
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => null, new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.printRequested", new { },
            scope: sessions.CurrentScope()));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("PRINT_FAILED", Code(failure));
        string message = Message(failure);
        Assert.IsTrue(message.Contains("InvalidOperationException"));
    }

    [TestMethod]
    public async Task Print_rejects_non_empty_payloads()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, new FakeOutputHost(), () => null,
            new DashboardExportDownloadGate(_ => { }));

        await controller.DispatchAsync(Request("dashboard.printRequested", new { extra = 1 },
            scope: sessions.CurrentScope()));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("BAD_PAYLOAD", Code(failure));
    }

    [TestMethod]
    public async Task Dashboard_switch_retires_an_admitted_token_and_prevents_late_completion()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath();
        await File.WriteAllTextAsync(target, "old");
        object? report = null;
        var gate = new DashboardExportDownloadGate(payload => report = payload);
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker, "blob:https://app.vibetable.local/old-board", 3));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/old-board", out var admission));
        await File.WriteAllTextAsync(opened.TemporaryPath, "new");
        int cancels = 0;
        Assert.IsTrue(await fixture.Broker.AttachDownloadTerminationAsync(opened.Id, () =>
        { cancels++; return Task.CompletedTask; }));
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(sink, sessions.Filter,
            new FakeOutputHost(), () => fixture.Broker, gate);
        ulong epoch = sessions.Manager.Current.SessionEpoch;

        await controller.DispatchAsync(Request("dashboard.exportRetireRequested", new { token = opened.Id },
            scope: sessions.CurrentScope()));
        var replies = await sink.WaitForCountAsync("dashboard.exportRetired", 1);
        Assert.IsTrue(JsonSerializer.SerializeToElement(replies[0].Payload).GetProperty("retired").GetBoolean());
        Assert.AreEqual(epoch, sessions.Manager.Current.SessionEpoch);
        Assert.AreEqual(1, cancels);
        Assert.IsTrue(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(1, fixture.Leases);

        await gate.SettleAsync(admission!, completed: true, null);
        Assert.AreEqual("old", await File.ReadAllTextAsync(target));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(0, fixture.Leases);
        Assert.AreEqual("cancelled", JsonSerializer.SerializeToElement(report).GetProperty("outcome").GetString());
    }

    [TestMethod]
    public async Task Export_retirement_reports_false_when_the_terminal_commit_already_won()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        await using var fixture = new BrokerFixture();
        var gate = new DashboardExportDownloadGate(_ => { });
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(fixture.TargetPath());
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker, "blob:https://app.vibetable.local/saved", 3));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/saved", out var admission));
        await File.WriteAllTextAsync(opened.TemporaryPath, "new");
        await gate.SettleAsync(admission!, completed: true, null);
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(sink, sessions.Filter,
            new FakeOutputHost(), () => fixture.Broker, gate);

        await controller.DispatchAsync(Request("dashboard.exportRetireRequested", new { token = opened.Id },
            scope: sessions.CurrentScope()));
        var replies = await sink.WaitForCountAsync("dashboard.exportRetired", 1);
        Assert.IsFalse(JsonSerializer.SerializeToElement(replies[0].Payload).GetProperty("retired").GetBoolean());
        Assert.AreEqual("new", await File.ReadAllTextAsync(fixture.TargetPath()));
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Restore_releases_unadmitted_capacity_even_when_retirement_uses_a_new_scope()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var time = new ManualTimeProvider();
        await using var owner = new BrokerFixture(time);
        await using var next = new BrokerFixture(time);
        var reports = new System.Collections.Concurrent.ConcurrentQueue<JsonElement>();
        var reported = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var gate = new DashboardExportDownloadGate(payload =>
        {
            reports.Enqueue(JsonSerializer.SerializeToElement(payload));
            if (reports.Count == 8) reported.TrySetResult();
        });
        HostSessionFileBroker currentBroker = owner.Broker;
        var sink = new FakeWebReplySink();
        var host = new FakeOutputHost { Target = owner.TargetPath() };
        var controller = new DashboardNativeOutputController(sink, sessions.Filter,
            host, () => currentBroker, gate);
        for (int index = 0; index < 8; index++)
            await controller.DispatchAsync(Request("dashboard.exportTargetRequested", new
            {
                fileName = "board", format = "csv",
                blobUrl = $"blob:https://app.vibetable.local/old-{index}", byteLength = 3,
            }, requestId: $"old-{index}", scope: sessions.CurrentScope()));
        var grants = await sink.WaitForCountAsync("dashboard.exportTargetGranted", 8);
        string token = PayloadString(grants[0], "token");
        WorkspaceWireScope oldScope = sessions.CurrentScope();

        owner.Epoch.Cancel();
        await owner.Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
        await sessions.Manager.RestartAfterRestoreAsync(oldScope.WorkspaceId, oldScope.SessionEpoch);
        currentBroker = next.Broker;
        host.Target = next.TargetPath();
        await controller.DispatchAsync(Request("dashboard.exportRetireRequested", new { token },
            requestId: "stale-retirement", scope: oldScope));
        await controller.DispatchAsync(Request("dashboard.exportRetireRequested", new { token },
            requestId: "current-retirement", scope: sessions.CurrentScope()));
        var failures = await sink.WaitForCountAsync("operation.failed", 2);
        Assert.AreEqual("BAD_WORKSPACE_SCOPE", Code(failures[0]));
        Assert.AreEqual("BAD_EXPORT_TOKEN", Code(failures[1]));
        Assert.AreEqual(0, time.ScheduledTimerCount);
        await reported.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.HasCount(8, reports);
        Assert.IsTrue(reports.All(report => report.GetProperty("outcome").GetString() == "cancelled"));
        Assert.AreEqual(0, owner.Leases);
        for (int index = 0; index < 8; index++)
        {
            Assert.IsFalse(gate.TryAdmit($"blob:https://app.vibetable.local/old-{index}", out _));
            await controller.DispatchAsync(Request("dashboard.exportTargetRequested", new
            {
                fileName = "board", format = "csv",
                blobUrl = $"blob:https://app.vibetable.local/new-{index}", byteLength = 3,
            }, requestId: $"new-{index}", scope: sessions.CurrentScope()));
        }
        var allGrants = await sink.WaitForCountAsync("dashboard.exportTargetGranted", 16);
        Assert.HasCount(16, allGrants);
        Assert.AreEqual(8, next.Leases);
    }

    [TestMethod]
    public async Task Broker_retirement_only_removes_its_own_unadmitted_registrations()
    {
        var time = new ManualTimeProvider();
        await using var owner = new BrokerFixture(time);
        await using var other = new BrokerFixture(time);
        var reports = new List<JsonElement>();
        var gate = new DashboardExportDownloadGate(payload => reports.Add(JsonSerializer.SerializeToElement(payload)));
        HostDownloadTarget pending = await owner.Broker.OpenDownloadTargetAsync(owner.TargetPath());
        HostDownloadTarget active = await other.Broker.OpenDownloadTargetAsync(other.TargetPath());
        Assert.IsTrue(gate.TryRegister(pending, owner.Broker, "blob:https://app.vibetable.local/retired", 3));
        Assert.IsTrue(gate.TryRegister(active, other.Broker, "blob:https://app.vibetable.local/current", 3));
        await File.WriteAllTextAsync(pending.TemporaryPath, "old");
        await File.WriteAllTextAsync(active.TemporaryPath, "new");

        owner.Broker.Retire();
        await owner.Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsFalse(gate.TryAdmit("blob:https://app.vibetable.local/retired", out _));
        Assert.IsFalse(File.Exists(pending.TemporaryPath));
        Assert.AreEqual(1, time.ScheduledTimerCount);
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/current", out var admission));
        await gate.SettleAsync(admission!, completed: true, null);
        Assert.AreEqual("new", await File.ReadAllTextAsync(other.TargetPath()));
        Assert.AreEqual(0, other.Leases);
        time.Advance(HostSessionFileBroker.DownloadLifetime);
        Assert.HasCount(2, reports);
        Assert.AreEqual("cancelled", reports[0].GetProperty("outcome").GetString());
        Assert.AreEqual("saved", reports[1].GetProperty("outcome").GetString());
    }

    [TestMethod]
    public async Task Broker_lock_retirement_and_late_registration_do_not_block_settlement()
    {
        var time = new ManualTimeProvider();
        await using var fixture = new BrokerFixture(time);
        var reports = new System.Collections.Concurrent.ConcurrentQueue<JsonElement>();
        var reported = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var gate = new DashboardExportDownloadGate(payload =>
        {
            reports.Enqueue(JsonSerializer.SerializeToElement(payload));
            if (reports.Count == 2) reported.TrySetResult();
        });
        HostDownloadTarget pending = await fixture.Broker.OpenDownloadTargetAsync(fixture.TargetPath());
        Assert.IsTrue(gate.TryRegister(pending, fixture.Broker, "blob:https://app.vibetable.local/before-retire", 3));
        await File.WriteAllTextAsync(pending.TemporaryPath, "old");
        // The commit callback still owns the broker semaphore when retirement fires.
        fixture.AfterCommit = fixture.Broker.Retire;
        HostDownloadTarget late = await fixture.Broker.OpenDownloadTargetAsync(fixture.TargetPath("late.csv"))
            .WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsFalse(gate.TryRegister(late, fixture.Broker, "blob:https://app.vibetable.local/after-retire", 3));
        await fixture.Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
        await reported.Task.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.AreEqual(0, fixture.Leases);
        Assert.AreEqual(0, time.ScheduledTimerCount);
        Assert.IsFalse(File.Exists(pending.TemporaryPath));
        Assert.IsFalse(gate.TryAdmit("blob:https://app.vibetable.local/before-retire", out _));
        Assert.IsFalse(gate.TryAdmit("blob:https://app.vibetable.local/after-retire", out _));
        time.Advance(HostSessionFileBroker.DownloadLifetime);
        Assert.HasCount(2, reports);
        Assert.IsTrue(reports.All(report => report.GetProperty("outcome").GetString() == "cancelled"));
    }

    [TestMethod]
    public async Task Export_retirement_rejects_a_token_from_another_broker()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        await using var owner = new BrokerFixture();
        await using var other = new BrokerFixture();
        var gate = new DashboardExportDownloadGate(_ => { });
        HostDownloadTarget opened = await owner.Broker.OpenDownloadTargetAsync(owner.TargetPath());
        Assert.IsTrue(gate.TryRegister(opened, owner.Broker, "blob:https://app.vibetable.local/foreign", 3));
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(sink, sessions.Filter,
            new FakeOutputHost(), () => other.Broker, gate);
        await controller.DispatchAsync(Request("dashboard.exportRetireRequested", new { token = opened.Id },
            scope: sessions.CurrentScope()));
        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("BAD_EXPORT_TOKEN", Code(failure));
        Assert.AreEqual(1, owner.Leases);
        Assert.AreEqual(true, await gate.RetireAsync(opened.Id, owner.Broker));
        Assert.AreEqual(0, owner.Leases);
    }

    [TestMethod]
    [DataRow("{\"token\":\"download-unknown\",\"path\":\"C:\\\\unowned.csv\"}", "BAD_PAYLOAD")]
    [DataRow("{\"token\":1}", "BAD_PAYLOAD")]
    [DataRow("{\"token\":\"download-unknown\"}", "BAD_EXPORT_TOKEN")]
    public async Task Export_retirement_rejects_unowned_tokens_and_non_token_fields(string json, string code)
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        await using var fixture = new BrokerFixture();
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(sink, sessions.Filter,
            new FakeOutputHost(), () => fixture.Broker, new DashboardExportDownloadGate(_ => { }));
        await controller.DispatchAsync(Request("dashboard.exportRetireRequested",
            JsonSerializer.Deserialize<JsonElement>(json), scope: sessions.CurrentScope()));
        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual(code, Code(failure));
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Expired_commit_before_the_failure_callback_still_cleans_the_terminal_writer()
    {
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath();
        await File.WriteAllTextAsync(target, "old");
        object? report = null;
        var gate = new DashboardExportDownloadGate(payload => report = payload);
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker, "blob:https://app.vibetable.local/interleaved", 3));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/interleaved", out var admission));
        await File.WriteAllTextAsync(opened.TemporaryPath, "new");
        Assert.IsTrue(await fixture.Broker.AttachDownloadTerminationAsync(opened.Id, () => Task.CompletedTask));

        await fixture.Broker.ExpireDownloadAsync(opened.Id, () =>
        {
            // Expire released the broker gate but has not recorded the failure yet.
            Assert.IsNull(admission!.FailureReason);
            Assert.IsTrue(File.Exists(opened.TemporaryPath));
            gate.SettleAsync(admission, completed: true, null).GetAwaiter().GetResult();
            admission.RecordFailure("SAVE_TIMEOUT");
        });

        Assert.AreEqual("old", await File.ReadAllTextAsync(target));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(0, fixture.Leases);
        Assert.AreEqual("SAVE_TIMEOUT", JsonSerializer.SerializeToElement(report).GetProperty("reason").GetString());
        fixture.Broker.Retire();
        await fixture.Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
    }

    [TestMethod]
    public async Task Picker_exception_returns_failure_and_releases_request_lease()
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        await using var fixture = new BrokerFixture();
        var sink = new FakeWebReplySink();
        var controller = new DashboardNativeOutputController(sink, sessions.Filter,
            new FakeOutputHost { PickerFailure = new System.Runtime.InteropServices.COMException() },
            () => fixture.Broker, new DashboardExportDownloadGate(_ => { }));
        await controller.DispatchAsync(Request("dashboard.exportTargetRequested",
            ExportPayload(), scope: sessions.CurrentScope()));
        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("PATH_GRANT_FAILED", Code(failure));
        Assert.AreEqual(0, fixture.Leases);
        await sessions.Manager.CloseAsync("picker failed").WaitAsync(TimeSpan.FromSeconds(2));
    }

    [TestMethod]
    public async Task Host_deadline_blocks_late_save_but_waits_for_terminal_cleanup()
    {
        var time = new ManualTimeProvider();
        await using var fixture = new BrokerFixture(time);
        string target = fixture.TargetPath();
        await File.WriteAllTextAsync(target, "old");
        var reports = new List<JsonElement>();
        var gate = new DashboardExportDownloadGate(payload => reports.Add(JsonSerializer.SerializeToElement(payload)));
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker, "blob:https://app.vibetable.local/deadline", 3));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/deadline", out var admission));
        await File.WriteAllTextAsync(opened.TemporaryPath, "new");
        int cancellations = 0;
        Assert.IsTrue(await fixture.Broker.AttachDownloadTerminationAsync(opened.Id, () =>
        { cancellations++; return Task.CompletedTask; }));

        time.Advance(HostSessionFileBroker.DownloadLifetime);
        Assert.AreEqual(1, cancellations);
        Assert.HasCount(1, reports);
        Assert.AreEqual("failed", reports[0].GetProperty("outcome").GetString());
        Assert.AreEqual("SAVE_TIMEOUT", reports[0].GetProperty("reason").GetString());
        Assert.IsTrue(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(1, fixture.Leases);
        await Assert.ThrowsAsync<TimeoutException>(() => fixture.Broker.CompleteDownloadAsync(opened.Id, true));
        Assert.AreEqual("old", await File.ReadAllTextAsync(target));

        await gate.SettleAsync(admission!, completed: true, null);
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(0, fixture.Leases);
        Assert.HasCount(1, reports);
        Assert.AreEqual(0, time.ScheduledTimerCount);
    }

    [TestMethod]
    public async Task Host_deadline_during_retirement_never_cleans_an_admitted_writer()
    {
        var time = new ManualTimeProvider();
        await using var fixture = new BrokerFixture(time);
        object? report = null;
        var gate = new DashboardExportDownloadGate(payload => report = payload);
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(fixture.TargetPath());
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker, "blob:https://app.vibetable.local/draining", 3));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/draining", out var admission));
        await File.WriteAllTextAsync(opened.TemporaryPath, "new");
        var cancelled = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        Assert.IsTrue(await fixture.Broker.AttachDownloadTerminationAsync(opened.Id, () => cancelled.Task));
        fixture.Broker.Retire();
        time.Advance(HostSessionFileBroker.DownloadLifetime);

        Assert.AreEqual("SAVE_TIMEOUT", JsonSerializer.SerializeToElement(report).GetProperty("reason").GetString());
        Assert.IsTrue(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(1, fixture.Leases);
        cancelled.SetResult();
        await gate.SettleAsync(admission!, completed: false, "UserCanceled");
        await fixture.Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Unadmitted_target_expires_and_cannot_start_a_late_download()
    {
        var time = new ManualTimeProvider();
        await using var fixture = new BrokerFixture(time);
        object? report = null;
        var gate = new DashboardExportDownloadGate(payload => report = payload);
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(fixture.TargetPath());
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker, "blob:https://app.vibetable.local/unadmitted", 3));
        time.Advance(HostSessionFileBroker.DownloadLifetime);
        Assert.AreEqual("SAVE_TIMEOUT", JsonSerializer.SerializeToElement(report).GetProperty("reason").GetString());
        Assert.IsFalse(gate.TryAdmit("blob:https://app.vibetable.local/unadmitted", out _));
        Assert.IsFalse(await fixture.Broker.AttachDownloadTerminationAsync(opened.Id, () => Task.CompletedTask));
        Assert.AreEqual(0, fixture.Leases);
        Assert.AreEqual(0, time.ScheduledTimerCount);
    }

    [TestMethod]
    public async Task Failed_target_replace_releases_the_lease_and_reports_failure()
    {
        await using var fixture = new BrokerFixture();
        string target = fixture.TargetPath("board.csv");
        await File.WriteAllTextAsync(target, "old");
        object? settled = null;
        var gate = new DashboardExportDownloadGate(payload => settled = payload);
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(target);
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/locked", 3));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/locked", out var admission));
        await File.WriteAllTextAsync(opened.TemporaryPath, "new");
        using (File.Open(target, FileMode.Open, FileAccess.Read, FileShare.Read))
        {
            await gate.SettleAsync(admission!, completed: true, null);
        }

        JsonElement payload = JsonSerializer.SerializeToElement(settled);
        Assert.AreEqual("failed", payload.GetProperty("outcome").GetString());
        Assert.IsTrue(payload.GetProperty("reason").GetString()
            is "IOException" or "UnauthorizedAccessException");
        Assert.AreEqual("old", await File.ReadAllTextAsync(target));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
        Assert.AreEqual(0, fixture.Leases);
    }

    [TestMethod]
    public async Task Failed_temporary_cleanup_reports_failure_and_releases_the_lease()
    {
        await using var fixture = new BrokerFixture();
        object? settled = null;
        var gate = new DashboardExportDownloadGate(payload => settled = payload);
        HostDownloadTarget opened = await fixture.Broker.OpenDownloadTargetAsync(fixture.TargetPath());
        Assert.IsTrue(gate.TryRegister(opened, fixture.Broker,
            "blob:https://app.vibetable.local/locked-temp", 3));
        Assert.IsTrue(gate.TryAdmit("blob:https://app.vibetable.local/locked-temp", out var admission));
        await File.WriteAllTextAsync(opened.TemporaryPath, "new");
        using (File.Open(opened.TemporaryPath, FileMode.Open, FileAccess.ReadWrite, FileShare.None))
        {
            await gate.SettleAsync(admission!, completed: false, "UserCanceled");
            Assert.IsTrue(File.Exists(opened.TemporaryPath));
            Assert.AreEqual(0, fixture.Leases);
        }

        JsonElement payload = JsonSerializer.SerializeToElement(settled);
        Assert.AreEqual("failed", payload.GetProperty("outcome").GetString());
        Assert.IsTrue(payload.GetProperty("reason").GetString()
            is "IOException" or "UnauthorizedAccessException");
        fixture.Broker.Retire();
        await fixture.Broker.DrainCompletion.WaitAsync(TimeSpan.FromSeconds(2));
        Assert.IsFalse(File.Exists(opened.TemporaryPath));
    }

    [TestMethod]
    [DataRow("null")]
    [DataRow("[]")]
    [DataRow("1")]
    [DataRow("\"print\"")]
    public async Task Print_rejects_non_object_payloads(string json)
    {
        using var sessions = new SessionFixture();
        await sessions.OpenAsync();
        var sink = new FakeWebReplySink();
        var host = new FakeOutputHost();
        var controller = new DashboardNativeOutputController(
            sink, sessions.Filter, host, () => null, new DashboardExportDownloadGate(_ => { }));
        await controller.DispatchAsync(Request("dashboard.printRequested",
            JsonSerializer.Deserialize<JsonElement>(json), scope: sessions.CurrentScope()));

        FakeWebReplySink.Reply? failure = await sink.WaitForFailedAsync();
        Assert.IsNotNull(failure);
        Assert.AreEqual("BAD_PAYLOAD", Code(failure));
        Assert.AreEqual(0, host.PrintDialogs);
    }

    private static string? Code(FakeWebReplySink.Reply reply)
        => PayloadString(reply, "code");

    private static string Message(FakeWebReplySink.Reply reply)
        => PayloadString(reply, "message");

    private static string PayloadString(FakeWebReplySink.Reply reply, string name)
    {
        JsonElement payload = JsonSerializer.SerializeToElement(reply.Payload);
        return payload.GetProperty(name).GetString() ?? string.Empty;
    }
}
