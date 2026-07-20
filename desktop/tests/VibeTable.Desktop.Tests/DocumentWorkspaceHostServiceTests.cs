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
    public async Task DragOut_ResolvesNativePathOnlyForSafeExistingFile()
    {
        using var fixture = new DocumentFixture();
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        Assert.IsTrue(entry.Capabilities.Contains("dragOut"));
        Assert.AreEqual(
            fixture.DocumentPath,
            fixture.Service.ResolveDragOutPath(entry.EntryHandle));
        Assert.IsFalse(JsonSerializer.Serialize(entry).Contains(
            fixture.WorkspaceRoot,
            StringComparison.OrdinalIgnoreCase));
    }

    [TestMethod]
    public void DragOut_ForgedHandleIsRejected()
    {
        using var fixture = new DocumentFixture();

        var error = Assert.Throws<DocumentCapabilityException>(
            () => fixture.Service.ResolveDragOutPath("doc-forged"));

        Assert.AreEqual("DOCUMENT_HANDLE_INVALID", error.Code);
    }

    [TestMethod]
    public async Task DragOut_ExpiredHandleIsRejected()
    {
        var now = new DateTimeOffset(2026, 7, 20, 12, 0, 0, TimeSpan.Zero);
        var capabilities = new DocumentCapabilityStore(
            () => now,
            TimeSpan.FromSeconds(1));
        using var fixture = new DocumentFixture(capabilityStore: capabilities);
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();
        now = now.AddSeconds(2);

        var error = Assert.Throws<DocumentCapabilityException>(
            () => fixture.Service.ResolveDragOutPath(entry.EntryHandle));

        Assert.AreEqual("DOCUMENT_HANDLE_EXPIRED", error.Code);
    }

    [TestMethod]
    public async Task DragOut_FileRemovedAfterCapabilityIssueIsRejected()
    {
        using var fixture = new DocumentFixture();
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();
        File.Delete(fixture.DocumentPath);

        var error = Assert.Throws<DocumentCapabilityException>(
            () => fixture.Service.ResolveDragOutPath(entry.EntryHandle));

        Assert.AreEqual("DOCUMENT_MISSING", error.Code);
    }

    [TestMethod]
    public async Task DragOut_DangerousExtensionDoesNotReceiveCapability()
    {
        using var fixture = new DocumentFixture();
        File.Delete(fixture.DocumentPath);
        string dangerousPath = Path.Combine(
            fixture.WorkspaceRoot,
            "contracts",
            "payload.ps1");
        File.WriteAllText(dangerousPath, "unsafe");
        var catalog = new DocumentCatalogStore(fixture.BackupRoot, new AtomicJsonStore());
        var manifest = catalog.ReadDocument("doc-1")!;
        catalog.WriteDocument(manifest with
        {
            FileName = "payload.ps1",
            MimeType = "text/plain",
        });

        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        Assert.IsFalse(entry.Capabilities.Contains("dragOut"));
        var error = Assert.Throws<DocumentCapabilityException>(
            () => fixture.Service.ResolveDragOutPath(entry.EntryHandle));
        Assert.AreEqual("DOCUMENT_CAPABILITY_DENIED", error.Code);
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
    public async Task LocalMount_FromAnotherAuthenticatedUser_IsNeverGranted()
    {
        using var fixture = new DocumentFixture(
            mountPartition: "local:default|user:user-a",
            servicePartition: "local:default|user:user-b");

        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        Assert.AreEqual("unmounted", entry.Availability);
        Assert.IsFalse(entry.Capabilities.Contains("open"));
        Assert.IsFalse(entry.Capabilities.Contains("dragOut"));
    }

    [TestMethod]
    public async Task ImportFromPicker_CopiesFileAndCommitsLocalManifestWithoutLeakingPaths()
    {
        using var fixture = new DocumentFixture();
        string sourcePath = fixture.CreatePickerFile("meeting notes.txt", "safe content");
        fixture.Picker.SelectedPath = sourcePath;

        var result = await fixture.Service.ImportFromPickerAsync(
            new DocumentImportRequest(
                "workspace-1", "folder-1", "orders", "42", "attachment"),
            CancellationToken.None);

        Assert.IsNotNull(result);
        Assert.AreEqual("meeting notes.txt", result.DisplayName);
        Assert.AreEqual("text/plain", result.MimeType);
        Assert.IsTrue(Guid.TryParseExact(result.DocumentId, "D", out _));
        Assert.IsTrue(Guid.TryParseExact(result.SchemeId, "D", out _));
        Assert.IsTrue(Guid.TryParseExact(result.RevisionId, "D", out _));
        Assert.AreEqual("link-imported", result.LinkId);
        string destinationPath = Path.Combine(
            fixture.WorkspaceRoot, "contracts", "meeting notes.txt");
        Assert.AreEqual("safe content", File.ReadAllText(destinationPath));
        Assert.IsTrue(File.Exists(sourcePath), "默认导入应复制而不是移动源文件。");
        var catalog = new DocumentCatalogStore(fixture.BackupRoot, new AtomicJsonStore());
        var manifest = catalog.ReadDocument(result.DocumentId);
        Assert.IsNotNull(manifest);
        Assert.AreEqual("folder-1", manifest.FolderId);
        Assert.AreEqual("meeting notes.txt", manifest.FileName);
        string resultJson = JsonSerializer.Serialize(result);
        Assert.IsFalse(resultJson.Contains(fixture.WorkspaceRoot, StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(resultJson.Contains(sourcePath, StringComparison.OrdinalIgnoreCase));
        var registration = fixture.Gateway.RegisterRequests.Single();
        Assert.AreEqual("Workspace", registration.WorkspaceName);
        Assert.AreEqual(result.DocumentId, registration.DocumentId);
        Assert.AreEqual(result.RevisionId, registration.RevisionId);
        Assert.AreEqual(result.SchemeId, registration.SchemeId);
        Assert.AreEqual("orders", registration.ItemCollection);
        Assert.AreEqual("42", registration.ItemId);
        Assert.AreEqual(64, registration.Hash.Length);
        Assert.AreEqual(new FileInfo(destinationPath).Length, registration.Size);
        string registrationJson = JsonSerializer.Serialize(registration);
        Assert.IsFalse(registrationJson.Contains(
            fixture.WorkspaceRoot,
            StringComparison.OrdinalIgnoreCase));
        Assert.IsFalse(registrationJson.Contains(sourcePath, StringComparison.OrdinalIgnoreCase));
        var revision = new RevisionStore(
            fixture.BackupRoot,
            new AtomicJsonStore()).Read(result.DocumentId, result.RevisionId);
        Assert.IsNotNull(revision);
        Assert.AreEqual(registration.Hash, revision.ContentHash);
        var schemeRef = new RefStore(
            fixture.BackupRoot,
            new AtomicJsonStore()).Read(result.DocumentId, result.SchemeId);
        Assert.IsNotNull(schemeRef);
        Assert.AreEqual(result.RevisionId, schemeRef.HeadRevisionId);
        Assert.AreEqual("main", schemeRef.SchemeName);
    }

    [TestMethod]
    public async Task ImportFromPicker_NameConflictCreatesNonOverwritingCopy()
    {
        using var fixture = new DocumentFixture();
        string sourcePath = fixture.CreatePickerFile("report.docx", "new document");
        fixture.Picker.SelectedPath = sourcePath;

        var result = await fixture.Service.ImportFromPickerAsync(
            new DocumentImportRequest("workspace-1", "folder-1"),
            CancellationToken.None);

        Assert.IsNotNull(result);
        Assert.AreEqual("report (1).docx", result.DisplayName);
        Assert.AreEqual("document", File.ReadAllText(fixture.DocumentPath));
        Assert.AreEqual(
            "new document",
            File.ReadAllText(Path.Combine(
                fixture.WorkspaceRoot, "contracts", "report (1).docx")));
    }

    [TestMethod]
    public async Task ImportFromPicker_RejectsDirectoryAndDangerousExtension()
    {
        using var fixture = new DocumentFixture();
        fixture.Picker.SelectedPath = fixture.SourceRoot;

        var directoryError = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.ImportFromPickerAsync(
                new DocumentImportRequest("workspace-1", "folder-1"),
                CancellationToken.None));
        Assert.AreEqual("DOCUMENT_SOURCE_INVALID", directoryError.Code);

        fixture.Picker.SelectedPath = fixture.CreatePickerFile("payload.PS1", "unsafe");
        var extensionError = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.ImportFromPickerAsync(
                new DocumentImportRequest("workspace-1", "folder-1"),
                CancellationToken.None));
        Assert.AreEqual("DOCUMENT_SOURCE_TYPE_DENIED", extensionError.Code);
        Assert.IsFalse(File.Exists(Path.Combine(
            fixture.WorkspaceRoot, "contracts", "payload.PS1")));
    }

    [TestMethod]
    public async Task ImportFromPicker_ManifestFailureRollsBackCopiedFileAndIndexTemp()
    {
        using var fixture = new DocumentFixture(createDocumentManifest: false);
        string documentsPath = Path.Combine(fixture.BackupRoot, "documents");
        File.WriteAllText(documentsPath, "blocks the manifest directory");
        string sourcePath = fixture.CreatePickerFile("new-file.txt", "content");
        fixture.Picker.SelectedPath = sourcePath;

        var error = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.ImportFromPickerAsync(
                new DocumentImportRequest("workspace-1", "folder-1"),
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_IMPORT_FAILED", error.Code);
        Assert.IsFalse(File.Exists(Path.Combine(
            fixture.WorkspaceRoot, "contracts", "new-file.txt")));
        Assert.IsTrue(File.Exists(sourcePath));
        Assert.IsTrue(File.Exists(documentsPath));
        string stagingRoot = Path.Combine(fixture.BackupRoot, ".staging");
        Assert.AreEqual(
            0,
            Directory.Exists(stagingRoot)
                ? Directory.GetFiles(stagingRoot, "*.partial").Length
                : 0);
        Assert.AreEqual(0, fixture.Gateway.RegisterRequests.Count);
    }

    [TestMethod]
    public async Task ImportFromPicker_RegisterFailureKeepsDurableJournalAndRetriesOnRefresh()
    {
        using var fixture = new DocumentFixture();
        fixture.Gateway.RegisterException = new IOException(
            @"remote registration failed for C:\private\source.txt");
        fixture.Picker.SelectedPath = fixture.CreatePickerFile("remote-fail.txt", "content");

        var error = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.ImportFromPickerAsync(
                new DocumentImportRequest("workspace-1", "folder-1"),
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_REGISTER_PENDING", error.Code);
        var registration = fixture.Gateway.RegisterRequests.Single();
        Assert.IsTrue(File.Exists(Path.Combine(
            fixture.WorkspaceRoot, "contracts", "remote-fail.txt")));
        var catalog = new DocumentCatalogStore(fixture.BackupRoot, new AtomicJsonStore());
        Assert.IsNotNull(catalog.ReadDocument(registration.DocumentId));
        Assert.IsTrue(File.Exists(new RevisionStore(
            fixture.BackupRoot,
            new AtomicJsonStore()).GetPath(
                registration.DocumentId,
                registration.RevisionId)));
        Assert.IsTrue(File.Exists(new RefStore(
            fixture.BackupRoot,
            new AtomicJsonStore()).GetPath(
                registration.DocumentId,
                registration.SchemeId)));
        string journalPath = Path.Combine(
            fixture.BackupRoot,
            "import-journal",
            registration.DocumentId + ".json");
        Assert.IsTrue(File.Exists(journalPath));
        Assert.IsFalse(error.Message.Contains("C:\\private", StringComparison.OrdinalIgnoreCase));

        fixture.Gateway.RegisterException = null;
        await fixture.Service.ListGlobalAsync(CancellationToken.None);

        Assert.AreEqual(2, fixture.Gateway.RegisterRequests.Count);
        Assert.AreEqual(registration, fixture.Gateway.RegisterRequests[1]);
        Assert.IsFalse(File.Exists(journalPath));
    }

    [TestMethod]
    public async Task RelinkMissingFromPicker_RestoresCurrentVersionToManifestAuthoritativePath()
    {
        using var fixture = new DocumentFixture(
            createFile: false,
            remoteFolder: ".backup/objects");
        string sourcePath = fixture.CreatePickerFile("report.docx", "document");
        fixture.Picker.SelectedPath = sourcePath;
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        var result = await fixture.Service.RelinkMissingFromPickerAsync(
            entry.EntryHandle,
            CancellationToken.None);

        Assert.IsNotNull(result);
        Assert.AreEqual("doc-1", result.DocumentId);
        Assert.AreEqual("document", File.ReadAllText(fixture.DocumentPath));
        Assert.IsTrue(File.Exists(sourcePath));
        Assert.IsFalse(File.Exists(Path.Combine(
            fixture.WorkspaceRoot, ".backup", "objects", "report.docx")));
        var manifest = new DocumentCatalogStore(
            fixture.BackupRoot,
            new AtomicJsonStore()).ReadDocument("doc-1");
        Assert.IsNotNull(manifest);
        Assert.AreEqual("folder-1", manifest.FolderId);
        Assert.AreEqual("report.docx", manifest.FileName);
    }

    [TestMethod]
    public async Task RelinkMissingFromPicker_RejectsDifferentContentWithoutChangingTarget()
    {
        using var fixture = new DocumentFixture(createFile: false);
        fixture.Picker.SelectedPath = fixture.CreatePickerFile(
            "report.docx",
            "different content");
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        var error = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.RelinkMissingFromPickerAsync(
                entry.EntryHandle,
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_RELINK_CONTENT_MISMATCH", error.Code);
        Assert.IsFalse(File.Exists(fixture.DocumentPath));
    }

    [TestMethod]
    public async Task RelinkMissingFromPicker_RejectsRemoteHeadThatIsNotLocalMainHead()
    {
        using var fixture = new DocumentFixture(createFile: false);
        fixture.Picker.SelectedPath = fixture.CreatePickerFile("report.docx", "document");
        new AtomicJsonStore().Write(
            new RefStore(
                fixture.BackupRoot,
                new AtomicJsonStore()).GetPath("doc-1", "scheme-1"),
            new RefManifest(
                RefManifest.CurrentFormatVersion,
                "doc-1",
                "scheme-1",
                "main",
                "rev-local-newer",
                "contracts/report.docx",
                "2026-07-20T00:00:00Z"));
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        var error = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.RelinkMissingFromPickerAsync(
                entry.EntryHandle,
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_RELINK_HEAD_MISMATCH", error.Code);
        Assert.IsFalse(File.Exists(fixture.DocumentPath));
    }

    [TestMethod]
    public async Task RelinkMissingFromPicker_RejectsTypeMismatchWithoutChangingTarget()
    {
        using var fixture = new DocumentFixture(createFile: false);
        fixture.Picker.SelectedPath = fixture.CreatePickerFile("report.txt", "wrong type");
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        var error = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.RelinkMissingFromPickerAsync(
                entry.EntryHandle,
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_RELINK_TYPE_MISMATCH", error.Code);
        Assert.IsFalse(File.Exists(fixture.DocumentPath));
    }

    [TestMethod]
    public async Task RelinkUnmanagedDocument_FailsBeforeOpeningPicker()
    {
        using var fixture = new DocumentFixture(createDocumentManifest: false);
        var entry = (await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None)).Entries.Single();

        var error = await Assert.ThrowsAsync<DocumentFileOperationException>(
            () => fixture.Service.RelinkMissingFromPickerAsync(
                entry.EntryHandle,
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_MANIFEST_MISSING", error.Code);
        Assert.AreEqual(0, fixture.Picker.PickCount);
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
            string remoteFolder = "contracts",
            bool createDocumentManifest = true,
            DocumentCapabilityStore? capabilityStore = null,
            string? mountPartition = null,
            string? servicePartition = null)
        {
            _baseDirectory = Path.Combine(
                Path.GetTempPath(), "vibetable-doc-host-" + Guid.NewGuid().ToString("N"));
            WorkspaceRoot = Path.Combine(_baseDirectory, "workspace");
            BackupRoot = Path.Combine(WorkspaceRoot, ".backup");
            SourceRoot = Path.Combine(_baseDirectory, "picker-source");
            Directory.CreateDirectory(Path.Combine(WorkspaceRoot, "contracts"));
            Directory.CreateDirectory(SourceRoot);
            DocumentPath = Path.Combine(WorkspaceRoot, "contracts", "report.docx");
            if (createFile) File.WriteAllText(DocumentPath, "document");

            var mounts = new WorkspaceMountStore(_baseDirectory);
            mounts.Mount(
                "workspace-1",
                WorkspaceRoot,
                "Workspace",
                mountPartition);
            var json = new AtomicJsonStore();
            json.Write(Path.Combine(BackupRoot, "workspace.json"), new WorkspaceManifest(
                WorkspaceManifest.CurrentFormatVersion,
                "workspace-1",
                "Workspace",
                "2026-07-19T12:00:00Z"));
            if (createCatalog)
            {
                var catalog = new DocumentCatalogStore(
                    BackupRoot, json);
                catalog.WriteFolder(new FolderManifest(
                    1, "folder-1", "workspace-1", null, "contracts", "active",
                    "2026-07-19T12:00:00Z"));
                if (createDocumentManifest)
                {
                    catalog.WriteDocument(new DocumentManifest(
                        1, "doc-1", "workspace-1", "folder-1", "report.docx",
                        "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                        "active", "2026-07-19T12:00:00Z"));
                    const string schemeId = "scheme-1";
                    const string revisionId = "rev-1";
                    string contentHash = ContentObjectStore.ComputeHash(
                        System.Text.Encoding.UTF8.GetBytes("document"));
                    new RevisionStore(BackupRoot, json).Write(new RevisionManifest(
                        RevisionManifest.CurrentFormatVersion,
                        revisionId,
                        "doc-1",
                        schemeId,
                        ParentRevisionId: null,
                        SourceRevisionId: null,
                        RestoredFromRevisionId: null,
                        Sequence: 1,
                        VersionLabel: "V1",
                        Kind: RevisionKind.Formal,
                        ContentHash: contentHash,
                        Size: 8,
                        MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                        WorkingRelativePath: "contracts/report.docx",
                        CreatedAt: "2026-07-19T12:00:00Z",
                        CreatedBy: null,
                        DeviceId: null,
                        Comment: null));
                    new RefStore(BackupRoot, json).Initialize(new RefManifest(
                        RefManifest.CurrentFormatVersion,
                        "doc-1",
                        schemeId,
                        "main",
                        revisionId,
                        "contracts/report.docx",
                        "2026-07-19T12:00:00Z"));
                }
            }
            Gateway = new FakeDocumentGateway(
                linkId,
                remoteFolder,
                ContentObjectStore.ComputeHash(
                    System.Text.Encoding.UTF8.GetBytes("document")));
            Actions = new FakeLocalDocumentActions();
            Preview = new FakeLocalDocumentPreview();
            Picker = new FakeLocalDocumentFilePicker();
            Service = new DocumentWorkspaceHostService(
                Gateway,
                mounts,
                capabilityStore ?? new DocumentCapabilityStore(),
                Actions,
                Preview,
                Picker,
                partitionKey: servicePartition);
        }

        public string WorkspaceRoot { get; }
        public string BackupRoot { get; }
        public string SourceRoot { get; }
        public string DocumentPath { get; }
        public FakeDocumentGateway Gateway { get; }
        public FakeLocalDocumentActions Actions { get; }
        public FakeLocalDocumentPreview Preview { get; }
        public FakeLocalDocumentFilePicker Picker { get; }
        public DocumentWorkspaceHostService Service { get; }

        public string CreatePickerFile(string fileName, string content)
        {
            string path = Path.Combine(SourceRoot, fileName);
            File.WriteAllText(path, content);
            return path;
        }

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

    public List<RegisterDocumentParams> RegisterRequests { get; } = [];
    public Exception? RegisterException { get; set; }

    public FakeDocumentGateway(
        string? linkId = "link-1",
        string folder = "contracts",
        string? mainHash = null)
    {
        _document = new DocumentSummary(
            linkId,
            "doc-1",
            "workspace-1",
            "report.docx",
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            "rev-1",
            mainHash ?? "abcdef",
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

    public Task<RegisterDocumentResult> RegisterDocumentAsync(
        RegisterDocumentParams request,
        CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        RegisterRequests.Add(request);
        if (RegisterException is not null) throw RegisterException;
        return Task.FromResult(new RegisterDocumentResult(
            request.DocumentId,
            "created",
            request.ItemCollection is null ? null : "link-imported"));
    }

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

internal sealed class FakeLocalDocumentFilePicker : ILocalDocumentFilePicker
{
    public string? SelectedPath { get; set; }
    public int PickCount { get; private set; }
    public DocumentFilePickPurpose? LastPurpose { get; private set; }
    public string? LastSuggestedFileName { get; private set; }

    public Task<string?> PickFileAsync(
        DocumentFilePickPurpose purpose,
        string? suggestedFileName,
        CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        PickCount++;
        LastPurpose = purpose;
        LastSuggestedFileName = suggestedFileName;
        return Task.FromResult(SelectedPath);
    }
}
