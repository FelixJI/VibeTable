using System.Text.Json;

namespace VibeTable.Contracts.Tests;

[TestClass]
public sealed class PluginContractsTests
{
    private static readonly JsonSerializerOptions Options =
        new(JsonSerializerDefaults.Web);

    [TestMethod]
    public void PluginSafeErrorFixture_DeserializesVersionedContract()
    {
        const string json = """
        {
          "contract": "vibetable.plugin-error.v1",
          "code": "flow_unbound",
          "message": "Local worker is unavailable",
          "recoverability": "rebind",
          "pluginId": "com.acme.clean",
          "actionId": "clean",
          "runId": null,
          "details": {},
          "causeId": "cause-1"
        }
        """;

        var error = JsonSerializer.Deserialize<PluginRuntimeSafeError>(json, Options);

        Assert.IsNotNull(error);
        Assert.AreEqual("vibetable.plugin-error.v1", error!.Contract);
        Assert.AreEqual("flow_unbound", error.Code);
        Assert.AreEqual("rebind", error.Recoverability);
    }

    [TestMethod]
    public void PluginSurfaceMessages_PinContractThemeLocaleAndDensity()
    {
        const string eventJson = """
        {"contract":"vibetable.plugin-surface.v1","surfaceToken":"surface-1","event":"ready","payload":{}}
        """;
        var surfaceEvent = JsonSerializer.Deserialize<PluginSurfaceEvent>(eventJson, Options);

        Assert.IsNotNull(surfaceEvent);
        Assert.AreEqual(PluginContractVersions.Surface, surfaceEvent!.Contract);
        Assert.AreEqual(PluginSurfaceEvents.Ready, surfaceEvent.Event);

        var theme = new PluginSurfaceThemeSnapshot(
            PluginContractVersions.Theme,
            PluginThemeModes.Dark,
            "zh-CN",
            PluginDensityModes.Compact,
            new Dictionary<string, string> { ["--vt-plugin-bg"] = "#111111" });
        using var document = JsonDocument.Parse(JsonSerializer.Serialize(theme, Options));

        Assert.AreEqual("dark", document.RootElement.GetProperty("mode").GetString());
        Assert.AreEqual("zh-CN", document.RootElement.GetProperty("locale").GetString());
        Assert.AreEqual("compact", document.RootElement.GetProperty("density").GetString());
    }
}
