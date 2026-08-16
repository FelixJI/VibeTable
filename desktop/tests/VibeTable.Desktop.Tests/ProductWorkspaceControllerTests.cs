using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class ProductWorkspaceControllerTests
{
    private sealed class Fixture : IDisposable
    {
        public Fixture()
        {
            Root = Path.Combine(
                Path.GetTempPath(),
                "vibetable-product-workspace-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(Root);
            Gateway = new FakeTableRpcGateway();
            Reply = new FakeWebReplySink();
            RuntimeFactory = new ProductionWorkspaceRuntimeFactory(
                () => throw new InvalidOperationException(
                    "sidecar template must not be resolved by these tests"),
                () => new BackendLaunchOptions
                {
                    Command = "backend.exe",
                });
            Sessions = new WorkspaceSessionManager(
                new WorkspaceRegistry(Root),
                RuntimeFactory);
            Traces = new List<string>();
            Controller = new ProductWorkspaceController(
                Reply,
                RuntimeFactory,
                Sessions,
                new FixedDatabasePicker("local://workspace/test"),
                new TableWorkspaceService(Gateway),
                new GridStateCoordinator(
                    Gateway,
                    _ => { }),
                () => true,
                () => false,
                () => true,
                message => Traces.Add(message),
                "test-host",
                retryDelay: _ => TimeSpan.Zero);
        }

        public string Root { get; }
        public FakeTableRpcGateway Gateway { get; }
        public FakeWebReplySink Reply { get; }
        public ProductionWorkspaceRuntimeFactory RuntimeFactory { get; }
        public WorkspaceSessionManager Sessions { get; }
        public List<string> Traces { get; }
        public ProductWorkspaceController Controller { get; }

        public void Dispose()
        {
            Controller.Dispose();
            Sessions.DisposeAsync().AsTask().GetAwaiter().GetResult();
            RuntimeFactory.DisposeAsync().AsTask().GetAwaiter().GetResult();
            try
            {
                Directory.Delete(Root, recursive: true);
            }
            catch (IOException)
            {
            }
            catch (UnauthorizedAccessException)
            {
            }
        }
    }

    private sealed class FixedDatabasePicker(string source) : IDatabasePicker
    {
        public Task<string?> PickDatabaseAsync() => Task.FromResult<string?>(source);
    }

    private static DatabaseOpenResult OpenResult() => new(
        ["tbl_attachments"],
        Array.Empty<string>(),
        new Dictionary<string, string>
        {
            ["tbl_attachments"] = "Attachments",
        });

    [TestMethod]
    public async Task OpenAsyncRetriesTransientSidecarRecycleFailures()
    {
        using var fixture = new Fixture();
        int attempts = 0;
        fixture.Gateway.OpenDatabaseOverride = _ =>
        {
            attempts++;
            return attempts < 3
                ? Task.FromException<DatabaseOpenResult>(
                    new InvalidOperationException("sidecar recycling"))
                : Task.FromResult(OpenResult());
        };

        await fixture.Controller.OpenAsync();

        Assert.AreEqual(3, attempts);
        Assert.AreEqual(3, fixture.Gateway.OpenDatabaseCalls.Count);
        var opened = fixture.Reply.Replies.Single(
            reply => reply.Type == "database.opened");
        Assert.IsNotNull(opened.Payload);
        Assert.IsFalse(fixture.Reply.Replies.Any(
            reply => reply.Type == "operation.failed"));
    }

    [TestMethod]
    public async Task OpenAsyncPostsOperationFailedAfterRetryBudget()
    {
        using var fixture = new Fixture();
        fixture.Gateway.OpenDatabaseOverride = _ =>
            Task.FromException<DatabaseOpenResult>(
                new InvalidOperationException("sidecar unavailable"));

        await fixture.Controller.OpenAsync();

        Assert.AreEqual(
            8,
            fixture.Gateway.OpenDatabaseCalls.Count,
            "the retry budget must stay bounded so genuine failures surface");
        var failed = fixture.Reply.Replies.Single(
            reply => reply.Type == "operation.failed");
        Assert.IsNotNull(failed.Payload);
    }
}
