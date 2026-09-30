using System.Text;
using System.Text.Json;

namespace VibeTable.DocumentDiff.Worker.Tests;

[TestClass]
public sealed class WorkerProtocolTests
{
    [TestMethod]
    [DataRow("null")]
    [DataRow("{}")]
    [DataRow("{\"version\":2,\"operationDirectory\":\"relative\"}")]
    [DataRow("{\"version\":1,\"operationDirectory\":\"relative\"}")]
    [DataRow("{\"version\":1,\"operationDirectory\":null}")]
    [DataRow("{\"unexpected\":true}")]
    public async Task InvalidRequestsFailWithoutFileAccess(string json)
    {
        using var request = new MemoryStream(Encoding.UTF8.GetBytes(json));
        Assert.AreEqual(2, await Program.RunAsync(request));
    }

    [TestMethod]
    public async Task OversizedRequestHasABoundedFailure()
    {
        using var request = new MemoryStream(Encoding.UTF8.GetBytes(new string(' ', 32 * 1024 + 1)));
        Assert.AreEqual(3, await Program.RunAsync(request));
    }

    [TestMethod]
    public async Task SuccessfulPairUsesOnlyFixedDerivedNamesAndCannotOverwriteThem()
    {
        string root = CreateOperation();
        byte[] original = File.ReadAllBytes(Path.Combine(AppContext.BaseDirectory,
            "Qualification/docx/existing-revisions.docx"));
        foreach (string side in new[] { "historical", "effective" })
            File.WriteAllBytes(Path.Combine(root, "input", side + ".content"), original);
        Assert.AreEqual(0, await RunAsync(root));
        string historical = Path.Combine(root, "normalized/historical.final.docx");
        byte[] normalized = File.ReadAllBytes(historical);
        Assert.AreEqual(2, await RunAsync(root));
        CollectionAssert.AreEqual(normalized, File.ReadAllBytes(historical));
        foreach (string side in new[] { "historical", "effective" })
        {
            string input = Path.Combine(root, "input", side + ".content");
            string output = Path.Combine(root, "normalized", side + ".final.docx");
            CollectionAssert.AreEqual(original, File.ReadAllBytes(input));
            Assert.IsTrue(File.Exists(output));
            Assert.IsFalse(File.Exists(output + ".partial"));
            File.Delete(input);
            File.Delete(output);
        }
        RemoveEmptyOperation(root);
    }

    [TestMethod]
    public async Task CorruptPackageLeavesOnlyTheKnownPartialForTheHostToClean()
    {
        string root = CreateOperation();
        string input = Path.Combine(root, "input/historical.content");
        File.WriteAllBytes(input, [1, 2, 3]);
        Assert.AreEqual(2, await RunAsync(root));
        string partial = Path.Combine(root, "normalized/historical.final.docx.partial");
        Assert.IsTrue(File.Exists(partial));
        Assert.IsFalse(File.Exists(Path.Combine(root, "normalized/historical.final.docx")));
        CollectionAssert.AreEqual(new byte[] { 1, 2, 3 }, File.ReadAllBytes(input));
        File.Delete(input);
        File.Delete(partial);
        RemoveEmptyOperation(root);
    }

    private static async Task<int> RunAsync(string root)
    {
        using var request = new MemoryStream(JsonSerializer.SerializeToUtf8Bytes(
            new { version = 1, operationDirectory = root }));
        return await Program.RunAsync(request);
    }

    private static string CreateOperation()
    {
        DirectoryInfo? repo = new(AppContext.BaseDirectory);
        while (repo is not null && !File.Exists(Path.Combine(repo.FullName, ".ci/project.json")))
            repo = repo.Parent;
        if (repo is null)
            throw new DirectoryNotFoundException("Repository root was not found.");
        string root = Path.Combine(repo.FullName, "build/document-diff-worker-tests", Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(Path.Combine(root, "input"));
        Directory.CreateDirectory(Path.Combine(root, "normalized"));
        return root;
    }

    private static void RemoveEmptyOperation(string root)
    {
        Directory.Delete(Path.Combine(root, "input"));
        Directory.Delete(Path.Combine(root, "normalized"));
        Directory.Delete(root);
    }
}
