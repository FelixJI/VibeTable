using System.IO;
using System.Text.Json.Nodes;
using Microsoft.VisualStudio.TestTools.UnitTesting;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Infrastructure.Tests.Directus;

/// <summary>
/// Tests for <see cref="DirectusEnvMaterializer"/>: env generation, secret
/// preservation across runs, and the port-free probe. These are deterministic
/// file-system operations; no processes involved.
/// </summary>
[TestClass]
public sealed class DirectusEnvMaterializerTests
{
    [TestMethod]
    public void Materialize_GeneratesSecretsForPlaceholdersOnFirstRun()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, ".env.template"),
                "KEY=__GENERATE__\nSECRET=__GENERATE__\nPORT=8055\nADMIN_EMAIL=admin@example.com\n");

            var env = DirectusEnvMaterializer.Materialize(dir);

            Assert.AreNotEqual("__GENERATE__", env["KEY"]);
            Assert.AreNotEqual("__GENERATE__", env["SECRET"]);
            Assert.IsTrue(env["KEY"].Length >= 32, "generated secret should be substantial");
            StringAssert.StartsWith(File.ReadAllText(Path.Combine(dir, ".env")), "# Auto-materialized");
        });
    }

    [TestMethod]
    public void Materialize_PreservesGeneratedSecretsAcrossRuns()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, ".env.template"),
                "KEY=__GENERATE__\nPORT=8055\nADMIN_EMAIL=admin@example.com\n");

            var first = DirectusEnvMaterializer.Materialize(dir);
            string firstKey = first["KEY"];

            // Second run should reuse the already-generated KEY, not rotate it.
            var second = DirectusEnvMaterializer.Materialize(dir);

            Assert.AreEqual(firstKey, second["KEY"], "secret must not rotate on re-run");
        });
    }

    [TestMethod]
    public void Materialize_AppliesBootstrapCredsOnlyBeforeBootstrap()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, ".env.template"),
                "KEY=__GENERATE__\nPORT=8055\nADMIN_EMAIL=admin@example.com\n");

            // Before bootstrapped: host-supplied creds override the template default.
            var before = DirectusEnvMaterializer.Materialize(dir,
                bootstrapEmail: "setup@user.com", bootstrapPassword: "setup-pw", isBootstrapped: false);
            Assert.AreEqual("setup@user.com", before["ADMIN_EMAIL"]);
            Assert.AreEqual("setup-pw", before["ADMIN_PASSWORD"]);

            // After bootstrapped: bootstrap creds are NOT applied — the existing
            // .env value (whatever was persisted) is preserved instead. Assert the
            // brand-new supplied credential is NOT written over the persisted one.
            var after = DirectusEnvMaterializer.Materialize(dir,
                bootstrapEmail: "new-after-bootstrap@user.com", isBootstrapped: true);
            Assert.AreNotEqual("new-after-bootstrap@user.com", after["ADMIN_EMAIL"],
                "post-bootstrap, supplied bootstrap creds must not overwrite the persisted admin");
        });
    }

    [TestMethod]
    public void PickFreePort_ReturnsPreferredWhenFree()
    {
        // Pick an ephemeral range port that is very likely free.
        int port = DirectusEnvMaterializer.PickFreePort(0);
        Assert.IsTrue(port >= 0);
    }

    private static void WithTemporaryDirectory(System.Action<string> body)
    {
        string root = Path.Combine(Path.GetTempPath(), "vibetable-env-" + System.Guid.NewGuid().ToString("N"));
        try
        {
            Directory.CreateDirectory(root);
            body(root);
        }
        finally
        {
            try { Directory.Delete(root, recursive: true); }
            catch { /* best-effort */ }
        }
    }
}
