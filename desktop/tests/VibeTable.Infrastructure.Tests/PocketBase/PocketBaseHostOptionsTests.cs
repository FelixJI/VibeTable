using System.Security.Cryptography;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Infrastructure.Tests.PocketBase;

[TestClass]
public sealed class PocketBaseHostOptionsTests
{
    [TestMethod]
    public void Resolve_UsesPackagedIdentityAndSeparatesMutableData()
    {
        string root = CreateDirectory();
        string sidecar = Path.Combine(root, "resources", "sidecar");
        Directory.CreateDirectory(sidecar);
        File.WriteAllBytes(
            Path.Combine(sidecar, "vibetable-pb.exe"),
            [1, 2, 3]);
        File.WriteAllText(
            Path.Combine(sidecar, "build-info.json"),
            """
            {
              "contractVersion": "v1",
              "pocketBaseVersion": "0.39.9",
              "schemaVersion": "3",
              "migrationHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
            }
            """);
        string local = CreateDirectory();

        PocketBaseLaunchOptions options =
            PocketBaseHostOptions.Resolve(root, local);

        Assert.AreEqual(
            Path.Combine(sidecar, "vibetable-pb.exe"),
            options.ExecutablePath);
        Assert.AreEqual(
            Path.Combine(local, "VibeTable", "data", "pocketbase"),
            options.DataDirectory);
        Assert.IsFalse(options.DevelopmentMode);
        Assert.AreEqual(
            TimeSpan.FromSeconds(60),
            options.StartupTimeout,
            "Packaged cold start must extend beyond the legacy 30-second boundary.");
        Assert.AreEqual(
            new string('a', 64),
            options.ExpectedIdentity!.MigrationHash);
    }

    [TestMethod]
    public void Resolve_DevelopmentIdentityHashesMigrationManifest()
    {
        string root = CreateDirectory();
        File.WriteAllText(Path.Combine(root, "pyproject.toml"), "[project]");
        Directory.CreateDirectory(Path.Combine(root, "backend"));
        string build = Path.Combine(root, "build", "dev");
        Directory.CreateDirectory(build);
        File.WriteAllBytes(
            Path.Combine(build, "vibetable-pb.exe"),
            [1, 2, 3]);
        string migrations = Path.Combine(root, "sidecar", "migrations");
        Directory.CreateDirectory(migrations);
        byte[] manifest = """{"schemaVersion":3}"""u8.ToArray();
        File.WriteAllBytes(Path.Combine(migrations, "manifest.json"), manifest);

        PocketBaseLaunchOptions options =
            PocketBaseHostOptions.Resolve(root, CreateDirectory());

        Assert.IsTrue(options.DevelopmentMode);
        Assert.AreEqual(
            PocketBaseLaunchOptions.DefaultStartupTimeout,
            options.StartupTimeout,
            "Source development keeps the strict default startup policy.");
        Assert.AreEqual(
            Convert.ToHexString(SHA256.HashData(manifest)).ToLowerInvariant(),
            options.ExpectedIdentity!.MigrationHash);
        Assert.AreEqual("3", options.ExpectedIdentity.SchemaVersion);
    }

    [TestMethod]
    public void Resolve_RejectsInvalidDevelopmentSchemaVersion()
    {
        string root = CreateDirectory();
        File.WriteAllText(Path.Combine(root, "pyproject.toml"), "[project]");
        Directory.CreateDirectory(Path.Combine(root, "backend"));
        string build = Path.Combine(root, "build", "dev");
        Directory.CreateDirectory(build);
        File.WriteAllBytes(
            Path.Combine(build, "vibetable-pb.exe"),
            [1, 2, 3]);
        string migrations = Path.Combine(root, "sidecar", "migrations");
        Directory.CreateDirectory(migrations);
        File.WriteAllText(
            Path.Combine(migrations, "manifest.json"),
            """{"schemaVersion":0}""");

        Assert.ThrowsExactly<InvalidDataException>(
            () => PocketBaseHostOptions.Resolve(root, CreateDirectory()));
    }

    [TestMethod]
    public void WithRuntimeDataRoot_IsolatesWritablePathsAndPreservesIdentity()
    {
        string root = CreateDirectory();
        string sidecar = Path.Combine(root, "resources", "sidecar");
        Directory.CreateDirectory(sidecar);
        File.WriteAllBytes(
            Path.Combine(sidecar, "vibetable-pb.exe"),
            [1, 2, 3]);
        File.WriteAllText(
            Path.Combine(sidecar, "build-info.json"),
            """
            {
              "contractVersion": "v1",
              "pocketBaseVersion": "0.39.9",
              "schemaVersion": "3",
              "migrationHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
            }
            """);
        PocketBaseLaunchOptions original =
            PocketBaseHostOptions.Resolve(root, CreateDirectory());
        string runtimeRoot = CreateDirectory();

        PocketBaseLaunchOptions isolated =
            PocketBaseHostOptions.WithRuntimeDataRoot(original, runtimeRoot);

        Assert.AreEqual(
            Path.Combine(runtimeRoot, "pocketbase"),
            isolated.DataDirectory);
        Assert.AreEqual(
            Path.Combine(runtimeRoot, "logs", "pocketbase.log"),
            isolated.LogPath);
        Assert.AreEqual(original.ExecutablePath, isolated.ExecutablePath);
        Assert.AreSame(original.ExpectedIdentity, isolated.ExpectedIdentity);
    }

    [TestMethod]
    public void WithRuntimeDataRoot_RejectsDataNestedInsideSidecarInstall()
    {
        string root = CreateDirectory();
        string sidecar = Path.Combine(root, "resources", "sidecar");
        Directory.CreateDirectory(sidecar);
        var options = new PocketBaseLaunchOptions
        {
            ExecutablePath = Path.Combine(sidecar, "vibetable-pb.exe"),
            WorkingDirectory = sidecar,
            DataDirectory = CreateDirectory(),
        };

        Assert.ThrowsExactly<InvalidOperationException>(() =>
            PocketBaseHostOptions.WithRuntimeDataRoot(
                options,
                Path.Combine(sidecar, "runtime")));
    }

    [TestMethod]
    [DataRow("0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")]
    [DataRow("-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")]
    [DataRow("03", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")]
    [DataRow("x", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")]
    [DataRow("3", "short")]
    [DataRow("3", "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg")]
    public void Resolve_RejectsMalformedPackagedIdentity(
        string schemaVersion,
        string migrationHash)
    {
        string root = CreateDirectory();
        string sidecar = Path.Combine(root, "resources", "sidecar");
        Directory.CreateDirectory(sidecar);
        File.WriteAllBytes(
            Path.Combine(sidecar, "vibetable-pb.exe"),
            [1, 2, 3]);
        File.WriteAllText(
            Path.Combine(sidecar, "build-info.json"),
            $$"""
            {
              "contractVersion": "v1",
              "pocketBaseVersion": "0.39.9",
              "schemaVersion": "{{schemaVersion}}",
              "migrationHash": "{{migrationHash}}"
            }
            """);

        Assert.ThrowsExactly<InvalidDataException>(
            () => PocketBaseHostOptions.Resolve(root, CreateDirectory()));
    }

    private static string CreateDirectory()
    {
        string path = Path.Combine(
            Path.GetTempPath(),
            "vibetable-host-options-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(path);
        return path;
    }
}
