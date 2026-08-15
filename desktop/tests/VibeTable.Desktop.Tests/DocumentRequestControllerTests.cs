using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DocumentRequestControllerTests
{
    [TestMethod]
    public async Task DispatchesImportRelinkAndDragThroughOneClosedInterface()
    {
        var commands = new FakeDocumentCommands();
        var sink = new FakeWebReplySink();
        string? draggedPath = null;
        var controller = Controller(
            sink,
            commands,
            dragOut: path => draggedPath = path);

        await controller.DispatchAsync(Request("document.importRequested", "{}"));
        await controller.DispatchAsync(Request(
            "document.relinkRequested",
            """{"handle":"document-handle"}"""));
        await controller.DispatchAsync(Request(
            "document.dragOutRequested",
            """{"handle":"document-handle"}"""));

        Assert.AreEqual(1, commands.PickerImports);
        CollectionAssert.AreEqual(new[] { "document-handle" }, commands.Relinks);
        CollectionAssert.AreEqual(new[] { "document-handle" }, commands.DragHandles);
        Assert.AreEqual(commands.DragPath, draggedPath);
        Assert.AreEqual(2, sink.Replies.Count(reply =>
            reply.Type == "document.workspaceChanged"));
        CollectionAssert.AreEqual(
            new[] { "import", "relink" },
            sink.Replies
                .Where(reply => reply.Type == "document.workspaceChanged")
                .Select(reply => ReadString(reply.Payload, "reason"))
                .ToArray());
    }

    [TestMethod]
    public async Task ExternalDropCapsTheBatchAtOneHundredFiles()
    {
        var commands = new FakeDocumentCommands();
        var sink = new FakeWebReplySink();
        string[] nativePaths = Enumerable.Range(0, 105)
            .Select(index => $@"C:\drop\{index}.txt")
            .ToArray();
        var controller = Controller(sink, commands, nativePaths);

        await controller.DispatchAsync(Request(
            "document.externalDropRequested",
            "{}"));

        Assert.HasCount(100, commands.HostImports);
        FakeWebReplySink.Reply changed = sink.Replies.Single(reply =>
            reply.Type == "document.workspaceChanged");
        Assert.AreEqual(100, ReadInt32(changed.Payload, "affectedCount"));
    }

    [TestMethod]
    public async Task ExternalDropReportsPartialSuccessBeforeStableFailure()
    {
        var commands = new FakeDocumentCommands
        {
            FailImportAtCall = 3,
            ImportFailure = new DocumentFileOperationException(
                "源文件已失效。",
                "DOCUMENT_SOURCE_INVALID"),
        };
        var sink = new FakeWebReplySink();
        var controller = Controller(
            sink,
            commands,
            [@"C:\drop\one.txt", @"C:\drop\two.txt", @"C:\drop\three.txt"]);

        await controller.DispatchAsync(Request(
            "document.externalDropRequested",
            "{}"));

        Assert.HasCount(2, commands.HostImports);
        Assert.AreEqual(
            2,
            ReadInt32(sink.Replies[0].Payload, "affectedCount"));
        var failure = (DocumentOperationFailedPayload)sink.Replies[1].Payload!;
        Assert.AreEqual("源文件已失效。", failure.Message);
        Assert.AreEqual("DOCUMENT_SOURCE_INVALID", failure.Code);
    }

    [TestMethod]
    public async Task RejectsMissingWorkspacePathsHandlesAndUnknownTypes()
    {
        var unavailableSink = new FakeWebReplySink();
        var unavailable = Controller(unavailableSink, commands: null);
        await unavailable.DispatchAsync(Request(
            "document.externalDropRequested",
            "{}"));
        await unavailable.DispatchAsync(Request("document.importRequested", "{}"));
        AssertFailure(
            unavailableSink.Replies[0],
            "DOCUMENT_DROP_OBJECTS_MISSING");
        AssertFailure(
            unavailableSink.Replies[1],
            "DOCUMENT_WORKSPACE_UNAVAILABLE");

        var commands = new FakeDocumentCommands();
        var sink = new FakeWebReplySink();
        var controller = Controller(sink, commands);
        await controller.DispatchAsync(Request("document.externalDropRequested", "{}"));
        await controller.DispatchAsync(Request("document.relinkRequested", "{}"));
        await controller.DispatchAsync(Request("document.rawRequested", "{}"));

        AssertFailure(sink.Replies[0], "DOCUMENT_DROP_OBJECTS_MISSING");
        AssertFailure(sink.Replies[1], "BAD_PAYLOAD");
        Assert.AreEqual("operation.failed", sink.Replies[2].Type);
        Assert.AreEqual("UNKNOWN_TYPE", ReadString(sink.Replies[2].Payload, "code"));
    }

    [TestMethod]
    public async Task CancellationProducesNoSuccessOrFailureNotification()
    {
        var commands = new FakeDocumentCommands
        {
            ImportFailure = new OperationCanceledException(),
            FailImportAtCall = 1,
        };
        var sink = new FakeWebReplySink();
        var controller = Controller(
            sink,
            commands,
            [@"C:\drop\cancelled.txt"]);

        await controller.DispatchAsync(Request(
            "document.externalDropRequested",
            "{}"));

        Assert.IsEmpty(sink.Replies);
    }

    [TestMethod]
    public void HandlesOnlyTheClosedDocumentCommandUnion()
    {
        foreach (string type in new[]
        {
            "document.importRequested",
            "document.externalDropRequested",
            "document.relinkRequested",
            "document.dragOutRequested",
        })
        {
            Assert.IsTrue(DocumentRequestController.Handles(type), type);
        }
        Assert.IsFalse(DocumentRequestController.Handles("document.rawRequested"));
        Assert.IsFalse(DocumentRequestController.Handles("file.uploadRequested"));
    }

    private static DocumentRequestController Controller(
        FakeWebReplySink sink,
        IWorkspaceDocumentCommands? commands,
        IReadOnlyList<string>? nativePaths = null,
        Action<string>? dragOut = null)
        => new(
            sink,
            () => commands,
            () => nativePaths ?? [],
            dragOut ?? (_ => { }));

    private static RoutedWebRequest Request(string type, string json)
    {
        using JsonDocument document = JsonDocument.Parse(json);
        return new RoutedWebRequest(
            type,
            $"request-{type}",
            document.RootElement.Clone(),
            string.Empty);
    }

    private static void AssertFailure(
        FakeWebReplySink.Reply reply,
        string code)
    {
        Assert.AreEqual("document.operationFailed", reply.Type);
        Assert.AreEqual(
            code,
            ((DocumentOperationFailedPayload)reply.Payload!).Code);
    }

    private static string? ReadString(object? payload, string property)
        => JsonSerializer.SerializeToElement(payload)
            .GetProperty(property)
            .GetString();

    private static int ReadInt32(object? payload, string property)
        => JsonSerializer.SerializeToElement(payload)
            .GetProperty(property)
            .GetInt32();

    private sealed class FakeDocumentCommands : IWorkspaceDocumentCommands
    {
        private int _importCalls;

        public int PickerImports { get; private set; }
        public List<string> HostImports { get; } = [];
        public List<string> Relinks { get; } = [];
        public List<string> DragHandles { get; } = [];
        public int? FailImportAtCall { get; init; }
        public Exception? ImportFailure { get; init; }
        public string DragPath { get; init; } = @"C:\workspace\files\document.txt";

        public Task<WorkspaceDocumentImportResult?> ImportFromPickerAsync(
            CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            PickerImports++;
            return Task.FromResult<WorkspaceDocumentImportResult?>(
                new WorkspaceDocumentImportResult(Guid.NewGuid(), "document.txt"));
        }

        public Task<WorkspaceDocumentImportResult> ImportFromHostPathAsync(
            string sourcePath,
            CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            _importCalls++;
            if (FailImportAtCall == _importCalls)
                throw ImportFailure ?? new InvalidOperationException("import failed");
            HostImports.Add(sourcePath);
            return Task.FromResult(
                new WorkspaceDocumentImportResult(Guid.NewGuid(), "document.txt"));
        }

        public Task<WorkspaceDocumentRelinkResult?> RelinkFromPickerAsync(
            string handle,
            CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            Relinks.Add(handle);
            return Task.FromResult<WorkspaceDocumentRelinkResult?>(
                new WorkspaceDocumentRelinkResult(
                    Guid.NewGuid(),
                    Guid.NewGuid(),
                    "document.txt"));
        }

        public string ResolveDragOutPath(string handle)
        {
            DragHandles.Add(handle);
            return DragPath;
        }
    }
}
