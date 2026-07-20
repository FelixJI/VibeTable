using System.Collections.Concurrent;
using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Storage;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class DocumentWorkspaceHostServiceTests
{
    [TestMethod]
    public async Task ListAsync_IssuesOpaqueHandleWithoutLeakingLocalRoot()
    {
        using var fixture = new DocumentFixture();
        var payload = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);

        var entry = payload.Entries.Single();
        Assert.AreEqual("available", entry.Availability);
        Assert.IsTrue(entry.EntryHandle.StartsWith("doc-", StringComparison.Ordinal));
        Assert.IsTrue(entry.Capabilities.Contains("open"));
        string json = JsonSerializer.Serialize(payload);
        Assert.IsFalse(json.Contains(fixture.WorkspaceRoot, StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(json.Contains("contracts/report.docx", StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public async Task Open_ResolvesHandleInsideMountedWorkspace()
    {
        using var fixture = new DocumentFixture();
        var payload = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);

        fixture.Service.Open(payload.Entries.Single().EntryHandle);

        Assert.AreEqual(fixture.DocumentPath, fixture.Actions.OpenedPath);
    }

    [TestMethod]
    public async Task Preview_UsesIsolatedPreviewCapabilityWhenAvailable()
    {
        using var fixture = new DocumentFixture();
        var payload = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);

        var entry = payload.Entries.Single();
        Assert.IsTrue(entry.Capabilities.Contains("preview"));
        Assert.AreEqual("system", entry.PreviewKind);

        fixture.Service.Preview(entry.EntryHandle);

        Assert.AreEqual(fixture.DocumentPath, fixture.Preview.PreviewedPath);
    }

    [TestMethod]
    public async Task MissingFile_OffersRelocateButCannotOpen()
    {
        using var fixture = new DocumentFixture(createFile: false);
        var payload = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);
        var entry = payload.Entries.Single();

        Assert.AreEqual("missing", entry.Availability);
        Assert.IsTrue(entry.Capabilities.Contains("relocate"));
        Assert.IsFalse(entry.Capabilities.Contains("open"));
        var error = Assert.Throws<DocumentCapabilityException>(
            () => fixture.Service.Open(entry.EntryHandle));
        Assert.AreEqual("DOCUMENT_CAPABILITY_DENIED", error.Code);
    }

    [TestMethod]
    public async Task GlobalList_DoesNotIssueUnlinkCapability()
    {
        using var fixture = new DocumentFixture(linkId: null);
        var payload = await fixture.Service.ListGlobalAsync(CancellationToken.None);

        Assert.IsFalse(payload.Entries.Single().Capabilities.Contains("unlink"));
    }

    [TestMethod]
    public async Task DirectusPathMetadata_CannotRedirectLocalCapability()
    {
        using var fixture = new DocumentFixture(remoteFolder: ".backup/objects");
        var payload = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);

        fixture.Service.Open(payload.Entries.Single().EntryHandle);

        Assert.AreEqual(fixture.DocumentPath, fixture.Actions.OpenedPath);
    }

    [TestMethod]
    public async Task MissingLocalManifest_IsUnmanagedAndCannotOpen()
    {
        using var fixture = new DocumentFixture(createCatalog: false);
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        Assert.AreEqual("unmanaged", entry.Availability);
        Assert.IsFalse(entry.Capabilities.Contains("open"));
    }

    [TestMethod]
    public void CapabilityStore_RejectsExpiredHandle()
    {
        var now = new DateTimeOffset(2026, 7, 19, 12, 0, 0, TimeSpan.Zero);
        var store = new DocumentCapabilityStore(() => now, TimeSpan.FromSeconds(1));
        string handle = store.Issue(
            "workspace-1", "doc-1", "link-1", "file.txt", ["open"]);
        now = now.AddSeconds(2);

        var error = Assert.Throws<DocumentCapabilityException>(
            () => store.Resolve(handle, "open"));
        Assert.AreEqual("DOCUMENT_HANDLE_EXPIRED", error.Code);
    }

    [TestMethod]
    public void CapabilityStore_RotateEpochInvalidatesDocumentAndRevisionHandles()
    {
        var store = new DocumentCapabilityStore();
        string documentHandle = store.Issue(
            "workspace-1", "doc-1", "link-1", "file.txt", ["open"]);
        string revisionHandle = store.IssueRevision(
            "workspace-1", "doc-1", "revision-1", ["preview"]);
        long originalEpoch = store.Resolve(documentHandle, "open").Epoch;

        long rotatedEpoch = store.RotateEpoch();

        Assert.AreNotEqual(originalEpoch, rotatedEpoch);
        var documentError = Assert.Throws<DocumentCapabilityException>(
            () => store.Resolve(documentHandle, "open"));
        Assert.AreEqual("DOCUMENT_HANDLE_INVALID", documentError.Code);
        var revisionError = Assert.Throws<DocumentCapabilityException>(
            () => store.ResolveRevision(revisionHandle, "preview"));
        Assert.AreEqual("REVISION_HANDLE_INVALID", revisionError.Code);

        string newHandle = store.Issue(
            "workspace-1", "doc-2", null, "new-file.txt", ["open"]);
        Assert.AreEqual(rotatedEpoch, store.Resolve(newHandle, "open").Epoch);
    }

    [TestMethod]
    public void CapabilityStore_ConcurrentIssueResolveRevokeAndPrune_AreThreadSafe()
    {
        var now = new DateTimeOffset(2026, 7, 19, 12, 0, 0, TimeSpan.Zero);
        var store = new DocumentCapabilityStore(() => now, TimeSpan.FromMinutes(5));
        var unexpectedErrors = new ConcurrentQueue<Exception>();

        Parallel.For(0, 2_000, index =>
        {
            try
            {
                if (index % 19 == 0)
                {
                    store.RotateEpoch();
                    return;
                }
                if (index % 17 == 0)
                {
                    store.RevokeAll();
                    return;
                }
                if (index % 13 == 0)
                {
                    store.PruneExpired();
                    return;
                }
                if (index % 5 == 0)
                {
                    string revisionHandle = store.IssueRevision(
                        "workspace-1", $"doc-{index}", $"revision-{index}", ["preview"]);
                    TryResolve(() => store.ResolveRevision(revisionHandle, "preview"));
                    return;
                }

                string handle = store.Issue(
                    "workspace-1", $"doc-{index}", null, $"file-{index}.txt", ["open"]);
                TryResolve(() => store.Resolve(handle, "open"));
            }
            catch (Exception ex)
            {
                unexpectedErrors.Enqueue(ex);
            }
        });

        Assert.AreEqual(
            0,
            unexpectedErrors.Count,
            string.Join(Environment.NewLine, unexpectedErrors.Select(error => error.ToString())));

        static void TryResolve(Action resolve)
        {
            try
            {
                resolve();
            }
            catch (DocumentCapabilityException error)
                when (error.Code is "DOCUMENT_HANDLE_INVALID" or "REVISION_HANDLE_INVALID")
            {
                // A concurrent RevokeAll may legitimately win after Issue.
            }
        }
    }

    private sealed class DocumentFixture : IDisposable
    {
        private readonly string _baseDirectory;

        public DocumentFixture(
            bool createFile = true,
            string? linkId = "link-1",
            bool createCatalog = true,
            string remoteFolder = "contracts")
        {
            _baseDirectory = Path.Combine(
                Path.GetTempPath(), "vibetable-doc-host-" + Guid.NewGuid().ToString("N"));
            WorkspaceRoot = Path.Combine(_baseDirectory, "workspace");
            Directory.CreateDirectory(Path.Combine(WorkspaceRoot, "contracts"));
            DocumentPath = Path.Combine(WorkspaceRoot, "contracts", "report.docx");
            if (createFile) File.WriteAllText(DocumentPath, "document");

            var mounts = new WorkspaceMountStore(_baseDirectory);
            mounts.Mount("workspace-1", WorkspaceRoot, "Workspace");
            if (createCatalog)
            {
                var catalog = new DocumentCatalogStore(
                    Path.Combine(WorkspaceRoot, ".backup"), new AtomicJsonStore());
                catalog.WriteFolder(new FolderManifest(
                    1, "folder-1", "workspace-1", null, "contracts", "active",
                    "2026-07-19T12:00:00Z"));
                catalog.WriteDocument(new DocumentManifest(
                    1, "doc-1", "workspace-1", "folder-1", "report.docx",
                    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                    "active", "2026-07-19T12:00:00Z"));
            }
            Gateway = new FakeDocumentGateway(linkId, remoteFolder);
            Actions = new FakeLocalDocumentActions();
            Preview = new FakeLocalDocumentPreview();
            Service = new DocumentWorkspaceHostService(
                Gateway,
                mounts,
                new DocumentCapabilityStore(),
                Actions,
                Preview);
        }

        public string WorkspaceRoot { get; }
        public string DocumentPath { get; }
        public FakeDocumentGateway Gateway { get; }
        public FakeLocalDocumentActions Actions { get; }
        public FakeLocalDocumentPreview Preview { get; }
        public DocumentWorkspaceHostService Service { get; }

        public void Dispose()
        {
            try
            {
                if (Directory.Exists(_baseDirectory))
                    Directory.Delete(_baseDirectory, recursive: true);
            }
            catch
            {
                // Best effort only; tests never delete outside their random temp root.
            }
        }
    }
}

internal sealed class FakeDocumentGateway : IDocumentWorkspaceRpcGateway
{
    private readonly DocumentSummary _document;

    public FakeDocumentGateway(string? linkId = "link-1", string folder = "contracts")
    {
        _document = new DocumentSummary(
            linkId,
            "doc-1",
            "workspace-1",
            "report.docx",
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            "rev-1",
            "abcdef",
            "active",
            linkId is null ? null : "attachment",
            folder,
            false);
    }

    public Task<DocumentListResult> ReadDocumentsAsync(
        int limit, int offset, CancellationToken token)
        => Task.FromResult(new DocumentListResult([_document with { LinkId = null }], 1));

    public Task<FolderResult> ReadFolderAsync(
        string collection, string itemId, CancellationToken token)
        => Task.FromResult(new FolderResult(collection, itemId, null, [_document]));

    public Task<DocumentHistoryResult> ReadHistoryAsync(
        string documentId, int limit, int offset, CancellationToken token)
        => Task.FromResult(new DocumentHistoryResult(
            documentId,
            [new DocumentRevisionEntry(
                "rev-1", "main", 1, "V1", "formal", "abcdef", 8,
                "2026-07-19T12:00:00Z", "Test User")],
            1));

    public Task UnlinkAsync(string linkId, CancellationToken token)
        => Task.CompletedTask;
}

internal sealed class FakeLocalDocumentActions : ILocalDocumentActions
{
    public string? OpenedPath { get; private set; }
    public string? RevealedPath { get; private set; }

    public void Open(string fullPath) => OpenedPath = fullPath;
    public void Reveal(string fullPath) => RevealedPath = fullPath;
}

internal sealed class FakeLocalDocumentPreview : ILocalDocumentPreview
{
    public string? PreviewedPath { get; private set; }

    public bool CanPreview(string fullPath) => true;
    public void Show(string fullPath) => PreviewedPath = fullPath;
    public void Dispose() { }
}
