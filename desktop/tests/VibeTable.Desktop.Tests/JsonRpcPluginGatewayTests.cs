using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading.Channels;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class JsonRpcPluginGatewayTests
{
    [TestMethod]
    public async Task GatewayMapsOnlyFixedPluginUseCasesToPythonRpc()
    {
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcPluginGateway(client);

        await gateway.InspectInstallAsync(
            new("project-1", "revision-1", "source-1", "plan-host-1"), CancellationToken.None);
        await gateway.CommitInstallAsync(
            new("project-1", CommitPlan, "revision-1"), CancellationToken.None);
        await gateway.UpgradeAsync(
            new("project-1", "com.acme.clean", UpgradePlan, "revision-2"), CancellationToken.None);
        await gateway.RollbackAsync(new("project-1", "com.acme.clean"), CancellationToken.None);
        await gateway.UninstallAsync(new("project-1", "com.acme.clean", "record-clean", 1), CancellationToken.None);
        CollectionAssert.AreEqual(
            new[]
            {
                "plugin.inspectInstall",
                "plugin.commitInstall",
                "plugin.upgrade",
                "plugin.rollback",
                "plugin.uninstall",
            },
            transport.Methods);
        Assert.AreEqual("record-clean", transport.Requests[4].GetProperty("params").GetProperty("installationId").GetString());
        Assert.AreEqual(1, transport.Requests[4].GetProperty("params").GetProperty("expectedRevision").GetInt32());
        // plugin.getTask is retired: the WPF host registry answers public
        // task queries. plugin.cancelInstall is retired on the wire: the host
        // owns cancel by taking and disposing the install-plan lease locally.
        Assert.IsFalse(transport.Methods.Contains("plugin.getTask"));
        Assert.IsFalse(transport.Methods.Contains("plugin.cancelInstall"));
        Assert.IsFalse(transport.SerializedRequests.Contains("rpc.invoke", StringComparison.Ordinal));
        Assert.AreEqual(
            "plan-host-1",
            transport.Requests[0].GetProperty("params").GetProperty("planId").GetString());
        Assert.AreEqual(
            "source-1",
            transport.Requests[0].GetProperty("params").GetProperty("sourceLocation").GetString());
        // The private commit payload carries the full plan from the consumed
        // host lease, never the renderer's plan-id-only DTO.
        Assert.AreEqual(
            "plan-host-1",
            transport.Requests[1].GetProperty("params").GetProperty("plan")
                .GetProperty("planId").GetString());
        Assert.AreEqual(
            "package.vtplugin",
            transport.Requests[1].GetProperty("params").GetProperty("plan")
                .GetProperty("sourceLocation").GetString());
        Assert.AreEqual(
            "plan-host-2",
            transport.Requests[2].GetProperty("params").GetProperty("plan")
                .GetProperty("planId").GetString());
        Assert.AreEqual(
            "com.acme.clean",
            transport.Requests[2].GetProperty("params").GetProperty("plan")
                .GetProperty("manifest").GetProperty("pluginId").GetString());

    }

    [TestMethod]
    public async Task ActualInstallAndUpgradeWireMatchesPythonValidatedPlans()
    {
        DirectoryInfo? root = new(AppContext.BaseDirectory);
        while (root is not null && !File.Exists(Path.Combine(root.FullName, "contracts", "v2", "fixtures", "plugin-install-host-plans.json"))) root = root.Parent;
        Assert.IsNotNull(root);
        using var fixture = JsonDocument.Parse(await File.ReadAllTextAsync(Path.Combine(root.FullName, "contracts", "v2", "fixtures", "plugin-install-host-plans.json")));
        foreach (var item in fixture.RootElement.EnumerateArray())
        {
            var expected = item.GetProperty("params");
            var plan = expected.GetProperty("plan").Deserialize<PluginRuntimeInstallPlan>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
            var transport = new AutoRespondTransport();
            await using var client = new JsonRpcClient(transport);
            using var gateway = new JsonRpcPluginGateway(client);
            await gateway.CommitInstallAsync(new("project-1", plan, "revision-1"), CancellationToken.None);
            Assert.IsTrue(JsonNode.DeepEquals(JsonNode.Parse(expected.GetRawText()), JsonNode.Parse(transport.Requests[0].GetProperty("params").GetRawText())), item.GetProperty("name").GetString());
            await gateway.UpgradeAsync(new("project-1", plan.Manifest.PluginId, plan, "revision-1"), CancellationToken.None);
            var upgrade = JsonNode.Parse(expected.GetRawText())!.AsObject();
            upgrade["pluginId"] = plan.Manifest.PluginId;
            Assert.IsTrue(JsonNode.DeepEquals(upgrade, JsonNode.Parse(transport.Requests[1].GetProperty("params").GetRawText())));
        }
    }

    [TestMethod]
    public async Task ActualUninstallWireMatchesPythonValidatedCatalogRequest()
    {
        DirectoryInfo? root = new(AppContext.BaseDirectory);
        while (root is not null && !File.Exists(Path.Combine(root.FullName, "contracts", "v2", "fixtures", "product-rpc-catalog.json"))) root = root.Parent;
        Assert.IsNotNull(root);
        using var fixture = JsonDocument.Parse(await File.ReadAllTextAsync(Path.Combine(root.FullName, "contracts", "v2", "fixtures", "product-rpc-catalog.json")));
        var expected = fixture.RootElement.GetProperty("rpcCases").EnumerateArray()
            .Single(item => item.GetProperty("method").GetString() == "plugin.uninstall").GetProperty("request").GetProperty("params");
        var request = expected.Deserialize<PluginUninstallParams>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcPluginGateway(client);
        await gateway.UninstallAsync(request, CancellationToken.None);
        Assert.IsTrue(JsonNode.DeepEquals(JsonNode.Parse(expected.GetRawText()), JsonNode.Parse(transport.Requests.Single().GetProperty("params").GetRawText())));
    }

    [TestMethod]
    public async Task HostLifecycleExecutionSerializesCreationTargetWithoutPublicRpcRegistration()
    {
        DirectoryInfo? root = new(AppContext.BaseDirectory);
        while (root is not null && !File.Exists(Path.Combine(root.FullName, "contracts", "v2", "fixtures", "plugin-lifecycle-host-executions.json"))) root = root.Parent;
        Assert.IsNotNull(root);
        using var fixture = JsonDocument.Parse(await File.ReadAllTextAsync(Path.Combine(root.FullName, "contracts", "v2", "fixtures", "plugin-lifecycle-host-executions.json")));
        var expected = fixture.RootElement;
        var request = expected.Deserialize<PluginSetEnabledExecutionParams>(new JsonSerializerOptions(JsonSerializerDefaults.Web))!;
        var transport = new AutoRespondTransport();
        await using var client = new JsonRpcClient(transport);
        using var gateway = new JsonRpcPluginGateway(client);
        await gateway.SetEnabledExecutionAsync(request, CancellationToken.None);
        Assert.AreEqual("plugin.setEnabledExecution", transport.Methods.Single());
        Assert.IsTrue(JsonNode.DeepEquals(JsonNode.Parse(expected.GetRawText()), JsonNode.Parse(transport.Requests.Single().GetProperty("params").GetRawText())));
    }

    private static PluginRuntimeInstallPlan Plan(string planId) => new(
        planId,
        "project-1",
        "revision-1",
        "package",
        "package.vtplugin",
        new string('a', 64),
        new PluginRuntimeManifest(
            "vibetable.plugin-manifest.v1",
            "com.acme.clean",
            "1.0.0",
            new Dictionary<string, string>(),
            new Dictionary<string, string>(),
            JsonDocument.Parse("{}").RootElement.Clone(),
            JsonDocument.Parse("{}").RootElement.Clone(),
            [],
            JsonDocument.Parse("{}").RootElement.Clone()),
        new Dictionary<string, IReadOnlyDictionary<string, JsonElement>>());
    private static PluginRuntimeInstallPlan CommitPlan => Plan("plan-host-1");
    private static PluginRuntimeInstallPlan UpgradePlan => Plan("plan-host-2");

    private sealed class AutoRespondTransport : IJsonLineTransport
    {
        private readonly Channel<JsonElement?> _incoming = Channel.CreateUnbounded<JsonElement?>();

        public List<JsonElement> Requests { get; } = [];
        public string[] Methods => Requests
            .Select(request => request.GetProperty("method").GetString()!)
            .ToArray();
        public string SerializedRequests => JsonSerializer.Serialize(Requests);

        public Task<JsonElement?> ReadAsync(CancellationToken cancellationToken)
            => _incoming.Reader.ReadAsync(cancellationToken).AsTask();

        public Task WriteAsync(string line, CancellationToken cancellationToken)
        {
            using var request = JsonDocument.Parse(line);
            var clone = request.RootElement.Clone();
            Requests.Add(clone);
            string id = clone.GetProperty("id").GetString()!;
            string method = clone.GetProperty("method").GetString()!;
            string result = method switch
            {
                "plugin.listCatalog" => "[]",
                "plugin.inspectInstall" =>
                    """{"planId":"p","projectKey":"project","projectRevision":"1","sourceType":"package","sourceLocation":"package.vtplugin","packageHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifest":{"$schema":"vibetable.plugin-manifest.v1","pluginId":"x.test","version":"1","displayName":{},"description":{},"compatibility":{},"permissions":{},"actions":[],"ui":{}},"schemas":{}}""",
                "plugin.cancelInstall" => "true",
                "plugin.uninstall" =>
                    """{"uninstalled":true,"privateSettingsRetained":true}""",
                _ =>
                    """{"projectKey":"project","pluginId":"x.test","version":"1","packageHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceType":"package","sourceLocation":"package.vtplugin","manifest":{"$schema":"vibetable.plugin-manifest.v1","pluginId":"x.test","version":"1","displayName":{},"description":{},"compatibility":{},"permissions":{},"actions":[],"ui":{}},"schemas":{},"status":"enabled","disabledReason":null,"revision":1}""",
            };
            using var response = JsonDocument.Parse(
                $$"""{"jsonrpc":"2.0","id":"{{id}}","result":{{result}}}""");
            _incoming.Writer.TryWrite(response.RootElement.Clone());
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync()
        {
            _incoming.Writer.TryComplete();
            return ValueTask.CompletedTask;
        }
    }
}
