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
        public void Trace(string message) { }
    }
}
