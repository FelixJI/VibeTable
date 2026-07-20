using System.Collections.Concurrent;
using VibeTable.Workspace.Storage;

namespace VibeTable.Workspace.Tests;

[TestClass]
public sealed class ContentObjectStoreTests
{
    [TestMethod]
    public void Commit_WritesVerifiedObjectWithNoTemporaryFile()
    {
        using var fixture = new ObjectStoreFixture();
        string source = fixture.WriteSource("normal.bin", "atomic content");

        var result = fixture.Store.Commit(source);

        Assert.IsFalse(result.AlreadyExisted);
        Assert.AreEqual("atomic content", File.ReadAllText(
            fixture.Store.GetObjectPath(result.ContentHash)));
        AssertNoTemporaryFiles(fixture.ObjectsRoot);
    }

    [TestMethod]
    public void Commit_CorruptExistingObjectWithSameSizeIsRejectedAndPreserved()
    {
        using var fixture = new ObjectStoreFixture();
        string source = fixture.WriteSource("source.bin", "AAAA");
        string hash = ContentObjectStore.ComputeHash(source);
        string objectPath = fixture.Store.GetObjectPath(hash);
        Directory.CreateDirectory(Path.GetDirectoryName(objectPath)!);
        File.WriteAllText(objectPath, "BBBB");

        Assert.Throws<InvalidDataException>(() => fixture.Store.Commit(source));

        Assert.AreEqual("BBBB", File.ReadAllText(objectPath));
        AssertNoTemporaryFiles(fixture.ObjectsRoot);
    }

    [TestMethod]
    public void Commit_CorruptExistingObjectWithWrongSizeIsRejected()
    {
        using var fixture = new ObjectStoreFixture();
        string source = fixture.WriteSource("source.bin", "expected");
        string hash = ContentObjectStore.ComputeHash(source);
        string objectPath = fixture.Store.GetObjectPath(hash);
        Directory.CreateDirectory(Path.GetDirectoryName(objectPath)!);
        File.WriteAllText(objectPath, "short");

        Assert.Throws<InvalidDataException>(() => fixture.Store.Commit(source));

        AssertNoTemporaryFiles(fixture.ObjectsRoot);
    }

    [TestMethod]
    public async Task Commit_ConcurrentWritersAcceptOneVerifiedWinnerAndCleanTemps()
    {
        using var fixture = new ObjectStoreFixture();
        string source = fixture.WriteSource(
            "concurrent.bin",
            new string('x', 4 * 1024 * 1024));
        using var start = new ManualResetEventSlim(initialState: false);
        var results = new ConcurrentBag<CommitResult>();
        Task[] writers = Enumerable.Range(0, 12).Select(_ => Task.Run(() =>
        {
            start.Wait();
            results.Add(fixture.Store.Commit(source));
        })).ToArray();

        start.Set();
        await Task.WhenAll(writers);

        Assert.AreEqual(12, results.Count);
        Assert.AreEqual(1, results.Count(result => !result.AlreadyExisted));
        Assert.AreEqual(1, results.Select(result => result.ContentHash).Distinct().Count());
        var winner = results.First();
        string objectPath = fixture.Store.GetObjectPath(winner.ContentHash);
        Assert.AreEqual(winner.Size, new FileInfo(objectPath).Length);
        Assert.AreEqual(winner.ContentHash, ContentObjectStore.ComputeHash(objectPath));
        AssertNoTemporaryFiles(fixture.ObjectsRoot);
    }

    private static void AssertNoTemporaryFiles(string objectsRoot)
    {
        string[] temporaryFiles = Directory.Exists(objectsRoot)
            ? Directory.GetFiles(objectsRoot, "*.tmp", SearchOption.AllDirectories)
            : [];
        Assert.AreEqual(0, temporaryFiles.Length);
    }

    private sealed class ObjectStoreFixture : IDisposable
    {
        private readonly string _root = Path.Combine(
            Path.GetTempPath(),
            "vibetable-object-store-" + Guid.NewGuid().ToString("N"));

        public ObjectStoreFixture()
        {
            BackupRoot = Path.Combine(_root, ".backup");
            SourceRoot = Path.Combine(_root, "source");
            Directory.CreateDirectory(BackupRoot);
            Directory.CreateDirectory(SourceRoot);
            Store = new ContentObjectStore(BackupRoot);
        }

        public string BackupRoot { get; }
        public string SourceRoot { get; }
        public string ObjectsRoot => Path.Combine(BackupRoot, "objects");
        public ContentObjectStore Store { get; }

        public string WriteSource(string fileName, string content)
        {
            string path = Path.Combine(SourceRoot, fileName);
            File.WriteAllText(path, content);
            return path;
        }

        public void Dispose()
        {
            try
            {
                if (Directory.Exists(_root)) Directory.Delete(_root, recursive: true);
            }
            catch
            {
            }
        }
    }
}
