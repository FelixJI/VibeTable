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
        public int Dialogs { get; private set; }
        public int PrintDialogs { get; private set; }

        public string? SelectExportTarget(string format, string defaultName)
        {
            Dialogs++;
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

        internal BrokerFixture()
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
                });
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
