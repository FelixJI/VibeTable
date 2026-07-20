using System.IO;
using System.Text.Json;

namespace VibeTable.Contracts.Tests;

[TestClass]
public sealed class PluginPlatformFixtureTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void PluginPlatformFixtureDeserializesResultInteractionTaskAndSafeError()
    {
        string path = Path.Combine(
            AppContext.BaseDirectory,
            "fixtures",
            "plugin-platform-v1.json");
        using var document = JsonDocument.Parse(File.ReadAllText(path));
        var root = document.RootElement;

        var manifest = root.GetProperty("manifest").Deserialize<PluginRuntimeManifest>(Options);
        var result = root.GetProperty("result").Deserialize<PluginRuntimeResult>(Options);
        var interaction = root.GetProperty("interaction")
            .Deserialize<PluginRuntimeInteractionSnapshot>(Options);
        var task = root.GetProperty("task").Deserialize<PluginRuntimeTaskSnapshot>(Options);
        var error = root.GetProperty("error").Deserialize<PluginRuntimeSafeError>(Options);
        var envelope = root.GetProperty("event").Deserialize<PluginEventEnvelope>(Options);

        Assert.IsNotNull(manifest);
        Assert.AreEqual("vibetable.plugin-manifest.v1", manifest!.Schema);
        Assert.AreEqual("show-summary", manifest.Actions.Single().ActionId);
        Assert.IsNotNull(result);
        Assert.AreEqual(PluginContractVersions.Result, result!.Contract);
        Assert.IsNotNull(interaction);
        Assert.AreEqual("run-1", interaction!.RunId);
        Assert.IsNotNull(task);
        Assert.AreEqual("task-1", task!.TaskId);
        Assert.AreEqual("run-1", task.RunId);
        Assert.IsNotNull(error);
        Assert.AreEqual("vibetable.plugin-error.v1", error!.Contract);
        Assert.IsNotNull(envelope);
        Assert.AreEqual(PluginContractVersions.Event, envelope!.Contract);
        Assert.IsTrue(envelope.Revision >= 1);
    }
}
