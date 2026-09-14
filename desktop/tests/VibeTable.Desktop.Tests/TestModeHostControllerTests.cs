using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class TestModeHostControllerTests
{
    [TestMethod]
    public async Task OpenControlIsConsumedAndWritesCurrentSessionEpoch()
    {
        using var fixture = new Fixture();
        Guid workspaceId = Guid.NewGuid();
        File.WriteAllText(
            Path.Combine(fixture.Root, "host-open-workspace.request"),
            workspaceId.ToString("D"));
        fixture.Host.Open = (id, _) =>
        {
            fixture.Host.Session = OpenSession(id, 31);
            return Task.CompletedTask;
        };

        JsonElement state = await fixture.WaitForStateAsync("workspace-opened");

        Assert.AreEqual(workspaceId, fixture.Host.OpenedWorkspace);
        Assert.AreEqual(31UL, state.GetProperty("sessionEpoch").GetUInt64());
        Assert.IsFalse(File.Exists(
            Path.Combine(fixture.Root, "host-open-workspace.request")));
    }

    [TestMethod]
    public async Task ReaderDoesNotLoseCompletedWorkspaceState()
    {
        using var fixture = new Fixture();
        fixture.Controller.ReportStartupVisibility(startHidden: false);
        using var reader = File.Open(
            Path.Combine(fixture.Root, "host-lifecycle-state.json"),
            FileMode.Open,
            FileAccess.Read,
            FileShare.Read);
        var blocked = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Host.OnTrace = message =>
        {
            if (!message.StartsWith("TestModeHostController: state write", StringComparison.Ordinal))
                return;
            // Release only after publication actually collides with this reader.
            reader.Dispose();
            blocked.TrySetResult();
        };
        Guid workspaceId = Guid.NewGuid();
        fixture.Host.Open = (id, _) =>
        {
            fixture.Host.Session = OpenSession(id, 31);
            return Task.CompletedTask;
        };
        File.WriteAllText(
            Path.Combine(fixture.Root, "host-open-workspace.request"),
            workspaceId.ToString("D"));

        await blocked.Task.WaitAsync(TimeSpan.FromSeconds(3));
        JsonElement state = await fixture.WaitForStateAsync("workspace-opened");

        Assert.AreEqual(workspaceId, state.GetProperty("workspaceId").GetGuid());
        Assert.AreEqual(31UL, state.GetProperty("sessionEpoch").GetUInt64());
        Assert.AreEqual(1, fixture.Host.ScheduleCalls);
    }

    [TestMethod]
    public async Task PersistentReaderReportsFailureWithoutRepeatingWorkspaceOpen()
    {
        using var fixture = new Fixture();
        fixture.Controller.ReportStartupVisibility(startHidden: false);
        using var reader = File.Open(
            Path.Combine(fixture.Root, "host-lifecycle-state.json"),
            FileMode.Open,
            FileAccess.Read,
            FileShare.Read);
        var rejected = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Host.OnTrace = message =>
        {
            if (message.StartsWith("TestModeHostController: state write rejected:", StringComparison.Ordinal))
                rejected.TrySetResult();
        };
        File.WriteAllText(
            Path.Combine(fixture.Root, "host-open-workspace.request"),
            Guid.NewGuid().ToString("D"));

        await rejected.Task.WaitAsync(TimeSpan.FromSeconds(3));

        Assert.AreEqual(1, fixture.Host.ScheduleCalls);
        Assert.AreEqual(0, Directory.GetFiles(fixture.Root, "*.tmp").Length);
        JsonElement state = await fixture.WaitForStateAsync("visible-startup");
        Assert.IsNull(state.GetProperty("workspaceId").GetString());
    }

    [TestMethod]
    public async Task InvalidOpenControlFailsClosedWithoutCallingWorkspacePort()
    {
        using var fixture = new Fixture();
        File.WriteAllText(
            Path.Combine(fixture.Root, "host-open-workspace.request"),
            "not-a-workspace-id");

        JsonElement state = await fixture.WaitForStateAsync("workspace-open-failed");

        Assert.IsNull(fixture.Host.OpenedWorkspace);
        Assert.AreEqual(
            "workspace ID control is invalid",
            state.GetProperty("error").GetString());
    }

    [TestMethod]
    public async Task CloseControlSchedulesExactlyOneLifecycleAction()
    {
        using var fixture = new Fixture();
        File.WriteAllText(
            Path.Combine(fixture.Root, "host-normal-close.request"),
            string.Empty);

        await WaitUntilAsync(() => fixture.Host.ExitCalls == 1);

        Assert.AreEqual(1, fixture.Host.ScheduleCalls);
        Assert.IsFalse(File.Exists(
            Path.Combine(fixture.Root, "host-normal-close.request")));
    }

    private static async Task WaitUntilAsync(Func<bool> condition)
    {
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(3));
        while (!condition())
            await Task.Delay(20, timeout.Token);
    }

    private static WorkspaceSessionV2 ClosedSession() => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = null,
        SessionEpoch = 0,
        State = WorkspaceSessionState.Closed,
        OpenMode = WorkspaceOpenMode.ReadOnly,
        Writable = false,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };

    private static WorkspaceSessionV2 OpenSession(Guid workspaceId, ulong epoch) => new()
    {
        ContractVersion = WorkspaceV2Json.ContractVersion,
        WorkspaceId = workspaceId,
        SessionEpoch = epoch,
        State = WorkspaceSessionState.OpenedWritable,
        OpenMode = WorkspaceOpenMode.Writable,
        Writable = true,
        Provisional = false,
        Phase = WorkspaceSessionPhase.Idle,
        ErrorCode = null,
    };

    private sealed class Fixture : IDisposable
    {
        public Fixture()
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-test-host-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Host = new FakeHost();
            Controller = new TestModeHostController(Root, Host);
        }

        public string Root { get; }
        public FakeHost Host { get; }
        public TestModeHostController Controller { get; }

        public async Task<JsonElement> WaitForStateAsync(string action)
        {
            string path = Path.Combine(Root, "host-lifecycle-state.json");
            JsonElement state = default;
            await WaitUntilAsync(() =>
            {
                try
                {
                    if (!File.Exists(path)) return false;
                    using JsonDocument document = JsonDocument.Parse(File.ReadAllText(path));
                    state = document.RootElement.Clone();
                    return state.GetProperty("action").GetString() == action;
                }
                catch (IOException)
                {
                    return false;
                }
            });
            return state;
        }

        public void Dispose()
        {
            Controller.Dispose();
            try { Directory.Delete(Root, recursive: true); } catch { }
        }
    }

    private sealed class FakeHost : ITestModeHost
    {
        public bool CanDispatch => true;
        public int ScheduleCalls { get; private set; }
        public int ExitCalls { get; private set; }
        public Guid? OpenedWorkspace { get; private set; }
        public WorkspaceSessionV2 Session { get; set; } = ClosedSession();
        public Func<Guid, CancellationToken, Task> Open { get; set; } =
            (_, _) => Task.CompletedTask;

        public void Schedule(Func<Task> action)
        {
            ScheduleCalls++;
            action().GetAwaiter().GetResult();
        }

        public void RequestExit() => ExitCalls++;
        public void CloseWindow() { }

        public async Task OpenWorkspaceAsync(
            Guid workspaceId,
            CancellationToken cancellationToken)
        {
            OpenedWorkspace = workspaceId;
            await Open(workspaceId, cancellationToken);
        }

        public TestModeHostState CaptureState() => new(false, false, Session);
        public Action<string> OnTrace { get; set; } = _ => { };
        public void Trace(string message) => OnTrace(message);
    }
}
