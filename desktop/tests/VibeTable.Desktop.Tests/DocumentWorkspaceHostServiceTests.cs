using System.Collections.Concurrent;
using System.IO;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.Workspace;
using VibeTable.Workspace.Domain;
using VibeTable.Workspace.Services;
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
    public async Task ListAsync_PublishesReachableLocalRevisionsAfterIndexedHead()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        new RevisionStore(fixture.BackupRoot, json).Write(new RevisionManifest(
            RevisionManifest.CurrentFormatVersion,
            "rev-2",
            "doc-1",
            "scheme-1",
            ParentRevisionId: "rev-1",
            SourceRevisionId: null,
            RestoredFromRevisionId: null,
            Sequence: 2,
            VersionLabel: "V2",
            Kind: RevisionKind.Formal,
            ContentHash: "b".PadLeft(64, 'b'),
            Size: 9,
            MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            WorkingRelativePath: "contracts/report.docx",
            CreatedAt: "2026-07-20T12:00:00Z",
            CreatedBy: "local-user",
            DeviceId: "device-1",
            Comment: "version two"));
        new RefStore(fixture.BackupRoot, json).UpdateHead(
            "doc-1",
            "scheme-1",
            "rev-1",
            "rev-2",
            "2026-07-20T12:00:00Z");

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        var request = fixture.Gateway.PublishRequests.Single();
        Assert.AreEqual(1, request.Revisions.Count);
        Assert.AreEqual("rev-2", request.Revisions[0].RevisionId);
        Assert.AreEqual("rev-1", request.Revisions[0].ParentRevisionId);
        Assert.AreEqual("formal", request.Revisions[0].Kind);
        Assert.AreEqual("2026-07-20T12:00:00Z", request.Revisions[0].CreatedAt);
        Assert.IsNotNull(request.HeadAdvance);
        Assert.AreEqual("rev-1", request.HeadAdvance.ExpectedHeadRevisionId);
        Assert.AreEqual("rev-2", request.HeadAdvance.NewHeadRevisionId);
        Assert.IsTrue(request.IdempotencyKey.StartsWith(
            "workspace-",
            StringComparison.Ordinal));
    }

    [TestMethod]
    public async Task ListAsync_PublishFailureKeepsDurableOutboxForRetry()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var revision = new RevisionManifest(
            RevisionManifest.CurrentFormatVersion,
            "rev-2",
            "doc-1",
            "scheme-1",
            ParentRevisionId: "rev-1",
            SourceRevisionId: null,
            RestoredFromRevisionId: null,
            Sequence: 2,
            VersionLabel: "V2",
            Kind: RevisionKind.Formal,
            ContentHash: new string('c', 64),
            Size: 9,
            MimeType: "application/octet-stream",
            WorkingRelativePath: "contracts/report.docx",
            CreatedAt: "2026-07-20T12:00:00Z",
            CreatedBy: null,
            DeviceId: null,
            Comment: null);
        new RevisionStore(fixture.BackupRoot, json).Write(revision);
        new RefStore(fixture.BackupRoot, json).UpdateHead(
            "doc-1",
            "scheme-1",
            "rev-1",
            "rev-2",
            "2026-07-20T12:00:00Z");
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        outbox.Enqueue(revision);
        fixture.Gateway.PublishException = new IOException("offline");

        var payload = await fixture.Service.ListAsync(
            "orders",
            "42",
            CancellationToken.None);

        Assert.AreEqual(1, payload.Entries.Count);
        Assert.AreEqual(1, outbox.ListByDocument("doc-1").Count);

        fixture.Gateway.PublishException = null;
        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);
        Assert.AreEqual(0, outbox.ListByDocument("doc-1").Count);
    }

    [TestMethod]
    public async Task ListAsync_PublishesOutboxOnlyRevisionWithoutAdvancingHead()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var orphan = new RevisionManifest(
            RevisionManifest.CurrentFormatVersion,
            "orphan-rev-2",
            "doc-1",
            "scheme-1",
            ParentRevisionId: "rev-1",
            SourceRevisionId: null,
            RestoredFromRevisionId: null,
            Sequence: 2,
            VersionLabel: "conflict/V2",
            Kind: RevisionKind.Formal,
            ContentHash: new string('d', 64),
            Size: 9,
            MimeType: "application/octet-stream",
            WorkingRelativePath: "contracts/report.docx",
            CreatedAt: "2026-07-20T12:30:00Z",
            CreatedBy: null,
            DeviceId: null,
            Comment: "preserved fork");
        new RevisionStore(fixture.BackupRoot, json).Write(orphan);
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        outbox.Enqueue(orphan);

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        var request = fixture.Gateway.PublishRequests.Single();
        Assert.AreEqual("orphan-rev-2", request.Revisions.Single().RevisionId);
        Assert.IsNull(request.HeadAdvance);
        Assert.AreEqual(0, outbox.ListByDocument("doc-1").Count);
    }

    [TestMethod]
    public async Task ListAsync_ChunksMoreThanOneHundredRevisionsAndAdvancesHeadOnce()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var revisions = new RevisionStore(fixture.BackupRoot, json);
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        string parent = "rev-1";
        for (int sequence = 2; sequence <= 102; sequence++)
        {
            string revisionId = $"rev-{sequence}";
            var revision = new RevisionManifest(
                RevisionManifest.CurrentFormatVersion,
                revisionId,
                "doc-1",
                "scheme-1",
                ParentRevisionId: parent,
                SourceRevisionId: null,
                RestoredFromRevisionId: null,
                Sequence: sequence,
                VersionLabel: $"V{sequence}",
                Kind: RevisionKind.Formal,
                ContentHash: sequence.ToString("x64"),
                Size: sequence,
                MimeType: "application/octet-stream",
                WorkingRelativePath: "contracts/report.docx",
                CreatedAt: $"2026-07-20T12:{sequence % 60:00}:00Z",
                CreatedBy: null,
                DeviceId: null,
                Comment: null);
            revisions.Write(revision);
            outbox.Enqueue(revision);
            parent = revisionId;
        }
        new RefStore(fixture.BackupRoot, json).UpdateHead(
            "doc-1",
            "scheme-1",
            "rev-1",
            "rev-102",
            "2026-07-20T13:00:00Z");

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        Assert.AreEqual(2, fixture.Gateway.PublishRequests.Count);
        Assert.AreEqual(100, fixture.Gateway.PublishRequests[0].Revisions.Count);
        Assert.IsNull(fixture.Gateway.PublishRequests[0].HeadAdvance);
        Assert.AreEqual(1, fixture.Gateway.PublishRequests[1].Revisions.Count);
        Assert.IsNotNull(fixture.Gateway.PublishRequests[1].HeadAdvance);
        Assert.AreEqual(
            "rev-102",
            fixture.Gateway.PublishRequests[1].HeadAdvance!.NewHeadRevisionId);
        Assert.AreEqual(0, outbox.ListByDocument("doc-1").Count);
    }

    [TestMethod]
    public async Task ListAsync_EmptyPublishReceiptKeepsDurableOutbox()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var revision = CreatePendingRevision(
            fixture,
            json,
            "rev-2",
            "rev-1",
            2);
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        outbox.Enqueue(revision);
        fixture.Gateway.PublishResponder = (_, _) =>
            new PublishIndexBatchResult([], []);

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        Assert.AreEqual(1, outbox.ListByDocument("doc-1").Count);
    }

    [TestMethod]
    public async Task ListAsync_DuplicatePublishReceiptKeepsDurableOutbox()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var revision = CreatePendingRevision(
            fixture,
            json,
            "rev-2",
            "rev-1",
            2);
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        outbox.Enqueue(revision);
        fixture.Gateway.PublishResponder = (_, _) =>
            new PublishIndexBatchResult(
                [
                    new PublishResult("rev-2", "created"),
                    new PublishResult("rev-2", "unchanged"),
                ],
                []);

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        Assert.AreEqual(1, outbox.ListByDocument("doc-1").Count);
    }

    [TestMethod]
    public async Task ListAsync_ImmutableConflictIsDurablyIsolatedAndNotRetried()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var created = CreatePendingRevision(
            fixture,
            json,
            "outbox-created",
            "rev-1",
            2);
        var conflicted = CreatePendingRevision(
            fixture,
            json,
            "outbox-conflict",
            "rev-1",
            2);
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        outbox.Enqueue(created);
        outbox.Enqueue(conflicted);
        fixture.Gateway.PublishResponder = (request, _) =>
            new PublishIndexBatchResult(
                request.Revisions.Select(revision =>
                    new PublishResult(
                        revision.RevisionId,
                        revision.RevisionId == conflicted.RevisionId
                            ? "conflict"
                            : "created"))
                    .ToList(),
                [conflicted.RevisionId]);

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);
        int requestCountAfterConflict = fixture.Gateway.PublishRequests.Count;
        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        Assert.AreEqual(0, outbox.ListByDocument("doc-1").Count);
        var issues = outbox.ListConflictedByDocument("doc-1");
        Assert.AreEqual(1, issues.Count);
        Assert.AreEqual(conflicted.RevisionId, issues[0].Revision.RevisionId);
        Assert.AreEqual("conflicted", issues[0].Status);
        Assert.AreEqual(requestCountAfterConflict, fixture.Gateway.PublishRequests.Count);
    }

    [TestMethod]
    public async Task ListAsync_SecondBatchFailureRetriesWithSameIdempotencyKeys()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        string parent = "rev-1";
        for (int sequence = 2; sequence <= 102; sequence++)
        {
            var revision = CreatePendingRevision(
                fixture,
                json,
                $"rev-{sequence}",
                parent,
                sequence);
            outbox.Enqueue(revision);
            parent = revision.RevisionId;
        }
        new RefStore(fixture.BackupRoot, json).UpdateHead(
            "doc-1",
            "scheme-1",
            "rev-1",
            "rev-102",
            "2026-07-20T13:00:00Z");
        fixture.Gateway.PublishFailure = (_, call) =>
            call == 2 ? new IOException("second batch offline") : null;

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);
        Assert.AreEqual(2, fixture.Gateway.PublishRequests.Count);
        Assert.AreEqual(1, outbox.ListByDocument("doc-1").Count);

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        Assert.AreEqual(4, fixture.Gateway.PublishRequests.Count);
        Assert.AreEqual(
            fixture.Gateway.PublishRequests[0].IdempotencyKey,
            fixture.Gateway.PublishRequests[2].IdempotencyKey);
        Assert.AreEqual(
            fixture.Gateway.PublishRequests[1].IdempotencyKey,
            fixture.Gateway.PublishRequests[3].IdempotencyKey);
        Assert.AreEqual(0, outbox.ListByDocument("doc-1").Count);
    }

    [TestMethod]
    public async Task ListAsync_ResponseLostRetriesWithSameIdempotencyKey()
    {
        using var fixture = new DocumentFixture();
        var json = new AtomicJsonStore();
        var revision = CreatePendingRevision(
            fixture,
            json,
            "rev-2",
            "rev-1",
            2);
        var outbox = new RevisionPublishOutboxStore(fixture.BackupRoot, json);
        outbox.Enqueue(revision);
        fixture.Gateway.PublishFailure = (_, call) =>
            call == 1 ? new IOException("response lost") : null;

        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);
        await fixture.Service.ListAsync("orders", "42", CancellationToken.None);

        Assert.AreEqual(2, fixture.Gateway.PublishRequests.Count);
        Assert.AreEqual(
            fixture.Gateway.PublishRequests[0].IdempotencyKey,
            fixture.Gateway.PublishRequests[1].IdempotencyKey);
        Assert.AreEqual(0, outbox.ListByDocument("doc-1").Count);
    }

    [TestMethod]
    public async Task ProjectedPathMetadata_CannotRedirectLocalCapability()
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
        CollectionAssert.AreEqual(
            System.Text.Encoding.UTF8.GetBytes("document"),
            File.ReadAllBytes(fixture.DocumentPath));
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

    [TestMethod]
    public async Task CommitRevision_UsesObservedHeadAndReturnsOnlyOpaqueRevisionHandle()
    {
        using var fixture = new DocumentFixture();
        var list = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);
        string entryHandle = list.Entries.Single().EntryHandle;
        File.WriteAllText(fixture.DocumentPath, "changed");

        var result = await fixture.Service.CommitRevisionAsync(
            entryHandle,
            "local edit",
            schemeHandle: null,
            CancellationToken.None);

        Assert.IsTrue(result.RevisionHandle.StartsWith("rev-", StringComparison.Ordinal));
        Assert.AreEqual("R2", result.CurrentRevision);
        Assert.IsNull(result.SchemeHandle);
        string json = JsonSerializer.Serialize(result);
        Assert.IsFalse(json.Contains("doc-1", StringComparison.Ordinal));
        Assert.IsFalse(json.Contains("scheme-1", StringComparison.Ordinal));
        var current = new RefStore(fixture.BackupRoot, new AtomicJsonStore())
            .Read("doc-1", "scheme-1");
        Assert.IsNotNull(current);
        Assert.AreNotEqual("rev-1", current.HeadRevisionId);
        var revision = new RevisionStore(fixture.BackupRoot, new AtomicJsonStore())
            .Read("doc-1", current.HeadRevisionId);
        Assert.IsNotNull(revision);
        Assert.AreEqual("rev-1", revision.ParentRevisionId);
        Assert.AreEqual("local edit", revision.Comment);
    }

    [TestMethod]
    public async Task CommitRevision_RejectsStaleEntryHeadWithoutAdvancingMainRef()
    {
        using var fixture = new DocumentFixture();
        var list = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);
        string entryHandle = list.Entries.Single().EntryHandle;
        var json = new AtomicJsonStore();
        var objects = new ContentObjectStore(fixture.BackupRoot);
        string concurrentPath = fixture.CreatePickerFile("concurrent.docx", "concurrent");
        var committed = objects.Commit(concurrentPath);
        new RevisionStore(fixture.BackupRoot, json).Write(new RevisionManifest(
            RevisionManifest.CurrentFormatVersion,
            "rev-concurrent",
            "doc-1",
            "scheme-1",
            "rev-1",
            SourceRevisionId: null,
            RestoredFromRevisionId: null,
            Sequence: 2,
            VersionLabel: "V2",
            Kind: RevisionKind.Formal,
            committed.ContentHash,
            committed.Size,
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            "contracts/report.docx",
            "2026-07-26T12:00:00Z",
            null,
            null,
            null));
        new RefStore(fixture.BackupRoot, json).UpdateHead(
            "doc-1",
            "scheme-1",
            "rev-1",
            "rev-concurrent",
            "2026-07-26T12:00:00Z");

        var error = await Assert.ThrowsExactlyAsync<DocumentFileOperationException>(
            () => fixture.Service.CommitRevisionAsync(
                entryHandle,
                note: null,
                schemeHandle: null,
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_VERSION_CONFLICT", error.Code);
        Assert.AreEqual(
            "rev-concurrent",
            new RefStore(fixture.BackupRoot, json)
                .Read("doc-1", "scheme-1")!
                .HeadRevisionId);
    }

    [TestMethod]
    public async Task PreviewAndRestoreRevision_UseHostTemporaryCopyAndCreateRestoreRevision()
    {
        using var fixture = new DocumentFixture();
        var list = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);
        var entry = list.Entries.Single();
        var history = await fixture.Service.ReadHistoryAsync(
            entry.EntryHandle,
            100,
            0,
            CancellationToken.None);
        string revisionHandle = history.Revisions.Single().RevisionHandle;
        File.WriteAllText(fixture.DocumentPath, "unsaved working content");

        var preview = fixture.Service.PreviewRevision(
            entry.EntryHandle,
            revisionHandle);

        Assert.AreEqual("preview", preview.Action);
        Assert.IsNotNull(fixture.Preview.PreviewedPath);
        Assert.IsFalse(
            fixture.Preview.PreviewedPath.StartsWith(
                fixture.WorkspaceRoot,
                StringComparison.OrdinalIgnoreCase));
        Assert.AreEqual("document", File.ReadAllText(fixture.Preview.PreviewedPath));

        var restored = await fixture.Service.RestoreRevisionAsync(
            entry.EntryHandle,
            revisionHandle,
            CancellationToken.None);

        Assert.AreEqual("document", File.ReadAllText(fixture.DocumentPath));
        Assert.IsTrue(restored.RevisionHandle.StartsWith("rev-", StringComparison.Ordinal));
        var main = new RefStore(fixture.BackupRoot, new AtomicJsonStore())
            .Read("doc-1", "scheme-1");
        var restoreRevision = new RevisionStore(
            fixture.BackupRoot,
            new AtomicJsonStore()).Read("doc-1", main!.HeadRevisionId);
        Assert.IsNotNull(restoreRevision);
        Assert.AreEqual(RevisionKind.Restore, restoreRevision.Kind);
        Assert.AreEqual("rev-1", restoreRevision.RestoredFromRevisionId);
    }

    [TestMethod]
    public async Task RestoreRevision_WhenAtomicReplaceFails_CompensatesMainRef()
    {
        using var fixture = new DocumentFixture();
        var list = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);
        var entry = list.Entries.Single();
        var history = await fixture.Service.ReadHistoryAsync(
            entry.EntryHandle,
            100,
            0,
            CancellationToken.None);
        File.WriteAllText(fixture.DocumentPath, "dirty working bytes");
        byte[] before = File.ReadAllBytes(fixture.DocumentPath);

        DocumentFileOperationException error;
        using (var locked = new FileStream(
            fixture.DocumentPath,
            FileMode.Open,
            FileAccess.ReadWrite,
            FileShare.None))
        {
            error = await Assert.ThrowsExactlyAsync<DocumentFileOperationException>(
                () => fixture.Service.RestoreRevisionAsync(
                    entry.EntryHandle,
                    history.Revisions.Single().RevisionHandle,
                    CancellationToken.None));
        }

        Assert.AreEqual("DOCUMENT_RESTORE_MATERIALIZE_FAILED", error.Code);
        Assert.AreEqual(
            "rev-1",
            new RefStore(fixture.BackupRoot, new AtomicJsonStore())
                .Read("doc-1", "scheme-1")!
                .HeadRevisionId);
        CollectionAssert.AreEqual(before, File.ReadAllBytes(fixture.DocumentPath));
        Assert.IsTrue(new RevisionStore(
            fixture.BackupRoot,
            new AtomicJsonStore())
            .ListByDocument("doc-1")
            .Any(revision => revision.Kind == RevisionKind.Restore));
    }

    [TestMethod]
    public async Task SchemeLifecycle_UsesOpaqueHandlesAndPersistsArchivedStatus()
    {
        using var fixture = new DocumentFixture();
        var list = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);
        string entryHandle = list.Entries.Single().EntryHandle;

        var created = await fixture.Service.CreateSchemeAsync(
            entryHandle,
            "Option A",
            baseRevisionHandle: null,
            CancellationToken.None);
        Assert.IsTrue(created.Scheme.SchemeHandle.StartsWith(
            "scheme-",
            StringComparison.Ordinal));
        Assert.IsFalse(created.Scheme.Archived);
        string payloadJson = JsonSerializer.Serialize(created);
        Assert.IsFalse(payloadJson.Contains("scheme-1", StringComparison.Ordinal));
        Assert.IsFalse(payloadJson.Contains("doc-1", StringComparison.Ordinal));

        var renamed = await fixture.Service.RenameSchemeAsync(
            entryHandle,
            created.Scheme.SchemeHandle,
            "Option B",
            CancellationToken.None);
        Assert.AreEqual("Option B", renamed.Scheme.Name);
        var archived = await fixture.Service.ArchiveSchemeAsync(
            entryHandle,
            renamed.Scheme.SchemeHandle,
            CancellationToken.None);
        Assert.IsTrue(archived.Scheme.Archived);

        var schemes = fixture.Service.ListSchemes(entryHandle);
        Assert.AreEqual(2, schemes.Schemes.Count);
        Assert.IsTrue(schemes.Schemes.Single(item => item.Name == "Option B").Archived);
        var stored = new SchemeService(
            fixture.BackupRoot,
            new ContentObjectStore(fixture.BackupRoot),
            new RevisionStore(fixture.BackupRoot, new AtomicJsonStore()),
            new RefStore(fixture.BackupRoot, new AtomicJsonStore()),
            new AtomicJsonStore())
            .ListSchemes("doc-1")
            .Single(item => item.SchemeName == "Option B");
        Assert.AreEqual(SchemeStatus.Archived, stored.Status);
    }

    [TestMethod]
    public async Task SchemeMutation_WithStaleHandle_ReturnsStableConflict()
    {
        using var fixture = new DocumentFixture();
        var list = await fixture.Service.ListAsync(
            "orders", "42", CancellationToken.None);
        string entryHandle = list.Entries.Single().EntryHandle;
        var created = await fixture.Service.CreateSchemeAsync(
            entryHandle,
            "Option A",
            baseRevisionHandle: null,
            CancellationToken.None);
        await fixture.Service.CommitRevisionAsync(
            entryHandle,
            note: null,
            created.Scheme.SchemeHandle,
            CancellationToken.None);

        var renameError = await Assert.ThrowsExactlyAsync<DocumentFileOperationException>(
            () => fixture.Service.RenameSchemeAsync(
                entryHandle,
                created.Scheme.SchemeHandle,
                "Stale rename",
                CancellationToken.None));
        var archiveError = await Assert.ThrowsExactlyAsync<DocumentFileOperationException>(
            () => fixture.Service.ArchiveSchemeAsync(
                entryHandle,
                created.Scheme.SchemeHandle,
                CancellationToken.None));

        Assert.AreEqual("DOCUMENT_VERSION_CONFLICT", renameError.Code);
        Assert.AreEqual("DOCUMENT_VERSION_CONFLICT", archiveError.Code);
        Assert.AreEqual(
            "Option A",
            fixture.Service.ListSchemes(entryHandle)
                .Schemes
                .Single(scheme => scheme.Name == "Option A")
                .Name);
    }

    [TestMethod]
    public async Task RestoreRecovery_PreparedBeforeRefCommit_RollsBackAndCleans()
    {
        using var fixture = new DocumentFixture();
        File.WriteAllText(fixture.DocumentPath, "dirty before prepared crash");
        byte[] before = File.ReadAllBytes(fixture.DocumentPath);
        var interrupted = PrepareInterruptedRestore(
            fixture,
            commitRef: false,
            materialize: false,
            removeStage: false);

        await fixture.Service.ListGlobalAsync(CancellationToken.None);

        Assert.AreEqual(
            "rev-1",
            new RefStore(fixture.BackupRoot, new AtomicJsonStore())
                .Read("doc-1", "scheme-1")!
                .HeadRevisionId);
        CollectionAssert.AreEqual(before, File.ReadAllBytes(fixture.DocumentPath));
        Assert.AreEqual(0, interrupted.Versions.ListRestoreTransactions().Count);
        Assert.IsFalse(File.Exists(interrupted.StagedPath));
    }

    [TestMethod]
    public async Task RestoreRecovery_RefCommittedBeforeJournalMark_CompletesMaterialization()
    {
        using var fixture = new DocumentFixture();
        File.WriteAllText(fixture.DocumentPath, "dirty before ref crash");
        var interrupted = PrepareInterruptedRestore(
            fixture,
            commitRef: true,
            materialize: false,
            removeStage: false);

        await fixture.Service.ListGlobalAsync(CancellationToken.None);

        Assert.AreEqual(
            interrupted.RestoreRevisionId,
            new RefStore(fixture.BackupRoot, new AtomicJsonStore())
                .Read("doc-1", "scheme-1")!
                .HeadRevisionId);
        CollectionAssert.AreEqual(
            System.Text.Encoding.UTF8.GetBytes("document"),
            File.ReadAllBytes(fixture.DocumentPath));
        Assert.AreEqual(0, interrupted.Versions.ListRestoreTransactions().Count);
        Assert.IsFalse(File.Exists(interrupted.StagedPath));
    }

    [TestMethod]
    public async Task RestoreRecovery_RefCommittedWithoutStage_CompensatesRef()
    {
        using var fixture = new DocumentFixture();
        File.WriteAllText(fixture.DocumentPath, "dirty before missing stage");
        byte[] before = File.ReadAllBytes(fixture.DocumentPath);
        var interrupted = PrepareInterruptedRestore(
            fixture,
            commitRef: true,
            materialize: false,
            removeStage: true);

        await fixture.Service.ListGlobalAsync(CancellationToken.None);

        Assert.AreEqual(
            "rev-1",
            new RefStore(fixture.BackupRoot, new AtomicJsonStore())
                .Read("doc-1", "scheme-1")!
                .HeadRevisionId);
        CollectionAssert.AreEqual(before, File.ReadAllBytes(fixture.DocumentPath));
        Assert.AreEqual(0, interrupted.Versions.ListRestoreTransactions().Count);
    }

    [TestMethod]
    public async Task RestoreRecovery_MaterializedBeforeJournalCleanup_VerifiesAndCleans()
    {
        using var fixture = new DocumentFixture();
        File.WriteAllText(fixture.DocumentPath, "dirty before cleanup crash");
        var interrupted = PrepareInterruptedRestore(
            fixture,
            commitRef: true,
            materialize: true,
            removeStage: false);

        await fixture.Service.ListGlobalAsync(CancellationToken.None);

        Assert.AreEqual(
            interrupted.RestoreRevisionId,
            new RefStore(fixture.BackupRoot, new AtomicJsonStore())
                .Read("doc-1", "scheme-1")!
                .HeadRevisionId);
        CollectionAssert.AreEqual(
            System.Text.Encoding.UTF8.GetBytes("document"),
            File.ReadAllBytes(fixture.DocumentPath));
        Assert.AreEqual(0, interrupted.Versions.ListRestoreTransactions().Count);
    }

    [TestMethod]
    public async Task RestoreRecovery_UnexpectedLaterHead_BlocksAccessAndPreservesJournal()
    {
        using var fixture = new DocumentFixture();
        File.WriteAllText(fixture.DocumentPath, "concurrent content");
        var interrupted = PrepareInterruptedRestore(
            fixture,
            commitRef: true,
            materialize: false,
            removeStage: false);
        var later = interrupted.Versions.CommitFormal(
            fixture.DocumentPath,
            "contracts/report.docx",
            "doc-1",
            "scheme-1",
            interrupted.RestoreRevisionId,
            3,
            "Concurrent V3",
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
            "other-process",
            null,
            null,
            "2026-07-26T12:02:00Z");
        Assert.IsTrue(later.RefUpdated);

        var error = await Assert.ThrowsExactlyAsync<DocumentFileOperationException>(
            () => fixture.Service.ListGlobalAsync(CancellationToken.None));

        Assert.AreEqual("DOCUMENT_RESTORE_RECOVERY_REQUIRED", error.Code);
        Assert.AreEqual(1, interrupted.Versions.ListRestoreTransactions().Count);
        Assert.IsTrue(File.Exists(interrupted.StagedPath));
        Assert.AreEqual(
            later.RevisionId,
            new RefStore(fixture.BackupRoot, new AtomicJsonStore())
                .Read("doc-1", "scheme-1")!
                .HeadRevisionId);
    }

    private static InterruptedRestore PrepareInterruptedRestore(
        DocumentFixture fixture,
        bool commitRef,
        bool materialize,
        bool removeStage)
    {
        var json = new AtomicJsonStore();
        var objects = new ContentObjectStore(fixture.BackupRoot);
        var revisions = new RevisionStore(fixture.BackupRoot, json);
        var refs = new RefStore(fixture.BackupRoot, json);
        var versions = new WorkspaceVersionService(
            fixture.BackupRoot,
            objects,
            revisions,
            refs,
            json);
        RevisionManifest source = revisions.Read("doc-1", "rev-1")!;
        string transactionId = Guid.NewGuid().ToString("N");
        string restoreRevisionId = Guid.NewGuid().ToString("N");
        string stagedRelativePath =
            $"contracts/report.docx.restore-{transactionId}.partial";
        string stagedPath = WorkspacePathGuard.ResolveAndCheck(
            fixture.WorkspaceRoot,
            stagedRelativePath);
        objects.Restore(source.ContentHash, stagedPath);
        versions.PrepareRestoreTransaction(
            new WorkspaceVersionService.RestoreTransactionJournal(
                WorkspaceVersionService.RestoreTransactionJournal.CurrentFormatVersion,
                transactionId,
                "doc-1",
                "scheme-1",
                "rev-1",
                restoreRevisionId,
                "rev-1",
                "contracts/report.docx",
                stagedRelativePath,
                source.ContentHash,
                source.Size,
                WorkspaceVersionService.RestoreTransactionStage.Prepared,
                "2026-07-26T12:00:00Z"));

        if (commitRef)
        {
            var outcome = versions.RestoreRevisionAsFormal(
                "doc-1",
                "scheme-1",
                "rev-1",
                "rev-1",
                "Restore V1",
                "local",
                null,
                "interrupted restore test",
                "2026-07-26T12:01:00Z",
                restoreRevisionId);
            Assert.IsTrue(outcome.RefUpdated);
        }
        if (materialize)
            File.Move(stagedPath, fixture.DocumentPath, overwrite: true);
        if (removeStage)
            File.Delete(stagedPath);

        return new InterruptedRestore(
            versions,
            restoreRevisionId,
            stagedPath);
    }

    private sealed record InterruptedRestore(
        WorkspaceVersionService Versions,
        string RestoreRevisionId,
        string StagedPath);

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
                    string objectSeedPath = Path.Combine(
                        SourceRoot,
                        ".initial-document-object");
                    File.WriteAllText(objectSeedPath, "document");
                    string contentHash = new ContentObjectStore(BackupRoot)
                        .Commit(objectSeedPath)
                        .ContentHash;
                    File.Delete(objectSeedPath);
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
            Service.Dispose();
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

    private static RevisionManifest CreatePendingRevision(
        DocumentFixture fixture,
        AtomicJsonStore json,
        string revisionId,
        string parentRevisionId,
        int sequence)
    {
        var revision = new RevisionManifest(
            RevisionManifest.CurrentFormatVersion,
            revisionId,
            "doc-1",
            "scheme-1",
            parentRevisionId,
            SourceRevisionId: null,
            RestoredFromRevisionId: null,
            Sequence: sequence,
            VersionLabel: $"V{sequence}",
            Kind: RevisionKind.Formal,
            ContentHash: sequence.ToString("x64"),
            Size: sequence,
            MimeType: "application/octet-stream",
            WorkingRelativePath: "contracts/report.docx",
            CreatedAt: $"2026-07-20T12:{sequence % 60:00}:00Z",
            CreatedBy: null,
            DeviceId: null,
            Comment: null);
        new RevisionStore(fixture.BackupRoot, json).Write(revision);
        return revision;
    }
}

internal sealed class FakeDocumentGateway : IDocumentWorkspaceRpcGateway
{
    private readonly DocumentSummary _document;

    public List<RegisterDocumentParams> RegisterRequests { get; } = [];
    public List<PublishIndexBatchParams> PublishRequests { get; } = [];
    public Exception? RegisterException { get; set; }
    public Exception? PublishException { get; set; }
    public Func<PublishIndexBatchParams, int, Exception?>? PublishFailure { get; set; }
    public Func<PublishIndexBatchParams, int, PublishIndexBatchResult>? PublishResponder { get; set; }

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

    public Task<PublishIndexBatchResult> PublishIndexBatchAsync(
        PublishIndexBatchParams request,
        CancellationToken token)
    {
        token.ThrowIfCancellationRequested();
        PublishRequests.Add(request);
        if (PublishException is not null) throw PublishException;
        int call = PublishRequests.Count;
        Exception? failure = PublishFailure?.Invoke(request, call);
        if (failure is not null) throw failure;
        if (PublishResponder is not null)
            return Task.FromResult(PublishResponder(request, call));
        return Task.FromResult(new PublishIndexBatchResult(
            request.Revisions
                .Select(revision => new PublishResult(revision.RevisionId, "created"))
                .ToList(),
            []));
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
