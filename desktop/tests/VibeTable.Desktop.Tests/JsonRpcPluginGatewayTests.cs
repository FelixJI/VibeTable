using System.Text.Json;
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
        var context = new PluginRuntimeCommandContext(
            "vibetable.command-context.v1",
            "project-1",
            "customers",
            [JsonDocument.Parse(""""1"""").RootElement.Clone()],
            null,
            "zh-CN",
            "light",
            "comfortable",
            JsonDocument.Parse("{}").RootElement.Clone(),
            "1.0.0");

        await gateway.ListCatalogAsync(new("project-1"), CancellationToken.None);
        await gateway.InspectInstallAsync(new("project-1", "revision-1", "source-1"), CancellationToken.None);
        await gateway.CommitInstallAsync(new("plan-1", "revision-1"), CancellationToken.None);
        await gateway.ListExternalFlowCandidatesAsync(
            new("project-1", "com.acme.clean", "clean"), CancellationToken.None);
        await gateway.BindExternalFlowAsync(
            new("project-1", "com.acme.clean", "clean", "flow-1", false), CancellationToken.None);
        await gateway.SetEnabledAsync(new("project-1", "com.acme.clean", false), CancellationToken.None);
        await gateway.UpgradeAsync(
            new("project-1", "com.acme.clean", "upgrade-1", "revision-2"), CancellationToken.None);
        await gateway.RollbackAsync(new("project-1", "com.acme.clean"), CancellationToken.None);
        await gateway.UninstallAsync(new("project-1", "com.acme.clean"), CancellationToken.None);
        await gateway.DescribeActionAsync(
            new("project-1", "com.acme.clean", "normalize", context), CancellationToken.None);
        await gateway.StartActionAsync(
            new(
                "project-1",
                "com.acme.clean",
                "normalize",
                context,
                JsonDocument.Parse("""{"trim":true}""").RootElement.Clone()),
            CancellationToken.None);
        await gateway.ResolveInteractionAsync(new("run-1", "i-1", "rejected"), CancellationToken.None);
        await gateway.ResolveFileAsync(new("file-1", @"C:\trusted\output.txt"), CancellationToken.None);
        await gateway.CancelTaskAsync(new("task-1"), CancellationToken.None);
        await gateway.GetTaskAsync(new("task-1"), CancellationToken.None);

        CollectionAssert.AreEqual(
            new[]
            {
                "plugin.listCatalog",
                "plugin.inspectInstall",
                "plugin.commitInstall",
                "plugin.listExternalFlowCandidates",
                "plugin.bindExternalFlow",
                "plugin.setEnabled",
                "plugin.upgrade",
                "plugin.rollback",
                "plugin.uninstall",
                "plugin.describeAction",
                "plugin.startAction",
                "plugin.resolveInteraction",
                "plugin.resolveFile",
                "plugin.cancelTask",
                "plugin.getTask",
            },
            transport.Methods);
        Assert.IsFalse(transport.SerializedRequests.Contains("rpc.invoke", StringComparison.Ordinal));
        Assert.AreEqual(
            "source-1",
            transport.Requests[1].GetProperty("params").GetProperty("sourceLocation").GetString());
        Assert.AreEqual(
            "project-1",
            transport.Requests[10].GetProperty("params").GetProperty("context")
                .GetProperty("projectKey").GetString());
        Assert.IsTrue(
            transport.Requests[10].GetProperty("params").GetProperty("input")
                .GetProperty("trim").GetBoolean());
        Assert.AreEqual(
            "rejected",
            transport.Requests[11].GetProperty("params").GetProperty("decision").GetString());
        Assert.AreEqual(
            @"C:\trusted\output.txt",
            transport.Requests[12].GetProperty("params").GetProperty("selectedPath").GetString());
    }

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
                    """{"planId":"p","projectKey":"project","projectRevision":"1","sourceType":"package","sourceLocation":"package.vtplugin","packageHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","manifest":{"$schema":"vibetable.plugin-manifest.v1","pluginId":"x.test","version":"1","displayName":{},"description":{},"compatibility":{},"permissions":{},"actions":[],"flows":[],"ui":{}},"flowRequirements":[],"schemas":{}}""",
                "plugin.listExternalFlowCandidates" => "[]",
                "plugin.bindExternalFlow" =>
                    """{"projectKey":"project","pluginId":"x.test","logicalFlowId":"flow","ownership":"external","directusFlowUuid":"f","rollbackFlowUuid":null,"rollbackContractVersion":null,"rollbackDefinitionHash":null,"triggerType":"manual","contractVersion":"1","installedDefinitionHash":null,"observedDefinitionHash":"hash","revision":1,"health":"healthy","driftStatus":"not-applicable","lastError":null}""",
                "plugin.uninstall" =>
                    """{"managedFlowsRemoved":0,"externalFlowsUnbound":1,"uninstalled":true,"privateSettingsRetained":true}""",
                "plugin.describeAction" => """{"available":true,"reasons":[]}""",
                "plugin.startAction" or "plugin.cancelTask" or "plugin.getTask" =>
                    """{"taskId":"t","runId":"r","pluginId":"x","pluginVersion":"1.0.0","actionId":"a","projectKey":"p","collection":null,"targetCount":0,"risk":"read","state":"queued","cancelRequested":false,"result":null,"error":null}""",
                "plugin.resolveInteraction" => """{"status":"resolved","decision":"rejected"}""",
                "plugin.resolveFile" => "true",
                _ =>
                    """{"projectKey":"project","pluginId":"x.test","version":"1","packageHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceType":"package","sourceLocation":"package.vtplugin","manifest":{"$schema":"vibetable.plugin-manifest.v1","pluginId":"x.test","version":"1","displayName":{},"description":{},"compatibility":{},"permissions":{},"actions":[],"flows":[],"ui":{}},"flowRequirements":[],"schemas":{},"status":"enabled","disabledReason":null,"revision":1}""",
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
