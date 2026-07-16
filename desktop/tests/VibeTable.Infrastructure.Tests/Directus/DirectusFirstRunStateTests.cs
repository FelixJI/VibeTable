using System;
using System.IO;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

[TestClass]
public sealed class DirectusFirstRunStateTests
{
    [TestMethod]
    public void Inspect_FreshDirectoryNeedsFullWelcome()
    {
        WithTemporaryDirectory(dir =>
        {
            var status = DirectusFirstRunState.Inspect(dir);

            Assert.IsTrue(status.IsFresh);
            Assert.IsTrue(status.NeedsWelcome);
            Assert.IsFalse(status.IsExperienceComplete);
        });
    }

    [TestMethod]
    public void Inspect_BootstrappedWithoutExperienceIsInterrupted()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");

            var status = DirectusFirstRunState.Inspect(dir);

            Assert.IsTrue(status.IsInterrupted);
            Assert.IsTrue(status.NeedsWelcome);
        });
    }

    [TestMethod]
    public void MarkExperienceComplete_SuppressesWelcomeOnWarmStart()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.BootstrapMarker), "ok");
            File.WriteAllText(Path.Combine(dir, DirectusFirstRunState.SchemaMarker), "ok");

            DirectusFirstRunState.MarkExperienceComplete(dir);
            var status = DirectusFirstRunState.Inspect(dir);

            Assert.IsTrue(status.IsExperienceComplete);
            Assert.IsFalse(status.NeedsWelcome);
        });
    }

    private static void WithTemporaryDirectory(Action<string> body)
    {
        string root = Path.Combine(
            Path.GetTempPath(), "vibetable-first-run-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try { body(root); }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { /* best-effort */ }
        }
    }
}
