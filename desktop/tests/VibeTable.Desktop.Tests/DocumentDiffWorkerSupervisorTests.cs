using System.Diagnostics;
using System.Reflection;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DocumentDiffWorkerSupervisorTests
{
    [TestMethod]
    public async Task RealWorkerNormalizesBothIsolatedInputsAndExits()
    {
        string repo = RepositoryRoot();
        string root = CreateOperation(repo);
        string source = Path.Combine(repo, "desktop/tests/VibeTable.DocumentDiff.OpenXml.Tests/TestData/Qualification/docx/existing-revisions.docx");
        byte[] original = File.ReadAllBytes(source);
        foreach (string side in new[] { "historical", "effective" })
            File.Copy(source, Path.Combine(root, "input", side + ".content"));
        string configuration = typeof(DocumentDiffWorkerSupervisorTests).Assembly
            .GetCustomAttribute<AssemblyConfigurationAttribute>()!.Configuration;
        string worker = Path.Combine(repo, "desktop/src/VibeTable.DocumentDiff.Worker/bin", configuration,
            "net10.0/VibeTable.DocumentDiff.Worker.exe");

        int exitCode = await DocumentDiffWorkerSupervisor.RunAsync(worker, root, TimeSpan.FromSeconds(30), default);

        Assert.AreEqual(0, exitCode);
        foreach (string side in new[] { "historical", "effective" })
        {
            CollectionAssert.AreEqual(original, File.ReadAllBytes(Path.Combine(root, "input", side + ".content")));
            string final = Path.Combine(root, "normalized", side + ".final.docx");
            Assert.IsTrue(File.Exists(final));
            Assert.IsFalse(File.Exists(final + ".partial"));
            File.Delete(final);
            File.Delete(Path.Combine(root, "input", side + ".content"));
        }
        RemoveEmptyOperation(root);
    }

    [TestMethod]
    public async Task RealWorkerComparisonPublishesBoundedMetadataAndChangeIndex()
    {
        string repo = RepositoryRoot();
        string root = CreateOperation(repo);
        Directory.CreateDirectory(Path.Combine(root, "index"));
        string fixtures = Path.Combine(repo, "desktop/tests/VibeTable.DocumentDiff.OpenXml.Tests/TestData/Qualification/docx");
        File.Copy(Path.Combine(fixtures, "format-before.docx"), Path.Combine(root, "input/historical.content"));
        File.Copy(Path.Combine(fixtures, "format-after.docx"), Path.Combine(root, "input/effective.content"));
        string configuration = typeof(DocumentDiffWorkerSupervisorTests).Assembly
            .GetCustomAttribute<AssemblyConfigurationAttribute>()!.Configuration;
        string worker = Path.Combine(repo, "desktop/src/VibeTable.Desktop/bin", configuration,
            "net10.0-windows/resources/document-diff/VibeTable.DocumentDiff.Worker.exe");
        var start = new ProcessStartInfo(worker);
        int exit = await DocumentDiffWorkerSupervisor.RunAsync(start, root, TimeSpan.FromSeconds(30), default, compareDocx: true);
        Assert.AreEqual(0, exit);
        using var result = System.Text.Json.JsonDocument.Parse(File.ReadAllText(Path.Combine(root, "index/result.json")));
        Assert.AreEqual(2, result.RootElement.GetProperty("version").GetInt32());
        Assert.AreEqual(2, result.RootElement.GetProperty("summary").GetProperty("formattingChanges").GetInt32());
        string[] changes = File.ReadAllLines(Path.Combine(root, "index/changes.jsonl"));
        Assert.AreEqual(3, changes.Length);
        using var structural = System.Text.Json.JsonDocument.Parse(changes[2]);
        Assert.AreEqual("other", structural.RootElement.GetProperty("kind").GetString());
        Assert.AreEqual(1, structural.RootElement.GetProperty("location").GetProperty("paragraphIndex").GetInt32());
        foreach (string side in new[] { "historical", "effective" })
        {
            File.Delete(Path.Combine(root, "input", side + ".content"));
            File.Delete(Path.Combine(root, "normalized", side + ".final.docx"));
        }
        File.Delete(Path.Combine(root, "index/result.json"));
        File.Delete(Path.Combine(root, "index/changes.jsonl"));
        Directory.Delete(Path.Combine(root, "index"));
        RemoveEmptyOperation(root);
    }

    [TestMethod]
    [DataRow(true)]
    [DataRow(false)]
    public async Task RunningWorkerIsStoppedBeforeCancellationOrTimeoutReturns(bool cancel)
    {
        string root = CreateOperation(RepositoryRoot());
        using var cancellation = new CancellationTokenSource();
        var start = new ProcessStartInfo(Path.Combine(Environment.SystemDirectory, "WindowsPowerShell/v1.0/powershell.exe"));
        start.ArgumentList.Add("-NoProfile");
        start.ArgumentList.Add("-NonInteractive");
        start.ArgumentList.Add("-Command");
        start.ArgumentList.Add("""
            $request = [Console]::In.ReadToEnd() | ConvertFrom-Json
            [IO.File]::WriteAllText((Join-Path $request.operationDirectory 'worker.pid'), [string]$PID)
            while ($true) { [Threading.Thread]::Sleep(1000) }
            """);
        Task<int> running = DocumentDiffWorkerSupervisor.RunAsync(start, root,
            TimeSpan.FromSeconds(cancel ? 30 : 10), cancellation.Token);
        string marker = Path.Combine(root, "worker.pid");
        using (var readyDeadline = new CancellationTokenSource(TimeSpan.FromSeconds(15)))
        {
            while (!File.Exists(marker))
            {
                if (running.IsCompleted)
                    await running;
                await Task.Delay(25, readyDeadline.Token);
            }
        }
        int processId = int.Parse(File.ReadAllText(marker), System.Globalization.CultureInfo.InvariantCulture);
        using Process observed = Process.GetProcessById(processId);
        _ = observed.Handle;
        if (cancel)
        {
            cancellation.Cancel();
            await Assert.ThrowsAsync<OperationCanceledException>(() => running);
        }
        else
            await Assert.ThrowsExactlyAsync<TimeoutException>(() => running);
        Assert.IsTrue(observed.HasExited, "The exact running process must exit before returning.");
        File.Delete(marker);
        RemoveEmptyOperation(root);
    }

    private static string RepositoryRoot()
    {
        for (DirectoryInfo? directory = new(AppContext.BaseDirectory); directory is not null; directory = directory.Parent)
            if (File.Exists(Path.Combine(directory.FullName, ".ci/project.json")))
                return directory.FullName;
        throw new DirectoryNotFoundException("Repository root was not found.");
    }

    private static string CreateOperation(string repo)
    {
        string root = Path.Combine(repo, "build/document-diff-worker-tests", Guid.NewGuid().ToString("N"));
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
