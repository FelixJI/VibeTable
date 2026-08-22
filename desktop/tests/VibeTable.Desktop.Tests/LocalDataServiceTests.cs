using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class LocalDataServiceTests
{
    [TestMethod]
    public async Task Lifecycle_DelegatesToSupervisorAndReturnsProductStatus()
    {
        var supervisor = new FakePocketBaseSupervisor();
        await using var service = new LocalDataService(supervisor);

        await service.StartAsync(CancellationToken.None);
        LocalDataStatus ready = service.GetStatus();
        await service.StopAsync(CancellationToken.None);

        Assert.AreEqual(1, supervisor.StartCalls);
        Assert.AreEqual(LocalDataState.Ready, ready.State);
        Assert.IsTrue(ready.IsReady);
        Assert.AreEqual(1, supervisor.StopCalls);
    }

    [TestMethod]
    public void OpenAdmin_IsUnavailableEvenIfSupervisorOffersAnUnsafeUri()
    {
        var supervisor = new FakePocketBaseSupervisor
        {
            Status = new PocketBaseStatus(
                PocketBaseState.Ready,
                new Uri("http://127.0.0.1:43125/"),
                AdminAvailable: true,
                ExitCode: null,
                Error: null),
            AdminUri = new Uri("http://127.0.0.1:43125/_/"),
        };
        var launcher = new FakeAdminLauncher();
        var service = new LocalDataService(supervisor, launcher);

        bool opened = service.OpenAdmin();

        Assert.IsFalse(opened);
        Assert.AreEqual(0, launcher.Opened.Count);
    }

    [TestMethod]
    public void GetStatus_DoesNotExposeAddressOrSessionCredential()
    {
        var supervisor = new FakePocketBaseSupervisor
        {
            Status = new PocketBaseStatus(
                PocketBaseState.Ready,
                new Uri("http://127.0.0.1:43125/"),
                AdminAvailable: false,
                ExitCode: null,
                Error: null),
        };
        var service = new LocalDataService(supervisor);

        LocalDataStatus status = service.GetStatus();
        string serialized = System.Text.Json.JsonSerializer.Serialize(status);

        Assert.IsFalse(serialized.Contains("127.0.0.1", StringComparison.Ordinal));
        Assert.IsFalse(serialized.Contains("Secret", StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(status.CanOpenAdmin);
    }

    private sealed class FakeAdminLauncher : ILocalDataAdminLauncher
    {
        public List<Uri> Opened { get; } = [];
        public void Open(Uri uri) => Opened.Add(uri);
    }

    private sealed class FakePocketBaseSupervisor : IPocketBaseSupervisor
    {
        public PocketBaseStatus Status { get; set; } = new(
            PocketBaseState.Stopped, null, false, null, null);
        public Uri? AdminUri { get; set; }
        public int StartCalls { get; private set; }
        public int StopCalls { get; private set; }

        public event Action<object?, PocketBaseStatus>? StatusChanged
        {
            add { }
            remove { }
        }
        public PocketBaseStartupTimings? LastStartupTimings => null;

        public Task StartAsync(CancellationToken cancellationToken)
        {
            StartCalls++;
            Status = new PocketBaseStatus(
                PocketBaseState.Ready,
                new Uri("http://127.0.0.1:43125/"),
                false,
                null,
                null);
            return Task.CompletedTask;
        }

        public PocketBaseStatus GetStatus() => Status;

        public Task StopAsync(CancellationToken cancellationToken)
        {
            StopCalls++;
            Status = new PocketBaseStatus(
                PocketBaseState.Stopped, null, false, null, null);
            return Task.CompletedTask;
        }

        public Uri? GetAdminUri() => AdminUri;
        public PocketBaseAdminContext? GetAdminContext() => null;

        public void ConfigureBackendEnvironment(
            IDictionary<string, string> environment)
        {
        }
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
