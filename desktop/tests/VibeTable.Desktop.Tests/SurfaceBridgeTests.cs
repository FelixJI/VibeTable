using System.Text.Json;
using System.Diagnostics;
using System.Text;
using VibeTable.Contracts.Generated;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class SurfaceBridgeTests
{
    private static readonly string[] RequestTypes =
    [
        "interface.listRequested",
        "interface.loadRequested",
        "interface.commitRequested",
        "interface.deleteRequested",
        "interface.cancelRequested",
    ];

    [TestMethod]
    public void RouterAllowsOnlyNamedInterfaceMessages()
    {
        var dispatched = new List<RoutedWebRequest>();
        var router = new WebMessageRouter(dispatched.Add) { IsReady = true };
        foreach (string type in RequestTypes)
        {
            Assert.IsNull(router.Route(JsonSerializer.Serialize(new
            {
                type,
                requestId = type,
                payload = new { },
            })));
        }
        CollectionAssert.AreEqual(RequestTypes, dispatched.Select(item => item.Type).ToArray());
        foreach (string type in new[]
        {
            "interface.listLoaded", "interface.loaded", "interface.committed", "interface.deleted",
        }) Assert.IsTrue(router.IsHostNotificationAllowed(type), type);
        Assert.IsFalse(router.IsHostNotificationAllowed("interface.rpcResult"));
        Assert.IsNotNull(router.Route(
            """{"type":"interface.rpcRequested","requestId":"raw","payload":{}}"""));
    }

    [TestMethod]
    public async Task DispatcherCorrelatesListLoadCommitAndDelete()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();

        dispatcher.Dispatch(Request("interface.listRequested", "list-1", "{}"));
        Assert.AreEqual("list-1", (await sink.WaitForAsync("interface.listLoaded"))!.RequestId);

        dispatcher.Dispatch(Request(
            "interface.loadRequested", "load-1", """{"interfaceId":"if-orders"}"""));
        Assert.AreEqual("load-1", (await sink.WaitForAsync("interface.loaded"))!.RequestId);
        CollectionAssert.AreEqual(new[] { "if-orders" }, gateway.Loads);

        dispatcher.Dispatch(Request("interface.commitRequested", "commit-1", CommitJson));
        Assert.AreEqual("commit-1", (await sink.WaitForAsync("interface.committed"))!.RequestId);
        Assert.AreEqual("Orders", gateway.Commits.Single().Definition.Name);

        dispatcher.Dispatch(Request("interface.deleteRequested", "delete-1", DeleteJson));
        Assert.AreEqual("delete-1", (await sink.WaitForAsync("interface.deleted"))!.RequestId);
        Assert.AreEqual("if-orders", gateway.Deletes.Single().InterfaceId);
    }

    [TestMethod]
    public async Task DispatcherRejectsUnknownFieldsBeforeGateway()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        dispatcher.Dispatch(Request(
            "interface.loadRequested",
            "invalid-1",
            """{"interfaceId":"if-orders","rpcMethod":"raw.invoke"}"""));

        var failure = await sink.WaitForFailedAsync();
        Assert.AreEqual("invalid-1", failure!.RequestId);
        Assert.AreEqual("BAD_PAYLOAD", ((dynamic)failure.Payload!).code);
        Assert.IsEmpty(gateway.Loads);
    }

    [TestMethod]
    public async Task DispatcherCancellationSuppressesLateResult()
    {
        var (dispatcher, gateway, sink) = CreateDispatcher();
        var completion = new TaskCompletionSource<InterfaceSnapshot>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        gateway.LoadHandler = (_, _) => completion.Task;

        dispatcher.Dispatch(Request(
            "interface.loadRequested", "slow-load", """{"interfaceId":"if-orders"}"""));
        Assert.IsTrue(SpinWait.SpinUntil(() => gateway.Loads.Count == 1, 2_000));
        dispatcher.Dispatch(Request(
            "interface.cancelRequested", "cancel-1", """{"targetRequestId":"slow-load"}"""));

        var failure = await sink.WaitForFailedAsync();
        Assert.AreEqual("slow-load", failure!.RequestId);
        Assert.AreEqual("SURFACE_CANCELLED", ((dynamic)failure.Payload!).code);
        completion.SetResult(Snapshot());
        await Task.Delay(50);
        Assert.IsFalse(sink.Replies.Any(reply =>
            reply.Type == "interface.loaded" && reply.RequestId == "slow-load"));
    }

    [TestMethod]
    public async Task ControllerTimesOutAndRejectsTypesOutsideItsClosedUnion()
    {
        var gateway = new FakeSurfaceGateway
        {
            LoadHandler = async (_, token) =>
            {
                await Task.Delay(Timeout.InfiniteTimeSpan, token);
                return Snapshot();
            },
        };
        var sink = new FakeWebReplySink();
        var controller = new SurfaceRequestController(
            sink,
            TimeSpan.FromMilliseconds(20));
        controller.SetGateway(gateway);

        await controller.DispatchAsync(Request(
            "interface.loadRequested", "timeout-1", """{"interfaceId":"if-orders"}"""));
        var timeout = await sink.WaitForFailedAsync();
        Assert.AreEqual("timeout-1", timeout!.RequestId);
        Assert.AreEqual("SURFACE_TIMEOUT", ((dynamic)timeout.Payload!).code);

        await controller.DispatchAsync(Request("interface.rawRequested", "raw-1", "{}"));
        var unknown = sink.Replies.Last();
        Assert.AreEqual("raw-1", unknown.RequestId);
        Assert.AreEqual("UNKNOWN_TYPE", ((dynamic)unknown.Payload!).code);
    }

    [TestMethod]
    public async Task ControllerLogsOnlySafeFailureMetadata()
    {
        const string SensitiveMessage = @"C:\private\customer-name.txt";
        var gateway = new FakeSurfaceGateway
        {
            LoadHandler = (_, _) => throw new InvalidOperationException(SensitiveMessage),
        };
        var sink = new FakeWebReplySink();
        var controller = new SurfaceRequestController(sink, TimeSpan.FromSeconds(1));
        controller.SetGateway(gateway);
        var listener = new CapturingTraceListener();
        Trace.Listeners.Add(listener);
        try
        {
            await controller.DispatchAsync(Request(
                "interface.loadRequested", "failure-1", """{"interfaceId":"if-orders"}"""));
        }
        finally
        {
            Trace.Listeners.Remove(listener);
        }

        StringAssert.Contains(listener.Text, "SURFACE_OPERATION_FAILED");
        string json = listener.Text[listener.Text.IndexOf('{')..].Trim();
        using JsonDocument logged = JsonDocument.Parse(json);
        Assert.AreEqual(
            "interface.loadRequested",
            logged.RootElement.GetProperty("event").GetString());
        Assert.IsTrue(DiagnosticLogLine.IsSafe(json));
        Assert.IsFalse(listener.Text.Contains(SensitiveMessage, StringComparison.Ordinal));
        var failure = await sink.WaitForFailedAsync();
        Assert.AreEqual("SURFACE_OPERATION_FAILED", ((dynamic)failure!.Payload!).code);
    }

    private static (
        WorkspaceRequestDispatcher Dispatcher,
        FakeSurfaceGateway Gateway,
        FakeWebReplySink Sink) CreateDispatcher()
    {
        var gateway = new FakeSurfaceGateway();
        var sink = new FakeWebReplySink();
        using var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("db"),
            sink,
            NoDatabaseOpenRoute.Instance);
        dispatcher.SetSurfaceGateway(gateway);
        return (dispatcher, gateway, sink);
    }

    private static RoutedWebRequest Request(string type, string requestId, string json)
    {
        using var document = JsonDocument.Parse(json);
        return new RoutedWebRequest(type, requestId, document.RootElement.Clone(), string.Empty);
    }

    private static InterfaceSnapshot Snapshot()
        => new()
        {
            Definition = new InterfaceDefinition
            {
                ContractVersion = "1.0",
                InterfaceId = "if-orders",
                Name = "Orders",
                Bindings = [],
                Actions = [],
                Pages =
                [
                    new InterfacePage { PageId = "list", Title = "Orders", Elements = [] },
                ],
            },
            Revision = Revision,
        };

    private sealed class FakeSurfaceGateway : ISurfaceRpcGateway
    {
        public List<string> Loads { get; } = [];
        public List<InterfaceCommitRequest> Commits { get; } = [];
        public List<InterfaceDeleteRequest> Deletes { get; } = [];
        public Func<string, CancellationToken, Task<InterfaceSnapshot>>? LoadHandler { get; set; }

        public Task<InterfaceListResult> ListAsync(CancellationToken token)
            => Task.FromResult(new InterfaceListResult
            {
                Items =
                [
                    new InterfaceListEntry
                    {
                        InterfaceId = "if-orders",
                        Name = "Orders",
                        Revision = Revision,
                    },
                ],
            });

        public Task<InterfaceSnapshot> LoadAsync(string interfaceId, CancellationToken token)
        {
            Loads.Add(interfaceId);
            return LoadHandler is null ? Task.FromResult(Snapshot()) : LoadHandler(interfaceId, token);
        }

        public Task<InterfaceSnapshot> CommitAsync(
            InterfaceCommitRequest parameters,
            CancellationToken token)
        {
            Commits.Add(parameters);
            return Task.FromResult(Snapshot());
        }

        public Task<InterfaceDeleteResult> DeleteAsync(
            InterfaceDeleteRequest parameters,
            CancellationToken token)
        {
            Deletes.Add(parameters);
            return Task.FromResult(new InterfaceDeleteResult { InterfaceId = parameters.InterfaceId });
        }
    }

    private sealed class CapturingTraceListener : TraceListener
    {
        private readonly StringBuilder _text = new();
        public string Text => _text.ToString();
        public override void Write(string? message) => _text.Append(message);
        public override void WriteLine(string? message) => _text.AppendLine(message);
    }

    private const string Revision =
        "sha256:1111111111111111111111111111111111111111111111111111111111111111";
    private const string CommitJson =
        """{"definition":{"contractVersion":"1.0","interfaceId":"if-orders","name":"Orders","bindings":[],"actions":[],"pages":[{"pageId":"list","title":"Orders","elements":[]}]},"expectedRevision":null,"idempotencyKey":"surface-save-1"}""";
    private const string DeleteJson =
        """{"interfaceId":"if-orders","expectedRevision":"sha256:1111111111111111111111111111111111111111111111111111111111111111","idempotencyKey":"surface-delete-1"}""";
}
