using System.Text.Json;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DocumentDiffArtifactBrokerTests
{
    private static readonly Guid WorkspaceId =
        Guid.Parse("11111111-1111-4111-8111-111111111111");
    private static readonly Guid SessionId =
        Guid.Parse("22222222-2222-4222-8222-222222222222");

    [TestMethod]
    public void CreateOperationBuildsRoleDirectoriesAndRelativeManifest()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.Parse("33333333-3333-4333-8333-333333333333"),
            WorkspaceId,
            7);

        Assert.IsTrue(Directory.Exists(operation.InputDirectory));
        Assert.IsTrue(Directory.Exists(operation.NormalizedDirectory));
        Assert.IsTrue(Directory.Exists(operation.OutputDirectory));
        Assert.IsTrue(Directory.Exists(operation.IndexDirectory));
        Assert.AreEqual(
            Path.Combine(operation.InputDirectory, "historical.content"),
            operation.HistoricalInputPath);
        Assert.AreEqual(
            Path.Combine(operation.InputDirectory, "effective.content"),
            operation.EffectiveInputPath);

        using JsonDocument manifest = JsonDocument.Parse(
            File.ReadAllText(operation.ManifestPath));
        JsonElement root = manifest.RootElement;
        Assert.AreEqual("running", root.GetProperty("state").GetString());
        string[] knownFiles = root.GetProperty("knownFiles")
            .EnumerateArray()
            .Select(value => value.GetString()!)
            .ToArray();
        CollectionAssert.Contains(knownFiles, "manifest.json");
        CollectionAssert.Contains(knownFiles, "manifest.json.partial");
        CollectionAssert.Contains(knownFiles, "input/historical.content.partial");
        CollectionAssert.Contains(knownFiles, "input/effective.content.partial");
        CollectionAssert.Contains(knownFiles, "input/historical.content");
        CollectionAssert.Contains(knownFiles, "input/effective.content");
        Assert.IsFalse(knownFiles.Any(Path.IsPathRooted));
        Assert.IsFalse(File.ReadAllText(operation.ManifestPath)
            .Contains(directory.Path, StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task VerifyMaterializedInputsUsesAuthoritativeRevisionHashes()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        await File.WriteAllTextAsync(operation.HistoricalInputPath, "before");
        await File.WriteAllTextAsync(operation.EffectiveInputPath, "after");

        await using (DocumentDiffVerifiedInputLease verified =
                     await operation.OpenVerifiedInputsAsync(
                         "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
                         "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
                         CancellationToken.None))
        {
            Assert.ThrowsExactly<IOException>(() =>
                File.WriteAllText(operation.HistoricalInputPath, "replace"));
        }

        await Assert.ThrowsExactlyAsync<DocumentDiffArtifactStaleException>(() =>
            operation.OpenVerifiedInputsAsync(
                "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
                CancellationToken.None));
    }

    [TestMethod]
    public async Task PublishedArtifactIsBoundToSessionWorkspaceEpochAndKind()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        await File.WriteAllTextAsync(operation.HistoricalInputPath, "before");
        await File.WriteAllTextAsync(operation.EffectiveInputPath, "after");
        await using (DocumentDiffVerifiedInputLease inputs =
                     await operation.OpenVerifiedInputsAsync(
                         "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
                         "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
                         CancellationToken.None))
        {
            inputs.ConfirmSourceStable();
            string output = operation.PrepareArtifact(
                DocumentDiffArtifactKind.ComparisonDocument,
                "comparison.docx");
            File.WriteAllText(output, "derived comparison");
            operation.Complete(SessionId);
        }

        using (JsonDocument manifest = JsonDocument.Parse(
                   File.ReadAllText(operation.ManifestPath)))
        {
            JsonElement artifact = manifest.RootElement.GetProperty("artifacts")
                .GetProperty("comparisonDocument");
            Assert.IsTrue(artifact.GetProperty("derivedArtifact").GetBoolean());
            Assert.IsTrue(artifact.GetProperty("readOnlyResult").GetBoolean());
            Assert.IsFalse(artifact.GetProperty("workspaceRevision").GetBoolean());
            Assert.AreEqual(
                "output/comparison.docx",
                artifact.GetProperty("relativePath").GetString());
        }

        using (DocumentDiffArtifactReadLease read = broker.OpenRead(
                   SessionId, WorkspaceId, 7,
                   DocumentDiffArtifactKind.ComparisonDocument))
        using (var reader = new StreamReader(read.Stream, leaveOpen: true))
            Assert.AreEqual("derived comparison", reader.ReadToEnd());

        AssertUnavailable(() => broker.OpenRead(
            SessionId, Guid.NewGuid(), 7,
            DocumentDiffArtifactKind.ComparisonDocument));
        AssertUnavailable(() => broker.OpenRead(
            SessionId, WorkspaceId, 8,
            DocumentDiffArtifactKind.ComparisonDocument));
        AssertUnavailable(() => broker.OpenRead(
            SessionId, WorkspaceId, 7,
            DocumentDiffArtifactKind.ChangeIndex));

        broker.CloseSession(SessionId);
        AssertUnavailable(() => broker.OpenRead(
            SessionId, WorkspaceId, 7,
            DocumentDiffArtifactKind.ComparisonDocument));
        Assert.IsFalse(Directory.Exists(operation.OperationDirectory));
    }

    [TestMethod]
    public async Task ClosingSessionWaitsForActiveReaderThenCleans()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        await PublishComparisonAsync(operation);
        string operationDirectory = operation.OperationDirectory;
        DocumentDiffArtifactReadLease read = broker.OpenRead(
            SessionId, WorkspaceId, 7,
            DocumentDiffArtifactKind.ComparisonDocument);

        broker.CloseSession(SessionId);

        Assert.IsTrue(Directory.Exists(operationDirectory));
        AssertUnavailable(() => broker.OpenRead(
            SessionId, WorkspaceId, 7,
            DocumentDiffArtifactKind.ComparisonDocument));
        read.Dispose();
        Assert.IsFalse(Directory.Exists(operationDirectory));
    }

    [TestMethod]
    public async Task CleanupExpiredRevokesReadySessionInCurrentBroker()
    {
        using var directory = new TemporaryDirectory();
        var time = new ManualTimeProvider();
        using var broker = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromMinutes(5),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Alive);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        await PublishComparisonAsync(operation);
        string operationDirectory = operation.OperationDirectory;

        time.Advance(TimeSpan.FromMinutes(6));
        broker.CleanupExpired();

        AssertUnavailable(() => broker.OpenRead(
            SessionId, WorkspaceId, 7,
            DocumentDiffArtifactKind.ComparisonDocument));
        Assert.IsFalse(Directory.Exists(operationDirectory));
    }

    [TestMethod]
    public void PrepareArtifactRejectsPathTraversalAndDuplicateKinds()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);

        Assert.ThrowsExactly<ArgumentException>(() => operation.PrepareArtifact(
            DocumentDiffArtifactKind.ComparisonDocument,
            "../comparison.docx"));
        Assert.ThrowsExactly<ArgumentException>(() => operation.PrepareArtifact(
            DocumentDiffArtifactKind.ComparisonDocument,
            "nested/comparison.docx"));
        _ = operation.PrepareArtifact(
            DocumentDiffArtifactKind.ComparisonDocument,
            "comparison.docx");
        Assert.ThrowsExactly<InvalidOperationException>(() => operation.PrepareArtifact(
            DocumentDiffArtifactKind.ComparisonDocument,
            "second.docx"));
    }

    [TestMethod]
    public void StartupCleanupRemovesOnlyExpiredDeadOwnerKnownFiles()
    {
        using var directory = new TemporaryDirectory();
        var time = new ManualTimeProvider();
        var first = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromHours(1),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Dead);
        DocumentDiffArtifactOperation operation = first.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        string historicalInputPath = operation.HistoricalInputPath;
        string manifestPath = operation.ManifestPath;
        File.WriteAllText(historicalInputPath, "sensitive input");
        string unknown = Path.Combine(operation.OperationDirectory, "foreign.bin");
        File.WriteAllText(unknown, "preserve me");
        time.Advance(TimeSpan.FromHours(2));

        using var recovered = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromHours(1),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Dead);

        Assert.IsFalse(File.Exists(historicalInputPath));
        Assert.IsTrue(File.Exists(unknown));
        Assert.IsTrue(File.Exists(manifestPath));
        Assert.AreEqual(
            "closing",
            JsonDocument.Parse(File.ReadAllText(manifestPath))
                .RootElement.GetProperty("state").GetString());
        first.Dispose();
    }

    [TestMethod]
    public void StartupCleanupPreservesLiveUnexpiredAndInvalidManifests()
    {
        using var directory = new TemporaryDirectory();
        var time = new ManualTimeProvider();
        var first = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromHours(1),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Dead);
        DocumentDiffArtifactOperation unexpired = first.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        string historicalInputPath = unexpired.HistoricalInputPath;
        File.WriteAllText(historicalInputPath, "keep");

        string invalid = Path.Combine(directory.Path, Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(invalid);
        File.WriteAllText(Path.Combine(invalid, "manifest.json"), "{not-json");
        File.WriteAllText(Path.Combine(invalid, "unknown.bin"), "keep");

        using var recovered = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromHours(1),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Dead);
        Assert.IsTrue(File.Exists(historicalInputPath));
        Assert.IsTrue(Directory.Exists(invalid));
        recovered.Dispose();

        time.Advance(TimeSpan.FromHours(2));
        using var liveOwner = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromHours(1),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Alive);
        Assert.IsTrue(File.Exists(historicalInputPath));
        Assert.IsTrue(Directory.Exists(invalid));
        first.Dispose();
    }

    [TestMethod]
    public void StartupCleanupFullyRemovesExpiredDeadOwnerOperationWhenKnown()
    {
        using var directory = new TemporaryDirectory();
        var time = new ManualTimeProvider();
        var first = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromMinutes(5),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Dead);
        DocumentDiffArtifactOperation operation = first.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        string operationDirectory = operation.OperationDirectory;
        File.WriteAllText(operation.HistoricalInputPath, "sensitive input");
        time.Advance(TimeSpan.FromMinutes(6));

        using var recovered = new DocumentDiffArtifactBroker(
            directory.Path,
            TimeSpan.FromMinutes(5),
            time,
            (_, _) => DocumentDiffOwnerLiveness.Dead);

        Assert.IsFalse(Directory.Exists(operationDirectory));
        first.Dispose();
    }

    [TestMethod]
    public async Task VerifyMaterializedInputsRejectsMalformedHashWithoutReading()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);

        await Assert.ThrowsExactlyAsync<JsonException>(() =>
            operation.OpenVerifiedInputsAsync(
                "SHA256:not-canonical",
                "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                CancellationToken.None));
    }

    [TestMethod]
    public async Task SecondVerifiedInputLeaseIsRejectedUntilFirstReleased()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        await File.WriteAllTextAsync(operation.HistoricalInputPath, "before");
        await File.WriteAllTextAsync(operation.EffectiveInputPath, "after");

        DocumentDiffVerifiedInputLease first = await operation.OpenVerifiedInputsAsync(
            "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
            "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
            CancellationToken.None);
        await Assert.ThrowsExactlyAsync<InvalidOperationException>(() =>
            operation.OpenVerifiedInputsAsync(
                "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
                "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
                CancellationToken.None));

        await first.DisposeAsync();
        await using DocumentDiffVerifiedInputLease second =
            await operation.OpenVerifiedInputsAsync(
                "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
                "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
                CancellationToken.None);
    }

    [TestMethod]
    public async Task ReopenedVerifiedInputsRequireFreshPostCasBeforeComplete()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        await File.WriteAllTextAsync(operation.HistoricalInputPath, "before");
        await File.WriteAllTextAsync(operation.EffectiveInputPath, "after");

        DocumentDiffVerifiedInputLease first = await operation.OpenVerifiedInputsAsync(
            "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
            "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
            CancellationToken.None);
        first.ConfirmSourceStable();
        await first.DisposeAsync();

        await using DocumentDiffVerifiedInputLease second =
            await operation.OpenVerifiedInputsAsync(
                "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
                "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
                CancellationToken.None);
        string output = operation.PrepareArtifact(
            DocumentDiffArtifactKind.ComparisonDocument,
            "comparison.docx");
        await File.WriteAllTextAsync(output, "derived comparison");

        Assert.ThrowsExactly<ObjectDisposedException>(first.ConfirmSourceStable);
        Assert.ThrowsExactly<InvalidOperationException>(() => operation.Complete(SessionId));
        second.ConfirmSourceStable();
        operation.Complete(SessionId);
    }

    [TestMethod]
    public async Task ConcurrentVerifiedInputLeaseDisposeReleasesOperationOnce()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        string operationDirectory = operation.OperationDirectory;
        await File.WriteAllTextAsync(operation.HistoricalInputPath, "before");
        await File.WriteAllTextAsync(operation.EffectiveInputPath, "after");
        DocumentDiffVerifiedInputLease inputs = await operation.OpenVerifiedInputsAsync(
            "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
            "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
            CancellationToken.None);
        operation.Dispose();

        await Task.WhenAll(Enumerable.Range(0, 16)
            .Select(_ => inputs.DisposeAsync().AsTask()));

        Assert.IsFalse(Directory.Exists(operationDirectory));
    }

    [TestMethod]
    public void CompleteRejectsArtifactWithoutVerifiedInputsAndPostCas()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        using DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        string output = operation.PrepareArtifact(
            DocumentDiffArtifactKind.ComparisonDocument,
            "comparison.docx");
        File.WriteAllText(output, "unverified");

        Assert.ThrowsExactly<InvalidOperationException>(() => operation.Complete(SessionId));
    }

    [TestMethod]
    public void ReplacedInputJunctionIsRejectedWithoutDeletingExternalContent()
    {
        using var directory = new TemporaryDirectory();
        using var broker = new DocumentDiffArtifactBroker(directory.Path);
        DocumentDiffArtifactOperation operation = broker.CreateOperation(
            Guid.NewGuid(), WorkspaceId, 7);
        string inputDirectory = operation.InputDirectory;
        string external = System.IO.Path.Combine(directory.Path, "external");
        Directory.CreateDirectory(external);
        string externalInput = System.IO.Path.Combine(external, "historical.content");
        File.WriteAllText(externalInput, "must survive");
        Directory.Delete(inputDirectory, recursive: false);
        if (!TryCreateJunction(inputDirectory, external))
        {
            Directory.CreateDirectory(inputDirectory);
            operation.Dispose();
            Assert.Inconclusive("This environment cannot create a directory junction.");
        }

        Assert.ThrowsExactly<IOException>(() => _ = operation.HistoricalInputPath);
        operation.Dispose();
        Assert.IsTrue(File.Exists(externalInput));
        Directory.Delete(inputDirectory, recursive: false);
    }

    private static async Task PublishComparisonAsync(
        DocumentDiffArtifactOperation operation)
    {
        await File.WriteAllTextAsync(operation.HistoricalInputPath, "before");
        await File.WriteAllTextAsync(operation.EffectiveInputPath, "after");
        await using DocumentDiffVerifiedInputLease inputs =
            await operation.OpenVerifiedInputsAsync(
                "sha256:6db7d803e74f1ffa7d8f5adc0bf95b3e15bf4c8373fffadf546227cc6c6742cb",
                "sha256:f39592393ef0859cb196a52693d2cea00fb2df784b3c04ae54aa7cadb8e562f8",
                CancellationToken.None);
        inputs.ConfirmSourceStable();
        string output = operation.PrepareArtifact(
            DocumentDiffArtifactKind.ComparisonDocument,
            "comparison.docx");
        await File.WriteAllTextAsync(output, "derived comparison");
        operation.Complete(SessionId);
    }

    private static void AssertUnavailable(Func<DocumentDiffArtifactReadLease> action)
    {
        DocumentDiffArtifactUnavailableException exception =
            Assert.ThrowsExactly<DocumentDiffArtifactUnavailableException>(action);
        Assert.AreEqual("sessionExpired", exception.Failure);
    }

    private static bool TryCreateJunction(string junction, string target)
    {
        string command = Environment.GetEnvironmentVariable("COMSPEC") ?? "cmd.exe";
        var start = new System.Diagnostics.ProcessStartInfo
        {
            FileName = command,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        foreach (string argument in new[] { "/d", "/c", "mklink", "/J", junction, target })
            start.ArgumentList.Add(argument);
        using System.Diagnostics.Process? process = System.Diagnostics.Process.Start(start);
        if (process is null)
            return false;
        process.WaitForExit();
        return process.ExitCode == 0;
    }

    private sealed class TemporaryDirectory : IDisposable
    {
        public TemporaryDirectory()
        {
            Path = System.IO.Path.Combine(
                System.IO.Path.GetTempPath(),
                $"vibetable-diff-artifacts-{Guid.NewGuid():N}");
            Directory.CreateDirectory(Path);
        }

        public string Path { get; }

        public void Dispose()
        {
            if (Directory.Exists(Path))
                Directory.Delete(Path, recursive: true);
        }
    }
}
