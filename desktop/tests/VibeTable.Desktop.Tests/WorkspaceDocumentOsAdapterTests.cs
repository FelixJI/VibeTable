using System.Net;
using System.Text;
using System.Text.Json;
using VibeTable.Desktop.Services;
using VibeTable.Infrastructure.PocketBase;

namespace VibeTable.Desktop.Tests;

[TestClass]
public sealed class WorkspaceDocumentOsAdapterTests
{
    private static readonly Guid WorkspaceId =
        Guid.Parse("11111111-1111-4111-8111-111111111111");
    private static readonly Guid DocumentId =
        Guid.Parse("22222222-2222-4222-8222-222222222222");
    private static readonly Guid RevisionId =
        Guid.Parse("33333333-3333-4333-8333-333333333333");

    [TestMethod]
    public void CanonicalRelativePathIsResolvedFromFilesRoot()
    {
        string root = Path.Combine(Path.GetTempPath(), Guid.NewGuid().ToString("N"));

        string resolved = WorkspaceDocumentOsAdapter.ResolveWorkspacePath(
            root,
            "reports/q3.txt");

        Assert.AreEqual(
            Path.GetFullPath(Path.Combine(root, "files", "reports", "q3.txt")),
            resolved);
        Assert.ThrowsExactly<DocumentFileOperationException>(() =>
            WorkspaceDocumentOsAdapter.ResolveWorkspacePath(root, "../escape.txt"));
        Assert.ThrowsExactly<DocumentFileOperationException>(() =>
            WorkspaceDocumentOsAdapter.ResolveWorkspacePath(
                root,
                Path.GetFullPath(Path.Combine(root, "absolute.txt"))));
    }

    [TestMethod]
    public async Task ImportUsesOneShotSidecarGrantAndNeverCopiesInDesktop()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string source = Path.Combine(directory.Path, "report.txt");
        Directory.CreateDirectory(Path.Combine(workspaceRoot, "files"));
        await File.WriteAllTextAsync(source, "authoritative input");
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            Assert.AreEqual(
                WorkspaceDocumentOsAdapter.ImportDocumentMethod,
                root.GetProperty("method").GetString());
            JsonElement parameters = root.GetProperty("params");
            Assert.AreEqual(
                "report.txt",
                parameters.GetProperty("relativePath").GetString());
            Assert.AreEqual(
                "text/plain",
                parameters.GetProperty("mimeType").GetString());
            string grantId = parameters.GetProperty("pathGrant").GetString()!;
            Assert.IsTrue(grantId.StartsWith(
                "host-path-grant://",
                StringComparison.Ordinal));
            using JsonDocument grant = JsonDocument.Parse(DecodeGrant(request));
            Assert.AreEqual(grantId, grant.RootElement
                .GetProperty("grantId").GetString());
            Assert.AreEqual("file-import", grant.RootElement
                .GetProperty("purpose").GetString());
            Assert.AreEqual(
                Path.GetFullPath(source),
                grant.RootElement.GetProperty("path").GetString());
            Assert.IsFalse(File.Exists(
                Path.Combine(workspaceRoot, "files", "report.txt")));
            return RpcSuccess(root, FileDocument("report.txt", 2));
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [WorkspaceDocumentOsAdapter.ImportDocumentMethod]);

        WorkspaceDocumentImportResult result =
            await adapter.ImportFromHostPathAsync(
                source,
                CancellationToken.None);

        Assert.AreEqual(WorkspaceId, result.WorkspaceId);
        Assert.AreEqual("report.txt", result.RelativePath);
        Assert.IsFalse(File.Exists(
            Path.Combine(workspaceRoot, "files", "report.txt")));
    }

    [TestMethod]
    public async Task ListProjectsFormalVersionSeparatelyFromEffectiveUuid()
    {
        using var directory = new TemporaryDirectory();
        string workspaceRoot = Path.Combine(directory.Path, "workspace");
        string materialized = Path.Combine(
            workspaceRoot,
            "files",
            "reports",
            "q3.txt");
        Directory.CreateDirectory(Path.GetDirectoryName(materialized)!);
        await File.WriteAllTextAsync(materialized, "current leaf");
        var handler = new RecordingHandler(request =>
        {
            using JsonDocument body = JsonDocument.Parse(
                request.Content!.ReadAsStringAsync().GetAwaiter().GetResult());
            JsonElement root = body.RootElement;
            return RpcSuccess(
                root,
                $$"""{"documents":[{{FileDocument("reports/q3.txt", 4)}}]}""");
        });
        using WorkspaceV2HttpGateway gateway = Gateway(handler);
        using WorkspaceDocumentOsAdapter adapter = Adapter(
            workspaceRoot,
            gateway,
            [WorkspaceDocumentOsAdapter.ListDocumentsMethod]);

        DocumentListPayload result =
            await adapter.ListGlobalAsync(CancellationToken.None);

        DocumentBridgeEntry entry = result.Entries.Single();
        Assert.AreEqual("V3", entry.CurrentRevision);
        Assert.AreEqual(RevisionId.ToString("D"), entry.EffectiveRevisionId);
        Assert.AreEqual("available", entry.Availability);
        Assert.AreEqual("q3.txt", entry.DisplayName);
    }

    private static WorkspaceDocumentOsAdapter Adapter(
        string workspaceRoot,
        WorkspaceV2HttpGateway gateway,
        IReadOnlyCollection<string> methods)
    {
        var binding = new WorkspaceDocumentBinding(
            WorkspaceId,
            7,
            true,
            workspaceRoot,
            gateway,
            methods);
        return new WorkspaceDocumentOsAdapter(
            () => binding,
            new DocumentCapabilityStore(),
            new NoopActions(),
            new NoopPreview(),
            new NoopPicker());
    }

    private static WorkspaceV2HttpGateway Gateway(HttpMessageHandler handler)
        => new(
            () => new PocketBaseAdminContext(
                new Uri(
                    "http://127.0.0.1:43125/api/vibetable/v1/admin/bootstrap"),
                new Uri("http://127.0.0.1:43125/"),
                "X-VibeTable-Session",
                "private-secret"),
            handler);

    private static string FileDocument(string relativePath, ulong nextFormalVersion)
        => $$"""
        {
          "contractVersion":"2.0",
          "documentId":"{{DocumentId:D}}",
          "workspaceId":"{{WorkspaceId:D}}",
          "relativePath":"{{relativePath}}",
          "status":"active",
          "effectiveRevisionId":"{{RevisionId:D}}",
          "nextRevisionOrdinal":{{nextFormalVersion}},
          "nextFormalVersion":{{nextFormalVersion}}
        }
        """;

    private static HttpResponseMessage RpcSuccess(
        JsonElement request,
        string resultJson)
        => Json(
            "{\"jsonrpc\":\"2.0\",\"id\":"
            + JsonSerializer.Serialize(request.GetProperty("id").GetString())
            + ",\"wire\":"
            + request.GetProperty("wire").GetRawText()
            + ",\"result\":"
            + resultJson
            + "}");

    private static string DecodeGrant(HttpRequestMessage request)
    {
        string encoded = request.Headers
            .GetValues("X-VibeTable-Path-Grant")
            .Single()
            .Replace('-', '+')
            .Replace('_', '/');
        encoded += new string('=', (4 - encoded.Length % 4) % 4);
        return Encoding.UTF8.GetString(Convert.FromBase64String(encoded));
    }

    private static HttpResponseMessage Json(string body)
        => new(HttpStatusCode.OK)
        {
            Content = new StringContent(body, Encoding.UTF8, "application/json"),
        };

    private sealed class RecordingHandler(
        Func<HttpRequestMessage, HttpResponseMessage> responder)
        : HttpMessageHandler
    {
        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request,
            CancellationToken cancellationToken)
            => Task.FromResult(responder(request));
    }

    private sealed class NoopActions : ILocalDocumentActions
    {
        public void Open(string fullPath) { }
        public void Reveal(string fullPath) { }
    }

    private sealed class NoopPreview : ILocalDocumentPreview
    {
        public bool CanPreview(string fullPath) => false;
        public void Show(string fullPath) { }
        public void Dispose() { }
    }

    private sealed class NoopPicker : ILocalDocumentFilePicker
    {
        public Task<string?> PickFileAsync(
            DocumentFilePickPurpose purpose,
            string? suggestedFileName,
            CancellationToken token)
            => Task.FromResult<string?>(null);
    }

    private sealed class TemporaryDirectory : IDisposable
    {
        public TemporaryDirectory()
        {
            Path = System.IO.Path.Combine(
                System.IO.Path.GetTempPath(),
                "vibetable-desktop-tests",
                Guid.NewGuid().ToString("N"));
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
