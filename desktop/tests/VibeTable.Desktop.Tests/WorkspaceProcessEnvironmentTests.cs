using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceProcessEnvironmentTests
{
    [TestMethod]
    public void ConfigureScopesChildStateToResolvedWorkspaceDataRoot()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "VibeTable.WorkspaceEnvironment.Tests",
            Guid.NewGuid().ToString("N"),
            "data");
        var environment = new Dictionary<string, string>();

        WorkspaceProcessEnvironment.Configure(environment, root);

        string normalized = Path.GetFullPath(root);
        Assert.AreEqual(normalized, environment["LOCALAPPDATA"]);
        Assert.AreEqual(
            Path.Combine(normalized, "state"),
            environment["VIBETABLE_STATE_DIR"]);
    }
}
