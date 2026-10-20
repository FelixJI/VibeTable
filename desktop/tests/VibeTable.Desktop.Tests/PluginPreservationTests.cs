using System.IO;
using System.IO.Compression;
using System.Text;
using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class PluginPreservationTests
{
    private const string RecordId = "oldplugin000001";
    private static Func<string, int, string, CancellationToken, Task<JsonElement>> Pages(byte[] record, bool badCursor = false)
        => (after, offset, snapshot, token) =>
        {
            token.ThrowIfCancellationRequested();
            Assert.AreEqual(after == "" && offset == 0 ? "" : "12", snapshot);
            bool terminal = after == RecordId;
            int count = terminal ? 0 : Math.Min(LegacyLayoutRequestController.PageBytes, record.Length - offset);
            bool done = !terminal && offset + count == record.Length;
            return Task.FromResult(JsonSerializer.SerializeToElement(new {
                contractVersion = "2.0", snapshotRevision = "12", recordId = terminal ? "" : RecordId, revision = "7",
                chunk = terminal ? Array.Empty<byte>() : record.AsSpan(offset, count).ToArray(),
                recordDone = done, collectionDone = terminal,
                nextAfterRecordId = done ? RecordId : after,
                nextRecordOffset = terminal || done || badCursor ? 0 : offset + count,
            }));
        };

    [TestMethod]
    public async Task ArchivePreservesUnknownSettingsAndOriginalPackageThroughMultiplePages()
    {
        string directory = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string hash = OriginalPackageHash;
        string path = PluginRetainedPackage.PathFor(directory, hash)!;
        byte[] original = OriginalPackage();
        string raw = JsonSerializer.Serialize(new { packageHash = hash, unknownSetting = new string('界', 300000) });
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "revision", itemKey = "original", rawPayload = raw, seq = 9 });
        try
        {
            await File.WriteAllBytesAsync(path, original);
            using var output = new MemoryStream();
            var firstPages = Pages(row);
            const string secondId = "oldplugin000002";
            byte[] setting = JsonSerializer.SerializeToUtf8Bytes(new { recordId = secondId, kind = "setting", rawPayload = "{\"privateSetting\":true}" });
            Task<JsonElement> Read(string after, int offset, string snapshot, CancellationToken token)
            {
                if (after == RecordId) return Task.FromResult(JsonSerializer.SerializeToElement(new {
                    contractVersion = "2.0", snapshotRevision = "12", recordId = secondId, revision = "8", chunk = setting,
                    recordDone = true, collectionDone = false, nextAfterRecordId = secondId, nextRecordOffset = 0,
                }));
                if (after == secondId) return Task.FromResult(JsonSerializer.SerializeToElement(new {
                    contractVersion = "2.0", snapshotRevision = "12", recordId = "", revision = "", chunk = Array.Empty<byte>(),
                    recordDone = false, collectionDone = true, nextAfterRecordId = secondId, nextRecordOffset = 0,
                }));
                return firstPages(after, offset, snapshot, token);
            }
            Assert.AreEqual(0, await PluginPreservationRequestController.WriteArchiveAsync(output, Read, directory, CancellationToken.None));
            output.Position = 0;
            using var archive = new ZipArchive(output, ZipArchiveMode.Read);
            using var records = JsonDocument.Parse(archive.GetEntry("records.json")!.Open());
            Assert.AreEqual(raw, records.RootElement[0].GetProperty("rawPayload").GetString());
            Assert.AreEqual(2, records.RootElement.GetArrayLength());
            Assert.AreEqual("{\"privateSetting\":true}", records.RootElement[1].GetProperty("rawPayload").GetString());
            using var preserved = new MemoryStream();
            await archive.GetEntry($"packages/{hash[7..]}.vtplugin")!.Open().CopyToAsync(preserved);
            CollectionAssert.AreEqual(original, preserved.ToArray());
        }
        finally { File.Delete(path); Directory.Delete(directory); }
    }

    [TestMethod]
    public async Task MissingPackageIsExplicitAndNeverReplacedByAnEmptyPackage()
    {
        string raw = JsonSerializer.Serialize(new { packageHash = "sha256:" + new string('b', 64) });
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "installation", rawPayload = raw });
        using var output = new MemoryStream();
        Assert.AreEqual(1, await PluginPreservationRequestController.WriteArchiveAsync(output, Pages(row), Path.GetTempPath(), CancellationToken.None));
        output.Position = 0;
        using var archive = new ZipArchive(output, ZipArchiveMode.Read);
        Assert.IsFalse(archive.Entries.Any(entry => entry.FullName.StartsWith("packages/", StringComparison.Ordinal)));
        using var report = JsonDocument.Parse(archive.GetEntry("preservation.json")!.Open());
        Assert.AreEqual(1, report.RootElement.GetProperty("missingPackages").GetInt32());
    }

    [TestMethod]
    public async Task InvalidCursorAndCancellationCannotReturnSuccessfulArchive()
    {
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "setting", rawPayload = new string('x', 300000) });
        using var output = new MemoryStream();
        await Assert.ThrowsExactlyAsync<IOException>(() => PluginPreservationRequestController.WriteArchiveAsync(output, Pages(row, badCursor: true), null, CancellationToken.None));
        using var cancelled = new MemoryStream();
        await Assert.ThrowsAsync<OperationCanceledException>(() => PluginPreservationRequestController.WriteArchiveAsync(cancelled, Pages(row), null, new CancellationToken(true)));
    }

    // Generated by the install authority's _normalized_package_digest, including integrity.json.
    private const string OriginalPackageHash = "sha256:88f0f3aef53b48536b8069253391f3368688bfe60cbaf58eed477493cc7c9cd6";
    private static byte[] OriginalPackage(CompressionLevel compression = CompressionLevel.NoCompression, bool corrupt = false)
    {
        using var output = new MemoryStream();
        using (var archive = new ZipArchive(output, ZipArchiveMode.Create, leaveOpen: true))
        {
            // Deliberately reverse Unicode order and use decomposed path spelling.
            var entries = new (string Name, string Content)[] {
                ("😀.txt", "astral"), ("\uE000.txt", "private-use"),
                ("ui/e\u0301.txt", corrupt ? "tampered package" : "original immutable package"),
                ("manifest.json", "{\"legacy\":\"preserved\"}"),
                ("integrity.json", """{"algorithm":"sha256","files":{"manifest.json":"f9e839e93f198688214b0eaa2882863b08fe0081300dbe3f28bd852ca64db1a7","ui/é.txt":"497373bd77ec158d3b2ee67a9e896aec8162f7fca8428c50fc28938ec3a520a2",".txt":"7a5468ae7a51c52e18635af0d8127798067180c556ef779cfeb4e13263cbe24e","😀.txt":"2e9f768344ff39ac7c1e41218b0a488a0fb666992777f7bfe03d09450df4f5b1"}}""")
            };
            foreach (var (name, content) in entries)
            {
                using Stream entry = archive.CreateEntry(name, compression).Open();
                entry.Write(Encoding.UTF8.GetBytes(content));
            }
        }
        return output.ToArray();
    }

    [TestMethod]
    [DataRow(false)]
    [DataRow(true)]
    public async Task ChangedGenerationOnDataOrTerminalPageCannotReturnSuccessfulArchive(bool terminal)
    {
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "setting", rawPayload = "{}" });
        var original = Pages(row);
        async Task<JsonElement> Read(string after, int offset, string snapshot, CancellationToken token)
        {
            if (after == "") return await original(after, offset, snapshot, token);
            Assert.AreEqual("12", snapshot);
            return JsonSerializer.SerializeToElement(new {
                contractVersion = "2.0", snapshotRevision = "13", recordId = terminal ? "" : "oldplugin000002", revision = "1",
                chunk = terminal ? Array.Empty<byte>() : row, recordDone = !terminal, collectionDone = terminal,
                nextAfterRecordId = terminal ? after : "oldplugin000002", nextRecordOffset = 0,
            });
        }
        using var output = new MemoryStream();
        await Assert.ThrowsExactlyAsync<IOException>(() => PluginPreservationRequestController.WriteArchiveAsync(output, Read, null, CancellationToken.None));
    }

    [TestMethod]
    [DataRow(CompressionLevel.Optimal)]
    [DataRow(CompressionLevel.NoCompression)]
    public async Task PackageIdentityUsesInstalledMembersAndPreventsReplacement(CompressionLevel compression)
    {
        string directory = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string path = PluginRetainedPackage.PathFor(directory, OriginalPackageHash)!;
        try
        {
            await File.WriteAllBytesAsync(path, OriginalPackage(compression));
            await using var source = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
            await PluginRetainedPackage.VerifyAsync(source, OriginalPackageHash, CancellationToken.None);
            Assert.ThrowsExactly<IOException>(() => File.Open(path, FileMode.Open, FileAccess.Write).Dispose());
        }
        finally { File.Delete(path); Directory.Delete(directory); }
    }

    [TestMethod]
    public async Task CorruptRetainedPackageCannotReturnSuccessfulArchive()
    {
        string directory = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string path = PluginRetainedPackage.PathFor(directory, OriginalPackageHash)!;
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "installation", rawPayload = JsonSerializer.Serialize(new { packageHash = OriginalPackageHash }) });
        try
        {
            await File.WriteAllBytesAsync(path, OriginalPackage(corrupt: true));
            using var output = new MemoryStream();
            await Assert.ThrowsExactlyAsync<IOException>(() => PluginPreservationRequestController.WriteArchiveAsync(output, Pages(row), directory, CancellationToken.None));
        }
        finally { File.Delete(path); Directory.Delete(directory); }
    }

    [TestMethod]
    public async Task PythonCP437PackagePreservesInstalledIdentityAndOriginalBytes()
    {
        // Python zipfile read-back: flags=0, raw filename 82 2e 74 78 74 -> é.txt.
        // The digest is produced by the same install authority as the UTF-8 fixture above.
        const string hash = "sha256:baecc7d3bc1fa0e8418f065f0374a3ba60b5f336d0607174d95ef530539534a4";
        byte[] original = Convert.FromBase64String("UEsDBBQAAAAAAAAAIQBxcS3rbAAAAGwAAAAOAAAAaW50ZWdyaXR5Lmpzb257ImFsZ29yaXRobSI6InNoYTI1NiIsImZpbGVzIjp7IsOpLnR4dCI6IjgzM2YzODExODRjZDBjNzk3NDM5NDMyZDQyMjg4ZWM1ZWZmYzkwM2I4MTU5ZTA0MWY2OTE3NjNlNTkyOWI1MjEifX1QSwMEFAAAAAAAAAAhAL/EiMERAAAAEQAAAAUAAACCLnR4dGxlZ2FjeSBuYW1lZCBmaWxlUEsBAhQDFAAAAAAAAAAhAHFxLetsAAAAbAAAAA4AAAAAAAAAAAAAAKSBAAAAAGludGVncml0eS5qc29uUEsBAhQDFAAAAAAAAAAhAL/EiMERAAAAEQAAAAUAAAAAAAAAAAAAAKSBmAAAAIIudHh0UEsFBgAAAAACAAIAbwAAAMwAAAAAAA==");
        string directory = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string path = PluginRetainedPackage.PathFor(directory, hash)!;
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "installation", rawPayload = JsonSerializer.Serialize(new { packageHash = hash }) });
        try
        {
            await File.WriteAllBytesAsync(path, original);
            using var output = new MemoryStream();
            Assert.AreEqual(0, await PluginPreservationRequestController.WriteArchiveAsync(output, Pages(row), directory, CancellationToken.None));
            output.Position = 0;
            using var archive = new ZipArchive(output, ZipArchiveMode.Read);
            using var preserved = new MemoryStream();
            await archive.GetEntry($"packages/{hash[7..]}.vtplugin")!.Open().CopyToAsync(preserved);
            CollectionAssert.AreEqual(original, preserved.ToArray());
        }
        finally { File.Delete(path); Directory.Delete(directory); }
    }

    private static (byte[] Bytes, string Hash) CompressionFixture(string name)
    {
        using Stream fixture = typeof(PluginPreservationTests).Assembly.GetManifestResourceStream("PluginPreservationCompressionFixtures")!;
        using var document = JsonDocument.Parse(fixture);
        JsonElement value = document.RootElement.GetProperty("cases").GetProperty(name);
        return (value.GetProperty("archive").GetBytesFromBase64(), value.GetProperty("packageHash").GetString()!);
    }

    [TestMethod]
    [DataRow("bzip2")]
    [DataRow("lzma")]
    [DataRow("zstandard")]
    [DataRow("zip64-lzma")]
    [DataRow("descriptor-bzip2")]
    public async Task AuthorityCompressedPackagesPreserveTheirExactOriginalBytes(string name)
    {
        var fixture = CompressionFixture(name);
        string directory = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        string path = PluginRetainedPackage.PathFor(directory, fixture.Hash)!;
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "installation", rawPayload = JsonSerializer.Serialize(new { packageHash = fixture.Hash }) });
        try
        {
            await File.WriteAllBytesAsync(path, fixture.Bytes);
            using var output = new MemoryStream();
            Assert.AreEqual(0, await PluginPreservationRequestController.WriteArchiveAsync(output, Pages(row), directory, CancellationToken.None));
            output.Position = 0;
            using var archive = new ZipArchive(output, ZipArchiveMode.Read);
            using var preserved = new MemoryStream();
            await archive.GetEntry($"packages/{fixture.Hash[7..]}.vtplugin")!.Open().CopyToAsync(preserved);
            CollectionAssert.AreEqual(fixture.Bytes, preserved.ToArray());
        }
        finally { File.Delete(path); Directory.Delete(directory); }
    }

    [TestMethod]
    [DataRow("corrupt-bzip2")]
    [DataRow("corrupt-lzma")]
    [DataRow("corrupt-zstandard")]
    [DataRow("excess-output-bzip2")]
    [DataRow("excess-output-lzma")]
    [DataRow("excess-output-zstandard")]
    [DataRow("oversized-member")]
    [DataRow("oversized-zstandard-window")]
    [DataRow("unsupported-zstandard-20")]
    public async Task CompressionCorruptionAndBudgetsFailClosed(string name)
    {
        var fixture = CompressionFixture(name);
        string path = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N") + ".vtplugin");
        try
        {
            await File.WriteAllBytesAsync(path, fixture.Bytes);
            await using var source = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
            await Assert.ThrowsAsync<IOException>(() => PluginRetainedPackage.VerifyAsync(source, fixture.Hash, CancellationToken.None));
        }
        finally { File.Delete(path); }
    }

    [TestMethod]
    [DoNotParallelize]
    public async Task LargeAdvertisedLzmaDictionaryDoesNotAllocateItsFullWindow()
    {
        var fixture = CompressionFixture("large-lzma-dictionary");
        string path = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N") + ".vtplugin");
        try
        {
            await File.WriteAllBytesAsync(path, fixture.Bytes);
            await using var source = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
            long before = GC.GetTotalAllocatedBytes(precise: true);
            await PluginRetainedPackage.VerifyAsync(source, fixture.Hash, CancellationToken.None);
            Assert.IsLessThan(16L * 1024 * 1024, GC.GetTotalAllocatedBytes(precise: true) - before,
                "A tiny preserved member must not allocate its advertised 64 MiB dictionary.");
        }
        finally { File.Delete(path); }
    }

    private sealed class CancelOnPackageRead(string path, CancellationTokenSource cancellation)
        : FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read, 4096, useAsync: true)
    {
        internal bool Started { get; private set; }
        public override ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken token = default)
        {
            Started = true;
            cancellation.Cancel();
            return base.ReadAsync(buffer, token);
        }
        public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken token)
        {
            Started = true;
            cancellation.Cancel();
            return base.ReadAsync(buffer, offset, count, token);
        }
    }

    [TestMethod]
    [DataRow("bzip2")]
    [DataRow("lzma")]
    [DataRow("zstandard")]
    public async Task CompressionVerificationHonorsCancellationAfterInputStarts(string name)
    {
        var fixture = CompressionFixture(name);
        string path = Path.Combine(Path.GetTempPath(), "plugin-preservation-" + Guid.NewGuid().ToString("N") + ".vtplugin");
        try
        {
            await File.WriteAllBytesAsync(path, fixture.Bytes);
            using var cancellation = new CancellationTokenSource();
            await using var source = new CancelOnPackageRead(path, cancellation);
            await Assert.ThrowsAsync<OperationCanceledException>(() => PluginRetainedPackage.VerifyAsync(source, fixture.Hash, cancellation.Token));
            Assert.IsTrue(source.Started);
        }
        finally { File.Delete(path); }
    }
}
