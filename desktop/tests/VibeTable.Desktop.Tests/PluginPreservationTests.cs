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
    private static Func<string, int, CancellationToken, Task<JsonElement>> Pages(byte[] record, bool badCursor = false)
        => (after, offset, token) =>
        {
            token.ThrowIfCancellationRequested();
            bool terminal = after == RecordId;
            int count = terminal ? 0 : Math.Min(LegacyLayoutRequestController.PageBytes, record.Length - offset);
            bool done = !terminal && offset + count == record.Length;
            return Task.FromResult(JsonSerializer.SerializeToElement(new {
                contractVersion = "1.0", recordId = terminal ? "" : RecordId, revision = "7",
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
        string hash = "sha256:" + new string('a', 64);
        string path = PluginRetainedPackage.PathFor(directory, hash)!;
        byte[] original = "original immutable package"u8.ToArray();
        string raw = JsonSerializer.Serialize(new { packageHash = hash, unknownSetting = new string('界', 300000) });
        byte[] row = JsonSerializer.SerializeToUtf8Bytes(new { recordId = RecordId, kind = "revision", itemKey = "original", rawPayload = raw, seq = 9 });
        try
        {
            await File.WriteAllBytesAsync(path, original);
            using var output = new MemoryStream();
            var firstPages = Pages(row);
            const string secondId = "oldplugin000002";
            byte[] setting = JsonSerializer.SerializeToUtf8Bytes(new { recordId = secondId, kind = "setting", rawPayload = "{\"privateSetting\":true}" });
            Task<JsonElement> Read(string after, int offset, CancellationToken token)
            {
                if (after == RecordId) return Task.FromResult(JsonSerializer.SerializeToElement(new {
                    contractVersion = "1.0", recordId = secondId, revision = "8", chunk = setting,
                    recordDone = true, collectionDone = false, nextAfterRecordId = secondId, nextRecordOffset = 0,
                }));
                if (after == secondId) return Task.FromResult(JsonSerializer.SerializeToElement(new {
                    contractVersion = "1.0", recordId = "", revision = "", chunk = Array.Empty<byte>(),
                    recordDone = false, collectionDone = true, nextAfterRecordId = secondId, nextRecordOffset = 0,
                }));
                return firstPages(after, offset, token);
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
}
