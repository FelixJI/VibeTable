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
    public void Materialize_CreatesConfiguredSqliteParentDirectory()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, ".env.template"),
                "KEY=__GENERATE__\nSECRET=__GENERATE__\n"
                + "DB_CLIENT=sqlite3\nDB_FILENAME=./data/directus.sqlite\n"
                + "ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD=__GENERATE__\n");

            DirectusEnvMaterializer.Materialize(dir);

            Assert.IsTrue(Directory.Exists(Path.Combine(dir, "data")));
        });
    }

    [TestMethod]
    public void TryReadBootstrapCredentials_RecoversInterruptedFirstRun()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, ".env"),
                "ADMIN_EMAIL=legacy-admin@example.com\nADMIN_PASSWORD=generated-secret\n");

            bool found = DirectusEnvMaterializer.TryReadBootstrapCredentials(
                dir, out string email, out string password);

            Assert.IsTrue(found);
            Assert.AreEqual("legacy-admin@example.com", email);
            Assert.AreEqual("generated-secret", password);
        });
    }

    [TestMethod]
    public void TryReadBootstrapCredentials_RejectsMissingOrPlaceholderPassword()
    {
        WithTemporaryDirectory(dir =>
        {
            Assert.IsFalse(DirectusEnvMaterializer.TryReadBootstrapCredentials(
                dir, out _, out _));

            File.WriteAllText(Path.Combine(dir, ".env"),
                "ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD=__GENERATE__\n");
            Assert.IsFalse(DirectusEnvMaterializer.TryReadBootstrapCredentials(
                dir, out _, out _));
        });
    }

    [TestMethod]
    public void PickFreePort_ReturnsPreferredWhenFree()
    {
        // Pick an ephemeral range port that is very likely free.
        int port = DirectusEnvMaterializer.PickFreePort(0);
        Assert.IsTrue(port >= 0);
    }

    [TestMethod]
    public void Materialize_WritesLoopbackHostAndSessionCookieConfig()
    {
        WithTemporaryDirectory(dir =>
        {
            File.WriteAllText(Path.Combine(dir, ".env.template"),
                "KEY=__GENERATE__\nSECRET=__GENERATE__\nPORT=49152\n"
                + "ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD=__GENERATE__\n");

            var env = DirectusEnvMaterializer.Materialize(dir);

            Assert.AreEqual("127.0.0.1", env["HOST"],
                "Directus must bind loopback to close the default 0.0.0.0 exposure");
            Assert.AreEqual("7d", env["SESSION_COOKIE_TTL"],
                "long TTL so the injected session survives a long-running app session");
            Assert.AreEqual("lax", env["SESSION_COOKIE_SAME_SITE"],
                "lax avoids cross-site cookie drop on localhost");
        });
    }

    [TestMethod]
    public void DefaultPort_IsInHighEphemeralRange()
    {
        // The constant must move off the well-known 8055 to the IANA ephemeral range.
        // These assert against `public const int` fields; the MSTest analyzer
        // constant-folds the comparisons (MSTEST0025 when they'd be false,
        // MSTEST0032 when true) and this project sets TreatWarningsAsErrors +
        // AnalysisLevel=latest, so they surface as build errors. Suppress locally:
        // these are deliberate regression guards against someone bumping the
        // port/range constants back out of the ephemeral range.
#pragma warning disable MSTEST0025, MSTEST0032
        Assert.AreEqual(49152, DirectusEnvMaterializer.DefaultPort);
        Assert.IsTrue(DirectusEnvMaterializer.PortProbeRangeStart >= 49152,
            "probe range must start in the ephemeral range");
        Assert.IsTrue(DirectusEnvMaterializer.PortProbeRangeEnd <= 49152 + 50 + 1,
            "probe range must be within +50 of the default");
#pragma warning restore MSTEST0025, MSTEST0032
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
