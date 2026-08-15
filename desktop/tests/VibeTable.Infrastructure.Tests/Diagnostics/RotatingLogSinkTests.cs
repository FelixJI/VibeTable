using System.Text;
using VibeTable.Infrastructure.Diagnostics;

namespace VibeTable.Infrastructure.Tests.Diagnostics;

[TestClass]
public sealed class RotatingLogSinkTests
{
    [TestMethod]
    public async Task ReopenSameDay_AppendsWithoutRotating()
    {
        string root = CreateRoot();
        string path = Path.Combine(root, "backend.log");
        try
        {
            await WriteAsync(path, "first");
            await WriteAsync(path, "second");

            CollectionAssert.AreEqual(
                new[] { "first", "second" },
                await File.ReadAllLinesAsync(path));
            Assert.AreEqual(1, Directory.GetFiles(root, "*.log").Length);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task OversizedCurrentFile_RotatesBeforeWriting()
    {
        string root = CreateRoot();
        string path = Path.Combine(root, "sidecar.log");
        try
        {
            await File.WriteAllBytesAsync(
                path,
                new byte[RotatingLogSink.MaximumFileBytes]);

            await WriteAsync(path, "current");

            Assert.AreEqual("current", (await File.ReadAllTextAsync(path)).Trim());
            Assert.AreEqual(2, Directory.GetFiles(root, "*.log").Length);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    [TestMethod]
    public async Task OpeningSink_RemovesExpiredRotatedLogs()
    {
        string root = CreateRoot();
        string path = Path.Combine(root, "desktop.log");
        string expired = Path.Combine(root, "desktop-20000101-000000000.log");
        try
        {
            await File.WriteAllTextAsync(expired, "expired", Encoding.UTF8);
            File.SetLastWriteTimeUtc(
                expired,
                DateTime.UtcNow.AddDays(-RotatingLogSink.RetentionDays - 1));

            await WriteAsync(path, "current");

            Assert.IsFalse(File.Exists(expired));
            Assert.IsTrue(File.Exists(path));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    private static async Task WriteAsync(string path, string value)
    {
        await using var sink = new RotatingLogSink(path);
        await sink.WriteLineAsync(value);
    }

    private static string CreateRoot()
    {
        string root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-log-tests",
            Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        return root;
    }
}
