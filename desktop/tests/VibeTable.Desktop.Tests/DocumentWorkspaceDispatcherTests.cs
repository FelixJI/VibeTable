using System.IO;
using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DocumentWorkspaceDispatcherTests
{
    [TestMethod]
    public async Task ListThenOpen_UsesOpaqueHandleAndCorrelatedResponses()
    {
        string temp = Path.Combine(
            Path.GetTempPath(), "vibetable-doc-dispatch-" + Guid.NewGuid().ToString("N"));
        try
        {
            string root = Path.Combine(temp, "workspace");
            Directory.CreateDirectory(Path.Combine(root, "contracts"));
            string expectedPath = Path.Combine(root, "contracts", "report.docx");
            File.WriteAllText(expectedPath, "document");
            var mounts = new WorkspaceMountStore(temp);
            mounts.Mount("workspace-1", root, "Workspace");
            var catalog = new DocumentCatalogStore(
                Path.Combine(root, ".backup"), new AtomicJsonStore());
            catalog.WriteFolder(new FolderManifest(
                1, "folder-1", "workspace-1", null, "contracts", "active",
                "2026-07-19T12:00:00Z"));
            catalog.WriteDocument(new DocumentManifest(
                1, "doc-1", "workspace-1", "folder-1", "report.docx",
                "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                "active", "2026-07-19T12:00:00Z"));
            var actions = new FakeLocalDocumentActions();
            var documents = new DocumentWorkspaceHostService(
                new FakeDocumentGateway(),
                mounts,
                new DocumentCapabilityStore(),
                actions);
            var sink = new FakeWebReplySink();
            var dispatcher = new WorkspaceRequestDispatcher(
                new TableWorkspaceService(new FakeTableRpcGateway()),
                new FakeDatabasePicker("directus://configured"),
                sink);
            dispatcher.SetDocumentWorkspace(documents);

            using var listJson = JsonDocument.Parse(
                """{"scope":{"kind":"record","collection":"orders","itemId":42},"authority":"workspace"}""");
            dispatcher.Dispatch(new RoutedWebRequest(
                "document.listRequested", "list-1", listJson.RootElement.Clone(), ""));
            var listReply = await sink.WaitForAsync("document.listLoaded");
            Assert.IsNotNull(listReply);
            Assert.AreEqual("list-1", listReply!.RequestId);
            var listPayload = (DocumentListPayload)listReply.Payload!;
            string handle = listPayload.Entries.Single().EntryHandle;

            using var openJson = JsonDocument.Parse(
                $$"""{"entryHandle":"{{handle}}"}""");
            dispatcher.Dispatch(new RoutedWebRequest(
                "document.openRequested", "open-1", openJson.RootElement.Clone(), ""));
            var openReply = await sink.WaitForAsync("document.actionCompleted");

            Assert.IsNotNull(openReply);
            Assert.AreEqual("open-1", openReply!.RequestId);
            Assert.AreEqual(expectedPath, actions.OpenedPath);
        }
        finally
        {
            try { if (Directory.Exists(temp)) Directory.Delete(temp, recursive: true); }
            catch { }
        }
    }

    [TestMethod]
    public async Task DocumentActionFailure_DoesNotExposeExceptionMessageOrAbsolutePath()
    {
        string temp = Path.Combine(
            Path.GetTempPath(), "vibetable-doc-failure-" + Guid.NewGuid().ToString("N"));
        try
        {
            string root = Path.Combine(temp, "workspace");
            Directory.CreateDirectory(root);
            string documentPath = Path.Combine(root, "secret-report.docx");
            File.WriteAllText(documentPath, "document");
            var mounts = new WorkspaceMountStore(temp);
            mounts.Mount("workspace-1", root, "Workspace");
            var capabilities = new DocumentCapabilityStore();
            string handle = capabilities.Issue(
                "workspace-1", "doc-1", null, "secret-report.docx", ["open"]);
            using var documents = new DocumentWorkspaceHostService(
                new FakeDocumentGateway(),
                mounts,
                capabilities,
                new ThrowingLocalDocumentActions(),
                new FakeLocalDocumentPreview());
            var sink = new FakeWebReplySink();
            var dispatcher = CreateDispatcher(sink, documents);

            using var requestJson = JsonDocument.Parse(
                $$"""{"entryHandle":"{{handle}}"}""");
            dispatcher.Dispatch(new RoutedWebRequest(
                "document.openRequested", "open-failure", requestJson.RootElement.Clone(), ""));

            var failure = await sink.WaitForFailedAsync();
            Assert.IsNotNull(failure);
            var payload = JsonSerializer.SerializeToElement(failure.Payload);
            Assert.AreEqual("DOCUMENT_ACTION_FAILED", payload.GetProperty("code").GetString());
            Assert.AreEqual("文档操作失败，请稍后重试。", payload.GetProperty("message").GetString());
            Assert.IsFalse(
                payload.GetProperty("message").GetString()!.Contains(
                    documentPath,
                    StringComparison.OrdinalIgnoreCase));
        }
        finally
        {
            try { if (Directory.Exists(temp)) Directory.Delete(temp, recursive: true); }
            catch { }
        }
    }

    [TestMethod]
    public async Task DocumentPreviewFailure_UsesCodeSpecificSafeMessage()
    {
        string temp = Path.Combine(
            Path.GetTempPath(), "vibetable-preview-failure-" + Guid.NewGuid().ToString("N"));
        try
        {
            string root = Path.Combine(temp, "workspace");
            Directory.CreateDirectory(root);
            string documentPath = Path.Combine(root, "secret-report.docx");
            File.WriteAllText(documentPath, "document");
            var mounts = new WorkspaceMountStore(temp);
            mounts.Mount("workspace-1", root, "Workspace");
            var capabilities = new DocumentCapabilityStore();
            string handle = capabilities.Issue(
                "workspace-1", "doc-1", null, "secret-report.docx", ["preview"]);
            using var documents = new DocumentWorkspaceHostService(
                new FakeDocumentGateway(),
                mounts,
                capabilities,
                new FakeLocalDocumentActions(),
                new ThrowingDocumentPreview());
            var sink = new FakeWebReplySink();
            var dispatcher = CreateDispatcher(sink, documents);

            using var requestJson = JsonDocument.Parse(
                $$"""{"entryHandle":"{{handle}}"}""");
            dispatcher.Dispatch(new RoutedWebRequest(
                "document.previewRequested", "preview-failure", requestJson.RootElement.Clone(), ""));

            var failure = await sink.WaitForFailedAsync();
            Assert.IsNotNull(failure);
            var payload = JsonSerializer.SerializeToElement(failure.Payload);
            Assert.AreEqual("PREVIEW_HANDLER_FAILED", payload.GetProperty("code").GetString());
            Assert.AreEqual(
                "文件预览失败，请使用默认应用打开。",
                payload.GetProperty("message").GetString());
            Assert.IsFalse(
                payload.GetProperty("message").GetString()!.Contains(
                    documentPath,
                    StringComparison.OrdinalIgnoreCase));
        }
        finally
        {
            try { if (Directory.Exists(temp)) Directory.Delete(temp, recursive: true); }
            catch { }
        }
    }

    [TestMethod]
    public async Task RotateDocumentCapabilityEpoch_InvalidatesPreviouslyIssuedHandle()
    {
        string temp = Path.Combine(
            Path.GetTempPath(), "vibetable-doc-epoch-" + Guid.NewGuid().ToString("N"));
        try
        {
            string root = Path.Combine(temp, "workspace");
            Directory.CreateDirectory(root);
            string documentPath = Path.Combine(root, "report.docx");
            File.WriteAllText(documentPath, "document");
            var mounts = new WorkspaceMountStore(temp);
            mounts.Mount("workspace-1", root, "Workspace");
            var capabilities = new DocumentCapabilityStore();
            string handle = capabilities.Issue(
                "workspace-1", "doc-1", null, "report.docx", ["open"]);
            using var documents = new DocumentWorkspaceHostService(
                new FakeDocumentGateway(),
                mounts,
                capabilities,
                new FakeLocalDocumentActions(),
                new FakeLocalDocumentPreview());
            var sink = new FakeWebReplySink();
            var dispatcher = CreateDispatcher(sink, documents);

            dispatcher.RotateDocumentCapabilityEpoch();

            using var requestJson = JsonDocument.Parse(
                $$"""{"entryHandle":"{{handle}}"}""");
            dispatcher.Dispatch(new RoutedWebRequest(
                "document.openRequested", "open-stale", requestJson.RootElement.Clone(), ""));

            var failure = await sink.WaitForFailedAsync();
            Assert.IsNotNull(failure);
            Assert.AreEqual("open-stale", failure!.RequestId);
            var payload = JsonSerializer.SerializeToElement(failure.Payload);
            Assert.AreEqual(
                "DOCUMENT_HANDLE_INVALID",
                payload.GetProperty("code").GetString());
        }
        finally
        {
            try { if (Directory.Exists(temp)) Directory.Delete(temp, recursive: true); }
            catch { }
        }
    }

    private static WorkspaceRequestDispatcher CreateDispatcher(
        FakeWebReplySink sink,
        DocumentWorkspaceHostService documents)
    {
        var dispatcher = new WorkspaceRequestDispatcher(
            new TableWorkspaceService(new FakeTableRpcGateway()),
            new FakeDatabasePicker("directus://configured"),
            sink);
        dispatcher.SetDocumentWorkspace(documents);
        return dispatcher;
    }

    private sealed class ThrowingLocalDocumentActions : ILocalDocumentActions
    {
        public void Open(string fullPath)
            => throw new InvalidOperationException($"Cannot open private file: {fullPath}");

        public void Reveal(string fullPath)
            => throw new InvalidOperationException($"Cannot reveal private file: {fullPath}");
    }

    private sealed class ThrowingDocumentPreview : ILocalDocumentPreview
    {
        public bool CanPreview(string fullPath) => true;

        public void Show(string fullPath)
            => throw new DocumentPreviewException(
                $"Preview handler failed for private file: {fullPath}",
                "PREVIEW_HANDLER_FAILED");

        public void Dispose()
        {
        }
    }
}
